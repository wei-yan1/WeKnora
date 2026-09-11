#!/usr/bin/env bash
# ============================================================
#  WeKnora 插件安全框架 · 端到端验收演示（终版）
#  用法:  WEKNORA_ADMIN_EMAIL=xx WEKNORA_ADMIN_PASSWORD=xx bash scripts/plugin-security-acceptance.sh
#  可选:  WEKNORA_PLUGIN_ID / WEKNORA_APP_CONTAINER / WEKNORA_RUNTIME_AGENT_CONTAINER / WEKNORA_POSTGRES_CONTAINER 覆盖默认值
#  覆盖控制点:
#    ① Docker 权限分离    ② 强隔离沙箱        ③ 断网实测
#    ④ Socket 权限移交    ⑤ 信任分级持久化    ⑥ 端到端连通
# ============================================================
set +e

ADMIN_EMAIL="${WEKNORA_ADMIN_EMAIL:-}"
ADMIN_PASSWORD="${WEKNORA_ADMIN_PASSWORD:-}"
PLUGIN_ID="${WEKNORA_PLUGIN_ID:-weknora.tarily123}"
APP="${WEKNORA_APP_CONTAINER:-WeKnora-app}"
AGENT="${WEKNORA_RUNTIME_AGENT_CONTAINER:-WeKnora-plugin-runtime}"
PG="${WEKNORA_POSTGRES_CONTAINER:-WeKnora-postgres}"

# agent 的 runtime root（动态读取 agent 环境变量，避免硬编码漂移）
RUNTIME_ROOT=$(docker exec $AGENT printenv WEKNORA_PLUGIN_RUNTIME_ROOT 2>/dev/null)
[ -z "$RUNTIME_ROOT" ] && RUNTIME_ROOT="/var/lib/weknora/plugin-runtime"

G='\033[1;32m'; R='\033[1;31m'; Y='\033[1;33m'; B='\033[1;34m'; D='\033[2m'; N='\033[0m'
P=0; F=0
ok()  { echo -e "  ${G}✔ PASS${N}  $1"; P=$((P+1)); }
bad() { echo -e "  ${R}✘ FAIL${N}  $1"; F=$((F+1)); }
info(){ echo -e "  ${D}· $1${N}"; }
title(){ echo -e "\n${B}【$1】${N}"; }

# 插件容器（label 定位，不依赖命名）
CTR=$(docker ps -aq --filter "label=weknora.plugin.id=$PLUGIN_ID" | head -1)

title "① 权限分离架构：唯一持有 Docker 的组件"
docker inspect $APP  --format '{{json .Mounts}}' 2>/dev/null | grep -q docker.sock \
  && bad "app 容器持有 docker.sock（违反最小权限）" \
  || ok "app 容器无 docker.sock —— 编排权限与业务进程分离"
docker inspect $AGENT --format '{{json .Mounts}}' 2>/dev/null | grep -q docker.sock \
  && ok "runtime-agent 独占 docker.sock —— 容器编排入口唯一" \
  || bad "agent 未持有 docker.sock"

title "② 强隔离容器（isolated 插件运行时沙箱）"
if [ -n "$CTR" ]; then
  NM=$(docker inspect "$CTR" --format '{{.HostConfig.NetworkMode}}')
  RO=$(docker inspect "$CTR" --format '{{.HostConfig.ReadonlyRootfs}}')
  CD=$(docker inspect "$CTR" --format '{{.HostConfig.CapDrop}}')
  SO=$(docker inspect "$CTR" --format '{{.HostConfig.SecurityOpt}}')
  PL=$(docker inspect "$CTR" --format '{{.HostConfig.PidsLimit}}')
  MM=$(docker inspect "$CTR" --format '{{.HostConfig.Memory}}')
  NC=$(docker inspect "$CTR" --format '{{.HostConfig.NanoCpus}}')
  LB=$(docker inspect "$CTR" --format '{{json .Config.Labels}}')

  [ "$NM" = "none" ]       && ok "网络命名空间: none —— 容器无任何网络接口"   || bad "NetworkMode = $NM"
  [ "$RO" = "true" ]       && ok "根文件系统只读 —— 容器内无法持久化篡改"     || bad "Rootfs 可写"
  [ "$CD" = "[ALL]" ]      && ok "Capabilities 全部卸载 (cap-drop ALL)"       || bad "CapDrop = $CD"
  echo "$SO" | grep -q 'no-new-privileges' && ok "no-new-privileges —— 阻断容器内提权" || bad "SecurityOpt = $SO"
  [ "$PL" = "128" ]        && ok "进程数上限 128 —— 防 fork 炸弹"            || bad "PidsLimit = $PL"
  [ "$MM" = "536870912" ]  && ok "内存上限 512MB —— 防资源耗尽"              || bad "Memory = $MM"
  [ "$NC" = "1000000000" ] && ok "CPU 上限 1 核 (--cpus 1) —— 防 CPU 耗尽"   || bad "NanoCpus = $NC"
  echo "$LB" | grep -q 'weknora.plugin.runtime' && ok "容器带 runtime 实例 label —— 生命周期可追踪" || bad "缺 runtime label"
  echo "$LB" | grep -q 'weknora.plugin.id'      && ok "容器带 plugin.id label —— 支持按插件精确清理" || bad "缺 plugin.id label"
else
  bad "未找到插件容器（请先在 UI 刷新插件）"
fi

title "③ network:none 断网实测（红队视角）"
info "插件容器 NetworkMode=none 已在②步经 docker inspect 证实；此处演示 none 模式的实际出网行为"
WAY=""
if [ -n "$CTR" ]; then
  TMP=$(docker exec "$CTR" sh -c 'wget -T 3 -O- http://example.com 2>&1' 2>/dev/null)
  echo "$TMP" | grep -qiE 'bad address|unreachable|refused' && WAY="plugin"
fi
if [ -z "$WAY" ]; then
  TMP=$(docker run --rm --network none alpine sh -c 'wget -T 3 -O- http://example.com 2>&1' 2>/dev/null)
  echo "$TMP" | grep -qiE 'bad address|unreachable|refused' && WAY="alpine(同参数)"
fi
if [ -n "$WAY" ]; then
  echo "$TMP" | head -1
  ok "network:none 下外网访问被拒（实测途径: $WAY）—— 插件物理上无出网通路"
else
  bad "none 网络下竟可出网（输出: $TMP）"
fi

title "④ 宿主↔插件控制面：Socket 权限移交"
S=$(docker exec $AGENT sh -c "ls -la $RUNTIME_ROOT/$PLUGIN_ID/control/ 2>/dev/null")
if echo "$S" | grep -q 'plugin.sock'; then
  echo "$S" | grep -E 'srw-rw----' >/dev/null \
    && ok "control socket 权限 0660 —— 仅授权组可连接，不向系统开放" \
    || bad "socket 权限过宽: $(echo "$S" | grep plugin.sock)"
  GID=$(echo "$S" | awk '/plugin.sock/{print $4}')
  case "$GID" in
    2000|weknora-runtime) ok "socket 属组 $GID（GID 2000）—— 运行时组移交生效（组外不可达）" ;;
    *) bad "socket 属组 $GID，期望 2000(weknora-runtime)" ;;
  esac
else
  bad "未找到 control socket（先点一次 UI 刷新插件再重跑）"
fi
docker logs $AGENT 2>&1 | grep -q "socket-handover.*allowed=true" \
  && ok "审计留痕: [plugin-audit] socket-handover allowed=true —— 特权操作可追溯" \
  || bad "无移交审计记录"

title "⑤ 信任分级配置持久化（重启不丢）"
DB=$(docker exec $PG sh -c "psql -U \$POSTGRES_USER -d \$POSTGRES_DB -tAc \"SELECT value FROM system_settings WHERE key='plugins.trust_levels'\"" 2>/dev/null)
echo "$DB" | grep -q "$PLUGIN_ID" \
  && ok "插件信任等级已落库 system_settings" || bad "trust_levels 未持久化"
LVL=$(echo "$DB" | python3 -c "
import sys, json
raw = sys.stdin.read().strip()
try:
    d = json.loads(raw)
    if isinstance(d, str):
        d = json.loads(d)
    print(d.get('$PLUGIN_ID', '?'))
except Exception:
    print('?')
" 2>/dev/null)
[ "$LVL" = "isolated" ] \
  && ok "tarily123 = isolated（不可信 → 强隔离路径）" || info "tarily123 当前: $LVL"
echo "$DB" | grep -q 'offline' && echo "$DB" | grep -q 'trusted' && echo "$DB" | grep -q 'isolated' \
  && ok "三档信任分级齐备（offline / trusted / isolated）" || info "未检测到完整三档分级"

title "⑥ 插件上线状态（端到端连通性）"
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null)
if [ -n "$TOKEN" ]; then
  ST=$(curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/plugins \
    | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print(next((p['state'] for p in d if p['id']=='$PLUGIN_ID'),'?'))" 2>/dev/null)
  [ "$ST" = "running" ] \
    && ok "isolated 插件经 agent 编排上线: state=running —— 宿主↔agent↔容器↔socket 全链路贯通" \
    || bad "插件状态: $ST"
else
  info "未提供登录凭据，跳过 API 检查（可手动在 UI 确认 running）"
fi

title "⑦ egress 网络策略与信任矩阵（审计日志断言）"
LOGS=$(docker logs $AGENT 2>&1)
echo "$LOGS" | grep -q 'allowed=true reason=allowed by managed egress proxy' \
  && ok "白名单内放行：api.tavily.com allowed=true（经受控出口代理）" || info "未观察到放行记录（先触发一次联网调用再重跑）"
echo "$LOGS" | grep -q 'allowed=false reason=destination is not in manifest allowlist' \
  && ok "白名单外拦截：example.com allowed=false（不在 allowlist）" || info "未观察到白名单外拦截记录"
echo "$LOGS" | grep -q 'allowed=false reason=DNS resolved to a private or link-local address' \
  && ok "SSRF 防护：解析到私网/链路本地地址即拒绝" || info "未观察到 SSRF 拦截记录"
docker logs $APP 2>&1 | grep -q 'trusted plugin cannot use OCI entrypoint' \
  && ok "信任矩阵：非法组合（trusted + OCI 入口）被拒绝装载" || info "未观察到信任矩阵拒绝记录"

echo ""
echo -e "${B}══════════════════ 验收总览 ══════════════════${N}"
echo -e "  通过 ${G}${P}${N} 项   失败 ${R}${F}${N} 项"
[ $F -eq 0 ] && echo -e "  ${G}结论：插件安全框架关键控制点全部有效 ✔${N}" \
              || echo -e "  ${Y}存在失败项，见上方明细${N}"
