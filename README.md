# WeKnora 插件框架（课题一）

> 本文档聚焦 WeKnora 的**统一插件控制面 + 五大扩展点插件化**。它不是项目总览（README），而是"插件框架"这一课题的交付说明：架构、治理能力、内置/外部插件共存、五个扩展点的制作文档入口。

---

## 一、整体架构

WeKnora 的插件框架分为四层，自上而下：

```text
┌──────────────────── WeKnora Application ────────────────────┐
│  PluginManager                                              │
│    ├─ ManifestLoader          发现 / 校验 / 装载 plugin.yaml  │
│    ├─ CompatibilityValidator   weknora_version / 协议版本兼容  │
│    ├─ PluginRegistry           按 extension_type 注册工厂   │
│    ├─ LifecycleManager         启动 / 停止 / 守护 / 重启   │
│    ├─ HealthManager            周期性 Health + Admission    │
│    ├─ PermissionManager        network / read_paths / data  │
│    └─ AuditManager             装载 / 启停 / 调用审计       │
│                                                            │
│  Runtime                                                    │
│    ├─ BuiltinRuntime         内置扩展（飞书 / 语雀等）       │
│    ├─ ProcessRuntime         进程级 gRPC 插件（开发态）     │
│    └─ DockerRuntime          Docker/OCI 沙箱（生产态）      │
└────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┴─────────────────────┐
        ▼                                           ▼
┌─────────────── 内置扩展 Adapter ──────────┐   ┌──── 外部插件 gRPC Proxy ────┐
│  复用宿主现有连接器接口（如飞书/语雀/      │   │  gRPC proxy（带 invocation │
│  Notion/Ima/GitLab/RSS），走"内置单例"     │   │  context、admission、     │
│  通道，仅登记生命周期控制面               │   │  generation fencing）     │
│                                          │   │           ▼                │
│                                          │   │  Docker/OCI Runtime       │
└──────────────────────────────────────────┘   └────────────────────────────┘
                              │
                              ▼
┌──────────────── Type-specific Registry ────────────────┐
│   DataSource Registry   (内置 factory + 外部 plugin factory) │
│   Parser Registry                                         │
│   Search Registry                                         │
│   Model Registry                                          │
│   Retriever Registry                                      │
└────────────────────────────────────────────────────────┘
```

**核心思想**：宿主只负责稳定的扩展点、生命周期和最小协议；插件负责具体后端实现。内置与外部并存：内置走单例通道、外置走 factory 通道，**互不覆盖**。

---

## 二、插件部署目录结构

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

## 三、三个真实可用的数据源插件

在本仓库的 `plugins/` 目录下，已有三个**完整可运行**的数据源插件示例。开发者在不修改主仓任何代码的前提下，复制对应目录、改 `plugin.yaml` 的 `id` 和 `config`，就能独立构建出一个新插件。

| 插件 | 类型 | 用途 | 路径 |
|---|---|---|---|
| **LocalDir** | 进程型 gRPC 插件 | 从本地目录扫描文件接入知识库；用于论文、笔记、文档等本地内容 | `plugins/weknora-plugin-localdir/` |
| **GitHub** | 进程型 gRPC 插件 | 同步 GitHub 仓库的 README / Markdown 文件到知识库 | （外部安装，目录结构同 LocalDir） |
| **DingTalk** | 进程型 gRPC 插件 | 同步钉钉文档到知识库 | （外部安装，目录结构同 LocalDir） |

它们都验证了同一件事：**一个独立于 WeKnora 主仓的外部数据源插件，能通过 Manifest 描述身份 / 能力 / 版本 / 配置 / 权限，由宿主发现、启动、管理，并通过进程外 gRPC 接入现有数据源同步流程，最终把数据导入知识库**。

---

## 四、插件框架治理能力（生产级）

| 能力 | 实现位置 | 作用 |
|---|---|---|
| **进程级限流（Admission）** | `internal/plugin/admission.go` | 每个插件有独立的最大并发、有限等待队列、排队超时；队列满 / 超时 / 插件 draining 返回结构化错误 |
| **分布式锁（同步任务互斥）** | `internal/datasource/sync_lock.go`（Redis token-owned 锁）+ `internal/datasource/scheduler.go` | trigger-lock 串行化创建 SyncLog + Asynq 入队；sync-lock Worker 续租；所有权丢失会取消同步 |
| **进程外 gRPC 通信** | `pkg/pluginapi/proto/plugin.proto` + `internal/datasource/grpc_adapter` | 进程外 RPC 调用，每个 RPC 携带 invocation context、admission lease、generation token |
| **进程隔离 / 沙箱** | `internal/plugin/process_runtime.go`（开发态）+ `internal/plugin/docker_runtime.go`（生产态） | DockerRuntime 使用 `--network none` 实现硬隔离；ProcessRuntime 不提供安全隔离，仅用于开发 |
| **网络策略 + 受控出站** | `internal/plugin/manifest.go`（声明）+ `internal/plugin/security.go`（执行） + `pkg/pluginapi/guarded_client.go`（SDK 防护） | 插件声明 `network: none` / `egress` / `allowlist`；白名单按域名+通配匹配，宿主注入 SDK；`url` 类型字段自动走 SSRF 校验 |
| **插件生命周期** | `internal/plugin/runtime.go` | Handshake + Health 健康监督，failure 阈值后自动 Restart；Stop 先 drain 拒绝新 lease，超时强制取消 |
| **配置校验与脱敏** | `internal/datasource/httpclient.go`（SSRF）+ `internal/types/datasource.go`（AES-256-GCM 加密凭证）+ 前端 secret 锁头输入框 | 凭证字段加密存储，前端不回显明文，敏感字段在 UI 上以锁头展示 |
| **审计与追踪** | `internal/plugin/security.go` + `internal/plugin/audit_integration_test.go` | 插件启动、停止、调用等事件产生审计日志 |

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

## 五、五个扩展点插件制作文档

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

## 六、本仓库已有的最小独立开发示例

```text
plugins/
├── template-datasource/        模板：数据源插件最小脚手架
├── template-parser/            模板：解析插件最小脚手架
├── template-web-search/        模板：搜索插件最小脚手架
└── weknora-plugin-localdir/    真实示例：本地目录数据源（含 go.mod）
```

每个模板都包含完整的 `plugin.yaml` + Go 入口 + 独立 go.mod（使用 `replace` 指令指向本地 WeKnora 源码以便联调；独立发布时应删除该 replace）。

---

## 七、当前实现的边界（仍待后续课题处理）

- 五类扩展点中，**数据源已有端到端验证**（飞书 / 语雀 / Notion / RSS / IMA / GitLab / LocalDir / GitHub / 钉钉）；其余四类的协议和适配层已就位，UI 接入完整度不同；
- 检索引擎已落地最小协议（`Describe`/`OpenStore`/`CloseStore`/`BatchPut`/`Search`/`Delete`/`Patch` + `store_handle` 会话），自愈式 `GRPCRetrieverRepository` 已接入内部 `RetrieveEngineRepository`，`copy_indices` 等高级能力暂为可选 capability；
- 模型管理的前端"plugin source"选项、检索引擎的前端 schema/credential 配置界面、解析的流式化、WebSearch 的统一 settings/credentials，仍是后续课题需要补齐的 P1 项；
- Docker 沙箱是硬隔离的唯一边界；ProcessRuntime 仅供开发；
- 受控 Egress 代理（`allowlist` 的网络层强制）尚未实现，SDK 层 GuardedHTTPClient 是软约束。

---

## 八、运行效果截图

以下截图来自外部数据源插件的实际运行，展示了「插件类型动态出现在创建列表 → 配置 → 同步 → 文档进入知识库」的完整链路：

![数据源类型选择：外部插件类型自动出现在创建列表](docs/images/datasource-types-selection.png)

![Local Directory 插件：增量同步成功](docs/images/datasource-localdir-sync.png)

![GitHub 插件：同步成功（22 个文档，6 个失败提示）](docs/images/datasource-github-sync.png)

![DingTalk 插件：增量同步成功](docs/images/datasource-dingtalk-sync.png)

![GitHub 同步进入知识库的文档列表](docs/images/knowledge-github-docs.png)