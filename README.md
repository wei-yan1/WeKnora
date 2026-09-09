# WeKnora 插件框架（课题一）

> 本文档聚焦 WeKnora 的**统一插件控制面 + 五大扩展点插件化**。它不是项目总览（README），而是"插件框架"这一课题的交付说明：架构、插件从落盘到运行的完整生命周期、治理能力、五个扩展点的制作文档入口。

---

## 一、整体架构

**通俗地说**：WeKnora 的五类扩展能力（数据源、文档解析、网络搜索、模型、检索引擎）原本硬编码在主仓里——想加一个新数据源，必须改主仓代码、提 PR、等合入。插件框架把这件事倒过来：**宿主只认一份 `plugin.yaml` 清单和一个 gRPC 协议**，任何独立仓库编译出的插件，放进指定目录就能被发现、启动、监管、调用——主仓零改动。

### 分层架构

```text
┌──────────────────────── 宿主控制面（internal/plugin）────────────────────────┐
│                                                                             │
│  Discovery          discovery.go     扫描插件目录，递归发现 plugin.yaml       │
│  Manager            runtime.go       生命周期注册表：状态机 + 代数(generation) │
│      │                               + admission 并发租约 + 健康监督循环      │
│  ExecutionPlan      runtime_plan.go  信任级别 × entrypoint → 隔离方式 + 网络  │
│  ExtensionAdapter   adapter.go       五类扩展点各自的注册通道（datasource /   │
│                                      parser / search / model / retriever）  │
│                                                                             │
│  Runtime（统一接口：Start / Stop / Health，runtime.go:19）                    │
│    ├─ ProcessRuntime  process_runtime.go  进程态：子进程 + loopback gRPC     │
│    │                                    （开发 / 桌面，协作式安全）           │
│    └─ DockerRuntime   docker_runtime.go   容器态：--network none OCI 沙箱    │
│                                         （生产，硬隔离）                     │
│            └─ 生产形态：plugin-runtime-agent 是唯一持有 Docker 权限的组件      │
│               （cmd/plugin-runtime-agent + agent_server.go），app 容器        │
│                不挂 docker.sock、不装 docker CLI                              │
└─────────────────────────────────────────────────────────────────────────────┘
                    │ 五类适配器注册（adapter.Register）
                    ▼
┌────────────── 五类业务注册表（宿主既有代码路径，零改动）──────────────────────┐
│  ConnectorRegistry(datasource)   ParserRegistry   WebSearch.Registry       │
│  ModelProviderRegistry           RetrieveEngineRegistry                    │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 插件如何被识别：一个文件夹即一个插件

**通俗地说**：插件的物理形态就是一个**文件夹**——里面一个 `plugin.yaml`（声明"我是哪类插件"）加一个编译好的二进制，可选再带个图标。宿主要"识别"一个新插件，不需要改任何代码，只要把它放进对应扩展点的目录。

宿主识别插件的三条规则：

1. **按扩展类型分文件夹**：五类扩展点各对应一个目录（完整目录结构见第三节）：

```text
plugins/
├── datasource/   ← WEKNORA_PLUGIN_DIR_DATASOURCE
├── parser/       ← WEKNORA_PLUGIN_DIR_PARSER
├── search/       ← WEKNORA_PLUGIN_DIR_SEARCH
├── model/        ← WEKNORA_PLUGIN_DIR_MODEL
└── retriever/    ← WEKNORA_PLUGIN_DIR_RETRIEVER
```

2. **识别依据是文件夹里的 `plugin.yaml`**：宿主启动时递归扫描这些目录，找到 `plugin.yaml`（`plugin.yml` / `plugin.json` 亦可），读其中的 `extension_type` 字段判断它是哪类插件，再按该类协议注册、启动（详细过程见第二节"发现"阶段）。

3. **目录可多个、路径可覆盖**：每个 `WEKNORA_PLUGIN_DIR_*` 支持用路径分隔符（Windows `;` / Linux `:`）或逗号分隔多个目录；容器部署时通过 `docker-compose.yml` 挂载，路径可自定义，未设置的变量直接跳过。

> 一句话：**新增插件 = 新建一个文件夹 + 放进去 `plugin.yaml` 和二进制**，剩下的发现、校验、装载、启动、监管全由宿主完成。

### 六个关键设计决策

1. **一份 Manifest 契约**（`internal/plugin/types.go` `Manifest`）：插件的身份、版本、扩展类型、协议版本、`config_schema`、权限（`permissions.network` / `read_paths` / `data`）、`capabilities` 全部声明在 `plugin.yaml` 里，装载时统一校验（`manifest.Validate`：语义版本、ID 格式、网络策略、schema 分区、`secret` 只允许出现在 credentials 等）。

2. **信任三级 × 双运行时**（`runtime_plan.go` `ResolveExecutionPlan`）：部署管理员为每个插件选择 `offline`（默认断网）/ `trusted`（可联网的进程插件）/ `isolated`（必须 `docker://` 的容器插件）。信任级别**只能收窄** manifest 声明的权限，不能扩大——`trusted` 插件声明了 OCI 入口会被直接拒绝，`isolated` 插件没有容器入口同样拒绝。

3. **Docker 权限分离**（`runtime_wiring.go` `newOCIRuntime`）：容器化部署时，app 容器**不接触 docker.sock**。所有 OCI 插件的启动/停止请求通过共享卷上的 Unix socket 发给独立的 `plugin-runtime-agent`，由它执行 `docker run` 并再次校验请求（插件 ID 格式、扩展类型、协议版本、网络策略、镜像 digest 白名单——`agent_server.go` + `image_policy.go`）。开发/单进程形态下，同一套逻辑退化为进程内 docker CLI。

4. **内置与外部并存**（`builtin.go` + 各 `*_registration.go`）：飞书、语雀、Notion、GitLab 等内置连接器原样保留"内置单例"通道，插件框架只给它们登记生命周期控制面（`BuiltinRuntime` 占位）；外部插件走"factory"通道。调用时 `GetForScope` 先查外部 factory、再回退内置单例——**两套实现互不覆盖**。

5. **每次调用解析当前连接**（`runtime.go` `AcquireInvocation` + `datasource_proxy.go`）：宿主不缓存插件的 gRPC client。每次业务调用先通过 admission 申请一个调用租约（并发上限 + 排队 + drain 检查），固定当前 runtime 的 **generation（代数）**，再取"此刻"的连接构造代理。插件重启后 generation 递增，旧租约自动失效——**调用方无需感知插件的重启**。

6. **单插件失败隔离**（`loader.go` `LoadExternalWithRegistries`）：一个损坏的外部插件只记录失败并跳过（`RegisterFailed` 保留占位，让插件管理页仍能看到并可修复后重试），**不回滚其他插件、不拖垮宿主启动**——与热重扫的隔离语义一致。

### 插件作者靠什么做插件：`pkg/pluginapi` SDK + 五份制作文档

**通俗地说**：插件作者不需要理解上面任何一张架构图或任何一段宿主代码。他只需要做三件事——（1）照着对应扩展点的文档写一个 `plugin.yaml`；（2）用宿主提供的 SDK（`github.com/Tencent/WeKnora/pkg/pluginapi`）填几个回调函数；（3）编译成一个独立二进制。宿主负责发现、启动、监管、联网管控和生命周期，作者只负责"这个数据源怎么拉数据 / 这个模型怎么调用"。

三件套的对应关系：

| 想做的插件 | `extension_type` | 照着写的文档 | SDK 入口 |
|---|---|---|---|
| 数据源（同步外部文档） | `datasource` | [plugin-development-datasource.md](docs/plugin-development-datasource.md) | `pluginapi.DataSourceHandler` |
| 文档解析（字节 → 文本） | `parser` | [plugin-development-parser.md](docs/plugin-development-parser.md) | `pluginapi.ParserHandler` |
| 网络搜索（查询 → 结果） | `search` | [plugin-development-websearch.md](docs/plugin-development-websearch.md) | `pluginapi.WebSearchHandler` |
| 模型提供方（对话/向量/重排…） | `model` | [plugin-development-model.md](docs/plugin-development-model.md) | `pluginapi.ModelHandler` |
| 检索引擎（向量/关键词存取） | `retriever` | [plugin-development-retriever.md](docs/plugin-development-retriever.md) | `pluginapi.RetrieverProvider` |

这五份文档每一份都是**独立、完整、可盲测**的——它们自带五类扩展点共用的骨架速览（`plugin.yaml` 字段、`PluginControl` 握手/健康、`WEKNORA_PLUGIN_ADDR`、目录环境变量、权限声明），以及每类扩展点特有的协议字段表和 SDK Handler 示例。文档末尾还各有一个 **conformance 自检入口**（如 `RunDataSourceConformance`），作者本地就能做协议级冒烟验证，不需要起一个完整宿主。

> 验收闭环：课题验收要求"他人仅依据文档即可独立实现一个可运行的最简插件"。第四节列出的 LocalDir / GitHub / DingTalk / TARily / DS 五个真实插件，正是由不同扩展点的文档 + SDK 独立产出的——它们的存在本身就证明了文档的可复现性。

---

## 二、插件生命周期：从磁盘上的文件夹到运行中的服务

**通俗地说**：以 GitHub 数据源插件为例——你把编译好的 `weknora-plugin-github` 二进制和 `plugin.yaml` 放进插件目录，宿主下次启动（或你在设置页点一次"刷新插件"）时，它会被发现、校验、注册，然后宿主把它作为子进程（或容器）拉起来，通过 gRPC 握手确认身份和能力，之后知识库每一次同步 GitHub 仓库，真正干活的都是那个独立进程。下面按真实代码一步步走。

### 阶段 0：宿主接线

`internal/container/container.go` 在启动时做两件事：

- 读取系统设置里持久化的 `plugins.trust_levels`，注入 Manager（`ConfigureManagerTrust`；配置非法则 fail-closed，所有外部插件以 offline 启动），随后调用 `LoadExternalFromEnvWithRegistries`；
- 在内置 + 外部插件都注册并启动完成后，启动健康监督循环（`startPluginHealthSupervisor` → `manager.StartHealthSupervisor`，`RestartEnabled: true, MaxRestartCount: 3`，间隔与失败阈值取默认值 30s / 3 次）。

### 阶段 1：发现——找到磁盘上的插件

入口：`pluginRootsFromEnv`（`loader.go`）从五个环境变量 `WEKNORA_PLUGIN_DIR_{DATASOURCE,PARSER,SEARCH,MODEL,RETRIEVER}` 收集插件根目录（支持路径分隔符/逗号分隔多个目录）；未设置的变量跳过。

随后 `DiscoverPackages`（`discovery.go`）对每个根目录做 `filepath.WalkDir` 递归扫描，收集所有 `plugin.yaml` / `plugin.yml` / `plugin.json`：

- 每个 manifest 经 `LoadManifest` 解码并跑一遍 `Validate`（结构合法性在此把关）；
- **重复 ID 直接报错**（两个目录出现同一个 `id`，宿主拒绝启动而不是猜一个）；
- 记录 `SourceDir`（插件所在目录），后续用于解析插件自带的图标等资源。

### 阶段 2：解析执行计划——决定"怎么跑、能不能联网"

`loadOnePackage`（`loader.go`）对每个包调用 `ResolveExecutionPlan(manifest, trustLevel)`（`runtime_plan.go`）：

| 信任级别 | 隔离方式 | 网络 | 约束 |
|---|---|---|---|
| `offline`（默认） | 进程或容器均可 | `none` | 未配置信任的插件一律视为 offline，fail-closed |
| `trusted` | 仅进程（声明 OCI 直接拒绝） | 允许，但受 manifest 白名单约束 | 管理员显式信任的进程插件 |
| `isolated` | 仅 `docker://` OCI 容器 | 允许，受 manifest 白名单约束 | 必须走容器硬隔离 |

解析出的计划只作用于**运行时副本**（`runtimeManifest`），不修改磁盘上的 manifest——manifest 始终是插件作者声明的上限。

### 阶段 3：构造 Runtime 并注册到扩展点

按计划构造 Runtime（`loader.go:89-98`）：

- **OCI** → `newOCIRuntime`：配置了 `WEKNORA_PLUGIN_RUNTIME_AGENT_SOCKET` 时走 `RemoteRuntimeAgentController`（生产，请求经 Unix socket 发给 agent）；否则进程内 docker CLI（开发/单进程）。
- **进程** → `NewProcessRuntime(manifest, entrypoint)`。

随后按 `extension_type` 找到对应的 `ExtensionAdapter`（`adapter.go` 的 `ExtensionAdapterRegistry`），执行 `adapter.Register(manager, manifest, runtime)`。以数据源为例（`registration.go` `registerExternalDataSource`）：

1. `manager.Register`：再次 `manifest.Validate(hostVersion)` + `CapabilityReporter` 能力核对，然后登记 `pluginEntry`（状态机、generation、admission、lifecycle 锁）；
2. `registry.RegisterFactory`：把"按调用租约构造连接器实例"的工厂注册进 `ConnectorRegistry`——宿主既有的数据源调用路径**不需要任何改动**就能路由到外部插件；
3. 注册连接器元数据（名称/描述/图标/config_schema 透传前端）与插件目录内的图标文件。

注册失败会回滚该插件的全部局部状态（`rollbackLoad`：adapter.Unregister + manager.Unregister），保证重试从干净状态开始。其余四类扩展点（parser / search / model / retriever）各自有对应的 registration 文件，套路一致：**manager 登记生命周期 + 各自业务注册表登记调用入口**。

### 阶段 4：启动与握手——确认"你是谁、你会什么"

`manager.Start(ctx, id)`（`runtime.go`）在 per-plugin 生命周期锁内执行：

- 幂等：已启动直接返回；上一实例还在 draining、或还有旧调用未释放时拒绝启动；
- 状态置为 `starting`，调用 `runtime.Start(ctx)`。

**进程态**（`process_runtime.go`）：探测 `127.0.0.1` 空闲端口（最多 3 次重试 + 指数退避）→ `exec.Command` 启动插件子进程，注入 `WEKNORA_PLUGIN_ADDR`（监听地址）、`WEKNORA_PLUGIN_ID`、`WEKNORA_PLUGIN_PROTOCOL_VERSION`、`WEKNORA_PLUGIN_NETWORK_POLICY`、`WEKNORA_PLUGIN_NETWORK_ALLOWLIST` 五个环境变量 → stderr 管道接入审计（`ConsumePluginStderr`）→ gRPC 连接（10s 超时、50MB 消息上限，与 docreader 对齐）。

**容器态**（`docker_runtime.go` + `local_docker_controller.go`）：启动请求发给 runtime-agent（或进程内 docker CLI）。agent 校验通过后执行：

```text
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --pids-limit 128 --memory 512m --cpus 1 \
  -v <control-dir>:/run/weknora -v <egress-dir>:/run/weknora-egress:ro ...
```

插件容器没有网卡——联网只能经挂载进去的 egress 代理 socket（`egress_proxy.go`：域名+通配白名单、SSRF 拦截、逐次审计）。控制器等待插件创建 control socket 并完成属主交接（chown 到共享运行组 GID 2000 + `0660`），宿主才通过该 socket 建立 gRPC 连接。

**握手**：两条路径殊途同归——`PluginControl.Handshake` 拿到插件回报的 ID / 协议版本 / extension_type / capabilities，`validateHandshake`（`runtime.go`）做**双向严格校验**：runtime 回显了 manifest 未声明的能力、或 manifest 声明了 runtime 未回显的能力，都判装载失败。这保证"manifest 声明"与"运行时真实能力"永远一致。

全部通过后：`entry.started = true`，**generation 递增**（此后旧的调用租约自动失效），admission 恢复放行；模型插件还会异步触发一次配置重推（`fireModelConfigRepush`，把宿主 DB 里保存的模型配置重新投递给重启后的插件进程）。

### 阶段 5：健康监督与自愈

`healthSupervisorLoop`（`runtime.go`）每 30s 遍历所有已启动插件，逐个 `superviseOne`：

1. `runtime.Health(ctx)`（2s 超时）探测插件的控制面 Health；
2. `running` → 清零失败计数与重启计数；
3. 非 running → `healthFailures++`，连续 **3 次**（阈值）失败 → 标记 `unhealthy`；
4. 达到阈值且重启次数 < **3 次**（`MaxRestartCount`）→ 触发自动重启，每次降级/重启决策都写入监督审计（`recordSupervision`）；
5. `restartOne`：重新拿生命周期锁 + **校验 generation**（防止与手动操作并发重启同一个插件）→ drain 在途调用（超时强制取消）→ `runtime.Stop`（15s 宽限）→ 确认旧调用全部终结 → 重新 `Start`。

重启成功后 generation 再次递增——所有在途/后续调用都会拿到新连接。达到重启上限仍不健康的插件停留在 unhealthy 并被持续审计，不会再无限重启消耗资源。

### 阶段 6：业务调用——一次增量同步的完整路径

以"知识库同步 GitHub 仓库"为例：

```text
同步触发（手动/定时）
  → 创建 SyncLog + 落库（datasource_service.go，Outbox 模式：先持久化再投递）
  → dispatcher.Dispatch（sync_outbox.go）经 asynq 入队，TaskID = dssync:<dsID>:<logID>
  → Worker 取任务 → ProcessSync → ConnectorRegistry.GetForScope
  → 外部 factory（registration.go）：
      ① manager.AcquireInvocation —— admission 限流 + 固定 generation
      ② provider.Conn() 取"当前"gRPC 连接
      ③ 构造 GRPCConnectorProxy（unary）或 GRPCStreamingConnectorProxy（声明了
         streaming capability 的插件），与宿主侧 StreamingConnector 判定严格对应
  → 插件进程内 OnFetchIncremental：读 cursor → GitHub compare API → 增量 + 新 cursor
  → 宿主 Emit 入库 + Checkpoint 持久化 cursor（下次原样传回插件）
  → lease.Close() 释放租约
```

cursor 的契约由宿主与插件共同维护：**插件理解 cursor 的语义（GitHub 是 commit SHA，钉钉是节点时间戳），宿主只负责保存与原样传回，从不解释内容**。删除通过 `IsDeleted: true` 表达。插件进程重启不影响 cursor（它存在宿主 DB 的数据源记录里）。

### 阶段 7：热重扫——不重启宿主地增删改插件

设置页"刷新插件"按钮触发 `POST /api/v1/plugins/rescan` → `RescanExternalWithRegistries`（`loader.go`）：

- 重新 `DiscoverPackages`，与已加载清单对比；
- manifest 未变 + 信任级别未变 + 插件健康 → **Skipped**（幂等）；
- manifest 变更、信任级别变更、或插件此前 failed/unhealthy → **Changed**：用**旧 manifest** 推导旧 handle 卸载旧实例，再按新 manifest 重新装载——所以 rescan 同时是一次"自愈"通道（外部误删的插件容器可以在下一次刷新时被拉起）；
- 新出现的插件 → **Added**；
- 单个插件失败只记入 `Errors`，不影响其他插件。

信任级别的修改也遵循同一语义：`SetTrust` 只更新目标级别，插件继续以装载时的级别运行，直到下一次 rescan/重启才生效（前端以"待生效"圆点提示）。

---

## 三、插件部署目录结构

插件部署时，先建立一个总目录（例如 `D:\weknora-plugins`），再按扩展类型细分五个子目录，每个子目录对应一个环境变量：

```text
D:\weknora-plugins\          ← 总目录（名称任意）
├── datasource\              ← WEKNORA_PLUGIN_DIR_DATASOURCE
├── parser\                  ← WEKNORA_PLUGIN_DIR_PARSER
├── search\                  ← WEKNORA_PLUGIN_DIR_SEARCH
├── model\                   ← WEKNORA_PLUGIN_DIR_MODEL
└── retriever\               ← WEKNORA_PLUGIN_DIR_RETRIEVER
```

宿主启动时按这五个环境变量扫描对应目录，递归发现每个插件包里的 `plugin.yaml`（`plugin.yml` / `plugin.json` 亦可）。未设置的环境变量会被忽略；已设置但路径不存在会报错而非静默跳过。每个子目录下可放置多个独立插件包。

---

## 四、真实可用的插件示例（独立插件仓库）

插件示例与模板**不放在主仓源码中**，以独立插件仓库形式维护（本机 `D:\weknora-plugins`，后续将发布为独立 GitHub 仓库）。开发者在不修改主仓任何代码的前提下，复制对应目录、改 `plugin.yaml` 的 `id` 和 `config_schema`，就能独立构建出一个新插件。

| 插件 | 扩展点 | 用途 | 仓库内路径 |
|---|---|---|---|
| **LocalDir** | `datasource` | 从本地目录扫描文件接入知识库；用于论文、笔记、文档等本地内容 | `datasource/weknora-plugin-localdir/` |
| **GitHub** | `datasource` | 同步 GitHub 仓库的 README / Markdown 文件到知识库 | `datasource/weknora-plugin-github/` |
| **DingTalk** | `datasource` | 同步钉钉文档到知识库 | `datasource/weknora-plugin-dingtalk/` |
| **TARily** | `search` | Tavily Search API 的别名版搜索插件 | `search/weknora-plugin-tarily/` |
| **DS (DeepSeek)** | `model` | DeepSeek 聊天模型提供方（OpenAI 兼容） | `model/weknora-plugin-DS/` |

它们都验证了同一件事：**一个独立于 WeKnora 主仓的外部插件，能通过 Manifest 描述身份 / 能力 / 版本 / 配置 / 权限，由宿主发现、启动、管理，并通过进程外 gRPC 接入现有流程，且主仓零改动**。

---

## 五、插件框架治理能力（生产级）

| 能力 | 实现位置 | 作用 |
|---|---|---|
| **进程级限流（Admission）** | `internal/plugin/admission.go` | 每个插件有独立的最大并发、有限等待队列、排队超时；队列满 / 超时 / 插件 draining 返回结构化错误 |
| **分布式锁（同步任务互斥）** | `internal/datasource/sync_lock.go`（Redis token-owned 锁）+ `internal/datasource/scheduler.go` | trigger-lock 串行化创建 SyncLog + Asynq 入队；sync-lock Worker 续租；所有权丢失会取消同步 |
| **进程外 gRPC 通信** | `pkg/pluginapi/proto/plugin.proto` + 各扩展点 proxy | 进程外 RPC 调用，每个 RPC 携带 invocation context、admission lease、generation token |
| **进程隔离 / 沙箱** | `internal/plugin/process_runtime.go`（开发态）+ `internal/plugin/docker_runtime.go` + `local_docker_controller.go`（生产态） | DockerRuntime 使用 `--network none` 实现硬隔离；ProcessRuntime 不提供安全隔离，仅用于开发 |
| **网络策略 + 受控出站** | `internal/plugin/manifest.go`（声明）+ `internal/plugin/egress_proxy.go`（容器网络层强制）+ `pkg/pluginapi/guarded_client.go`（SDK 防护） | 插件声明 `network: none` / `allowlist`；isolated 插件经 runtime-agent 的 per-plugin 出口代理强制白名单（域名+通配、SSRF 拦截、审计）；`url` 类型字段自动走 SSRF 校验 |
| **插件生命周期** | `internal/plugin/runtime.go` | Handshake 双向能力校验 + Health 周期监督（30s / 阈值 3 次），失败自动 Restart（上限 3 次）且全程审计；Stop 先 drain 拒绝新 lease，超时强制取消 |
| **配置校验与脱敏** | `internal/datasource/httpclient.go`（SSRF）+ `internal/types/datasource.go`（AES-256-GCM 加密凭证）+ 前端 secret 锁头输入框 | 凭证字段加密存储，前端不回显明文，敏感字段在 UI 上以锁头展示 |
| **审计与追踪** | `internal/plugin/security.go` + `supervision_audit_test.go` + `audit_integration_test.go` | 插件装载、启动、停止、调用、网络放行/拦截、监督降级与重启等事件全部产生审计日志 |

### 数据源同步的双层分布式锁（生产化）

数据源同步是典型的"一次触发、要防并发"的场景，因此没有简单用单把锁，而是拆成两层边界（`internal/datasource/sync_lock.go`）：

| 锁 | 租约 / 续租 | 串行化边界 | 目的 |
|---|---|---|---|
| `trigger-lock`（触发锁） | 15s / 5s | 检查 pending/running → 创建 SyncLog → Asynq 入队 | 防止同一数据源被重复触发、重复入队 |
| `execution-lock`（执行锁） | 45s / 15s | connector 实际拉取执行 | 防止同一数据源并发拉取 |

生产级语义（不是简单 `SETNX`）：

- **token-owned**：每次获取生成随机 token，续租与释放都校验 token，只有持有者能释放；锁 key 带 `{dataSourceID}` hash tag，天然支持 Redis Cluster；
- **续租失败即取消**：后台 `renewLoop` 每 15s 续租，一旦续租失败立即 `cancel` 同步上下文，让 Worker 感知"锁已丢失"并停止，避免两实例同时写；
- **fail-closed**：Redis 故障时 `TryAcquire` 返回错误而非误判成功——宁可这次不同步，也不让两个实例并发执行；
- **本地同语义 fallback**：无 Redis（Lite 单进程）时退化为进程内 `map + mutex` 锁，接口与 Redis 版一致，不改变调用方语义。

配合 `internal/datasource/sync_outbox.go` 的 Outbox 模式：同步意图先落库、再投递队列，投递失败保留 `pending` 并指数退避重试——保证"同步一次、至少一次、可追踪"。

---

## 六、五个扩展点插件制作文档

每个扩展点都有独立、完整的"独立开发"指南，开发者只依据文档就能盲测实现一个新插件，无需阅读主仓源码。

| 扩展点 | `extension_type` | 文档 | SDK Handler |
|---|---|---|---|
| 数据源（DataSource） | `datasource` | [`docs/plugin-development-datasource.md`](docs/plugin-development-datasource.md) | `pluginapi.DataSourceHandler` |
| 文档解析（Parser） | `parser` | [`docs/plugin-development-parser.md`](docs/plugin-development-parser.md) | `pluginapi.ParserHandler` |
| 网络搜索（WebSearch） | `search` | [`docs/plugin-development-websearch.md`](docs/plugin-development-websearch.md) | `pluginapi.WebSearchHandler` |
| 模型管理（Model） | `model` | [`docs/plugin-development-model.md`](docs/plugin-development-model.md) | `pluginapi.ModelHandler` |
| 检索引擎（Retriever） | `retriever` | [`docs/plugin-development-retriever.md`](docs/plugin-development-retriever.md) | `pluginapi.RetrieverProvider` / `pluginapi.RetrieverBackend` |

每个扩展点文档的第 2 节都自带了五类扩展点共用的骨架速览（`extension_type`、`PluginControl` 握手/健康、`WEKNORA_PLUGIN_ADDR`、目录环境变量与权限），可直接从任一扩展点文档入门。

---

## 七、插件示例不在主仓源码中

主仓不携带任何插件模板或示例插件。全部插件（含后续新增的模板）以独立插件仓库形式维护，按 `datasource/ parser/ search/ model/ retriever/` 五个扩展点子目录组织，每个插件包含完整的 `plugin.yaml` + Go 入口 + 独立 `go.mod`（用 `replace` 指向本地 WeKnora 源码以便联调；独立发布时删除该 replace）。

---

## 八、当前实现的边界（仍待后续课题处理）

- 五类扩展点中，**数据源已有端到端验证**（飞书 / 语雀 / Notion / RSS / IMA / GitLab / LocalDir / GitHub / 钉钉）；其余四类的协议和适配层已就位，UI 接入完整度不同；
- 检索引擎已落地最小协议（`Describe`/`OpenStore`/`CloseStore`/`BatchPut`/`Search`/`Delete`/`Patch` + `store_handle` 会话），自愈式 `GRPCRetrieverRepository` 已接入内部 `RetrieveEngineRepository`，`copy_indices` 等高级能力暂为可选 capability；
- 模型管理的前端"plugin source"选项、检索引擎的前端 schema/credential 配置界面、解析的流式化、WebSearch 的统一 settings/credentials，仍是后续课题需要补齐的 P1 项；
- Docker 沙箱是硬隔离的唯一边界；ProcessRuntime 仅供开发，`trusted` 进程插件的联网约束依赖 SDK 层 `GuardedHTTPClient` 的协作式软约束；
- 非 root 运行的插件容器访问 egress socket 的兼容性、以及插件容器 UID/GID 的 manifest 声明，仍属后续生产部署验证项。

---

## 九、运行效果截图

以下截图来自外部数据源插件的实际运行，展示了「插件类型动态出现在创建列表 → 配置 → 同步 → 文档进入知识库」的完整链路：

![数据源类型选择：外部插件类型自动出现在创建列表](docs/images/datasource-types-selection.png)

![Local Directory 插件：增量同步成功](docs/images/datasource-localdir-sync.png)

![GitHub 插件：同步成功（22 个文档，6 个失败提示）](docs/images/datasource-github-sync.png)

![DingTalk 插件：增量同步成功](docs/images/datasource-dingtalk-sync.png)

![GitHub 同步进入知识库的文档列表](docs/images/knowledge-github-docs.png)
