# 测试与验收说明

本文说明插件框架的测试分层、运行命令与证据位置，便于复核与接手。

## 总览：四层测试

| 层级 | 覆盖内容 | 运行方式 | 证据 |
|---|---|---|---|
| ① 单元 / 契约 | 插件框架内部逻辑、SDK 契约、迁移与配置解析 | `go test` | CI 日志、本地输出 |
| ② 端到端（进程） | 独立仓库插件经 ProcessRuntime 真实装载与同步 | `go test`（需指定插件路径） | `internal/plugin/e2e_test.go` |
| ③ 安全验收 | 权限分离 / 强隔离 / 断网 / Socket 移交 / 信任持久化 / 连通 / egress | 验收脚本（Docker 部署） | 脚本输出 + 审计日志 |
| ④ 实机演示 | 五类扩展点的真实操作链路 | 人工操作录像 | 运行截图 + 演示视频 |

### 验收标准 → 测试与证据对照

| 验收标准 | 对应测试 | 证据 |
|---|---|---|
| ① 独立仓库插件免改主仓即可装载并完成同步 | ② 端到端（`TestExternalLocalDirectoryProcessRuntime`，二进制来自主仓之外） | 测试输出 + 运行截图/视频 |
| ② 不联网声明运行期被强制、可审计 | ③ 安全验收（② 强隔离 / ③ 断网 / ⑦ egress 审计） | 脚本输出 + `[plugin-audit]` 原始日志 |
| ③ 增量同步只处理变更文件 | ① 单元（`sync_outbox` / `scheduler` / `sync_lock`）+ ④ 实机演示 | 测试输出 + 截图/视频 |
| ④ 他人仅依据文档即可实现最简插件 | ① 契约（`pkg/pluginapi` conformance 自检）+ ⑤ 五份制作文档 | `RunXxxConformance` 输出 + 插件仓 |

## ① 单元与契约测试

```bash
make test                 # 全量：go test -v ./...
go test ./internal/plugin/... ./internal/datasource/... ./pkg/pluginapi/...
```

重点包与关注点：

| 包 | 测试文件数 | 关注点 |
|---|---|---|
| `internal/plugin` | 19 | manifest 校验、执行计划、admission、generation fencing、agent 鉴权、镜像白名单、egress 代理、审计、健康监督 |
| `internal/datasource` | 5 | 连接器契约、SSRF 校验、调度、双层分布式锁、Outbox 投递 |
| `pkg/pluginapi` | 6 | SDK 握手/健康、guarded client、conformance 自检、配置存储 |

CI：`.github/workflows/app.yml` 对全仓执行 `go vet` + `go test`（排除 `docreader`），上述单测在 CI 上为必过项。

## ② 端到端测试（独立仓库插件 + 进程运行时）

`internal/plugin/e2e_test.go` 直接用**主仓之外**构建出的插件二进制跑通「发现 → 装载 → 握手 → 同步」，是最贴近验收标准第 1 条的自动化证据：

```bash
# 指向独立插件仓库中的插件目录（推荐：连 manifest 一起用插件仓库自己的）
WEKNORA_LOCALDIR_PLUGIN_ROOT=/path/to/WeKnora-plugin/datasource/weknora-plugin-localdir \
  go test ./internal/plugin/ -run TestExternalLocalDirectoryProcessRuntime -v

# 或只提供已编译好的二进制
WEKNORA_LOCALDIR_PLUGIN_BIN=/path/to/weknora-plugin-localdir \
  go test ./internal/plugin/ -run TestExternalLocalDirectoryProcessRuntime -v
```

未设置上述变量时该用例会 `skip`，不影响全量测试。

## 五类扩展点：自动化与手工验证

| 扩展点 | 自动化测试 | 手工验证 |
|---|---|---|
| 数据源 | `datasource_proxy_test.go`、`internal/datasource/{sync_outbox,sync_lock,scheduler}_test.go`、端到端用例 | GitHub / 钉钉 / LocalDir 全量与增量同步（截图、视频） |
| 解析器 | `parser_proxy_test.go`、`parser_registration_test.go`、`pkg/pluginapi/parser_test.go` | BuiltinB 解析文档（截图） |
| 网络搜索 | `web_search_proxy_test.go`、`web_search_registration_test.go`、`web_search_config_fields_test.go`、`pkg/pluginapi/web_search_test.go` | Tavily 插件联网检索（审计日志） |
| 模型 | `model_registration_test.go`、`pkg/pluginapi/model_conformance_test.go` | DeepSeek(DS) 问答、深度思考（截图、视频） |
| 检索引擎 | 通用运行时用例（admission / generation / trust）+ `pkg/pluginapi/sdk_test.go` 契约；**暂无专项自动化用例** | Milvux 向量检索（截图） |

## ③ 安全验收（Docker 部署）

一键脚本：`scripts/plugin-security-acceptance.sh`，覆盖 6 组控制点 + egress 审计断言：

```bash
WEKNORA_ADMIN_EMAIL=you@example.com WEKNORA_ADMIN_PASSWORD='***' \
  bash scripts/plugin-security-acceptance.sh
```

容器名可用环境变量覆盖：`WEKNORA_PLUGIN_ID` / `WEKNORA_APP_CONTAINER` / `WEKNORA_RUNTIME_AGENT_CONTAINER` / `WEKNORA_POSTGRES_CONTAINER`。

脚本逐项检查：

| 组 | 检查手段 |
|---|---|
| ① 权限分离 | `docker inspect`：app 无 `docker.sock`、agent 独占 |
| ② 强隔离沙箱 | `docker inspect`：`network=none`、只读根、`cap-drop ALL`、`no-new-privileges`、资源上限、双 label |
| ③ 断网实测 | 容器内实际发起 `wget`，断言无出网通路 |
| ④ Socket 移交 | `ls -la` 看权限/属组 + agent 审计日志 `socket-handover` |
| ⑤ 信任持久化 | `psql` 查 `system_settings.plugins.trust_levels` |
| ⑥ 端到端连通 | 管理 API 查插件 `state=running` |
| ⑦ egress / 信任矩阵 | 审计日志断言：白名单内放行、白名单外拦截、SSRF 拦截、非法信任组合拒绝装载 |

产物：脚本尾部的「验收总览」（通过/失败计数）+ `docker logs` 中的 `[plugin-audit]` 原始记录。

## ④ 实机演示

- 运行截图见 README「十一、运行效果截图」；
- 演示视频与各段内容索引见 README「十二、实机测试演示视频」。

## 已知问题

| 问题 | 影响 | 处置 |
|---|---|---|
| Windows 下个别 SQLite 临时文件用例在**清理阶段**报文件占用（`TempDir RemoveAll ... being used by another process`） | 仅 Windows 本地复跑时可见，Linux/CI 正常 | 以 CI 结果为准；如需 Windows 全绿，可在用例里显式关闭 SQLite 句柄 |
| 插件化检索引擎（retriever）暂无专项自动化用例 | 该扩展点依赖通用运行时用例与实机验证 | 后续可补一个 `OpenStore/Search` 的代理级用例 |

## 如何判断通过

- 单测：命令退出码 0，末尾 `ok github.com/Tencent/WeKnora/...`；
- 端到端：用例 PASS（未设置插件路径时显示 `SKIP`，属预期）；
- 安全验收：脚本尾部输出「通过 N 项 失败 0 项」与「结论：插件安全框架关键控制点全部有效 ✔」。
