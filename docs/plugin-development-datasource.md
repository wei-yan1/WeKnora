# WeKnora 外部数据源插件开发指南

本文是数据源插件的完整开发指南，同时也是五类扩展点共用的插件骨架入口。
其他扩展点只需要复用本文中的 Manifest、Runtime、PluginControl、目录装载和
治理约定，再实现各自的业务协议。

本目录中的 `plugins/weknora-plugin-localdir` 是一个可独立构建的完整示例；`plugins/template-datasource` 是最小脚手架。外部插件不需要修改 WeKnora 主仓的注册表或同步流程。

## 1. 插件包结构

```text
my-plugin/
├── plugin.yaml
├── go.mod
├── main.go
└── README.md
```

`plugin.yaml` 是发现、兼容性和权限校验的入口。首期必须填写：

| 字段 | 说明 |
| --- | --- |
| `id` | 全局稳定的小写 ID；不要包含主仓内部 DataSource ID |
| `extension_type` | 首期填写 `datasource` |
| `version` | 插件自身版本 |
| `protocol_version` | 当前填写 `v1` |
| `weknora_version` | 兼容范围，例如 `>=0.7 <1.0` |
| `config` / `config_schema` | 用户配置项、类型、是否必填/敏感（敏感字段用 `secret: true` 标记）；推荐使用 `config_schema`。字段类型支持 `string` / `string[]` / `boolean` / `integer` / `number` / `directory` / `path` / `url`（`url` 会自动做 SSRF 校验） |
| `permissions.network` | `none`、`egress` 或 `allowlist` |
| `permissions.read_paths` | 允许读取的路径声明；运行时还要用只读挂载/沙箱执行 |
| `permissions.data` | 声明可处理的租户、知识库和数据源范围；`self` 表示当前调用上下文 |
| `entrypoint` | **必需**。进程方式填可执行文件路径（相对 `plugin.yaml` 所在目录），或 `docker://镜像:标签` 走容器运行；缺省会报 `plugin has no entrypoint` |
| `metadata.connector_type` | 可选，自定义连接器类型名（默认用插件 `id`）。**不能与内置类型名冲突**（见 1.0.1） |
| `metadata.icon` | 可选，连接器图标。支持两种写法：相对文件名（如 `icon.png`，指向插件目录内的文件，由宿主代理读取）或绝对 `http(s)://` URL（直接透传给前端） |
| `metadata.description` | 可选，连接器描述，显示在类型选择卡片上 |

权限声明不是安全边界本身。`network: none` 的插件在验收环境必须使用 `DockerRuntime`，该运行时使用 `--network none`；开发态 `ProcessRuntime` 不能作为安全隔离证明。

数据范围可以随插件能力一起声明：

```yaml
permissions:
  network: none
  read_paths: ["configured-root"]
  data:
    tenants: [self]
    knowledge_bases: [self]
    data_sources: [self]
```

这些范围是宿主审核和运行策略的输入；`self` 指当前调用的租户、知识库或数据源。插件不能仅凭 gRPC metadata 扩大自己的数据权限，宿主仍负责传入数据和执行最终授权。

### 1.0.1 自定义图标

插件可以自带连接器图标，随插件一起分发，无需把图片上传到外部 CDN：

```yaml
metadata:
  icon: icon.png          # 相对文件名 → 插件目录下的 icon.png
```

插件目录结构：

```text
my-plugin/
├── plugin.yaml           # metadata.icon: icon.png
├── my-plugin.exe
└── icon.png              # 图标文件，放在插件目录内
```

工作方式：

- **相对文件名**（如 `icon.png`）：宿主在加载插件时记录插件目录，图标通过 `GET /api/v1/datasource/icon/{connector_type}` 由宿主代理读取，前端 `<img>` 直接加载该地址。插件作者无需关心路径，只需把文件放在 `plugin.yaml` 同目录下。
- **绝对 `http(s)://` URL**：宿主直接透传给前端，插件作者可自行托管图标。
- **都不写**：前端回退到内置图标映射或首字母占位图。

安全约束：宿主只服务「已注册外部插件」声明的图标文件，且只允许图片扩展名（`.png` `.jpg` `.jpeg` `.svg` `.webp` `.gif` `.ico`），单个文件不超过 5 MB。插件无法通过该端点读取目录外的任意文件。

### 1.0.2 与内置连接器的关系

外部插件走的是 `ConnectorRegistry` 的 factory 通道（`RegisterFactory`），与内置连接器（飞书、语雀、Notion 等走 `Register` 单例通道）**并存、互不影响**：

- 内置连接器的实现、注册和调用路径**原样保留**，插件框架只是额外给它们登记了生命周期控制面（`BuiltinRuntime` 占位），不改变它们怎么干活；
- 宿主调用时通过 `GetForScope` 先查外部 factory、查不到再回退到内置单例，所以内置连接器在插件框架下照常工作；
- **注意**：外部插件通过 `metadata.connector_type` 自定义的类型名，**不能与内置类型名（如 `feishu`、`yuque`、`notion`）冲突**。因为「factory 优先」的查找顺序，同名的外部插件会覆盖内置实现。插件作者应使用自己独立的类型名前缀（例如 `yourorg.feishu-proxy`）避免冲突。

## 1.1 gRPC 协议与配置契约

插件运行时通信使用 `pkg/pluginapi/proto/plugin.proto` 定义的 Protobuf。协议方法名是强约束（`Validate` / `ListResources` / `FetchAll` / `FetchIncremental` / `Parse` / `Search`），但**请求体里的部分字段仍使用 `google.protobuf.Struct` 弱信封**——这是 v1 为换取「跨语言 + 无生成代码耦合」做的取舍，字段名与类型要到运行期才校验。

- 数据源使用 `DataSourceRequest` / `DataSourceResponse`；
- 解析器使用 `ParserRequest` / `ParserResponse`；
- 网络搜索使用 `WebSearchRequest` / `WebSearchResponse`；
- 握手与健康检查统一由 `PluginControl` service 承载（所有扩展类型通用），使用 `HandshakeRequest`、`HandshakeResponse`、`HealthRequest`、`HealthResponse`；业务 service（`DataSourcePlugin` / `ParserPlugin` / `WebSearchPlugin`）只负责各自的业务方法，不再包含 Handshake/Health；
- `DataSourceRequest.config` 在 v1 仍使用 `google.protobuf.Struct`，但宿主会依据 Manifest 的 `config_schema`（或兼容的 `config` 列表）在请求发出前做必填项、类型和枚举校验，插件作者不需要猜字段；
- `DataSourceResponse.cursor` 使用 `Cursor.data` 的 `bytes`，由插件自行编码和解码，推荐 JSON 或 protobuf 私有消息。
- `FetchedItem.updated_at` 使用 RFC3339/RFC3339Nano 字符串传输；格式错误会被宿主拒绝，不再静默替换为当前时间。后续协议版本会增加原生 protobuf Timestamp 字段，v1 插件继续兼容当前字段。

外部插件只需要依赖 `pkg/pluginapi` SDK，不需要手写 gRPC service descriptor。SDK 会把普通 Go 结构转换为 Protobuf 消息。

### 1.1.1 配置字段的 `url` 类型与 SSRF 校验

配置字段声明为 `type: url` 时，宿主会在配置进入插件**之前**自动做 SSRF 安全校验（复用主仓内置连接器同一套 `ValidateConnectorBaseURL` 防护）：

```yaml
config:
  - key: base_url
    type: url        # 用 url 而非 string
    required: true
```

宿主会拦截内网 IP、回环地址、云 metadata 地址（`169.254.169.254` 等）以及非 http/https 协议。**插件作者只需把 URL 字段声明为 `url` 类型，无需自己实现任何 SSRF 防护**；这是宿主在「不可信输入进入系统」这一关的统一防线。

对于声明 `network: allowlist` 的插件，宿主会依据 `permissions.allowed_destinations`
（域名白名单，支持 `*.example.com` 通配）向插件进程注入环境变量，SDK 再从环境变量
读取策略，并在出站时按**域名**（而非字符串前缀）匹配放行。插件必须使用
`pluginapi.NewPluginHTTPClient()` 才会经过这层 SDK 防护；`ProcessRuntime` 本身不提供
操作系统级的网络隔离，不能把它当作安全边界。

模板仓库里的 `go.mod` 可能包含 `replace github.com/Tencent/WeKnora => ../..`，
这只是当前源码树内联调的便利配置。复制到真正独立仓库后，应删除该
`replace`，改为依赖发布版 WeKnora SDK，或把经过审核的 `pkg/pluginapi` 版本
vendor 到插件仓库；插件运行时仍然不需要链接宿主主程序。

### 1.2 推荐的配置声明

```yaml
config_schema:
  type: object
  properties:
    settings:
      type: object
      required: [root]
      additionalProperties: false
      properties:
        root:
          type: string
          description: Directory to scan
        include_hidden:
          type: boolean
          default: false
```

`config` 列表仍可用于简单插件；当两者同时存在时，宿主优先使用 `config_schema`。

### 1.2.1 凭证分区（credentials）

带凭证的数据源（飞书、钉钉、GitHub 等）必须把敏感凭证放在 `credentials` 分区，
而不是 `settings`。宿主对此有两个硬性行为：

- 前端把 `config_schema` 渲染成两个表单区：`properties.settings`（基本设置）和
  `properties.credentials`（凭证，以密码框显示）；
- 「测试连接」时后端只把 `credentials` 下发给插件（`settings` 不下发），插件在
  `Validate` 里只能依赖 `credentials`。

因此凭证字段（如 `app_secret`、`token`）必须声明在 `properties.credentials` 下，
否则测试连接拿不到凭证、永远失败。完整示例：

```yaml
config_schema:
  type: object
  properties:
    settings:
      type: object
      required: [repositories]
      additionalProperties: false
      properties:
        repositories:
          type: array
          description: 逗号分隔的仓库，如 owner/repo 或 owner/repo:branch
    credentials:
      type: object
      additionalProperties: false
      required: [app_key, app_secret]
      properties:
        app_key:
          type: string
          description: 应用 AppKey（Client ID）
        app_secret:
          type: string
          secret: true      # 敏感字段，前端以密码框显示
          description: 应用 AppSecret（Client Secret）
```

插件端从 `request.Config["credentials"]` 读取凭证、从 `request.Config["settings"]`
读取普通配置。无凭证的数据源（如本地目录）可以只声明 `settings`，不写
`credentials`——前端会跳过凭证步骤。

### 1.3 调用上下文（InvocationContext）

宿主通过 gRPC metadata 传递调用关联信息，不修改 v1 protobuf，旧插件可以直接忽略：

```go
invocation := pluginapi.InvocationContextFromContext(ctx)
// invocation.DataSourceID
// invocation.OperationID
// invocation.TraceID
```

这些字段用于日志、审计和追踪，不是权限凭据。插件不得根据 metadata 中的租户或数据源 ID 绕过自身配置校验；宿主仍然是权限和数据归属的唯一权威。

## 1.4 生产运行治理

共享 Runtime 的业务调用由宿主统一治理：

- 每个插件有独立的最大并发、有限等待队列和排队超时；
- 队列满、排队超时和 Runtime draining 返回可重试的结构化 Admission 错误；
- Handshake 与 Health 属于控制面，不占用业务并发配额；
- `active`、`waiting`、`admitted`、`rejected`、`queue_timeouts` 可从 PluginManager 查询；
- Stop/自动 Restart 先进入 `draining`，拒绝新 Lease，等待现有调用；超时后主动取消旧调用；
- 新 generation 只有在旧调用全部释放且旧 Runtime 停止后才会启动，避免两代同时提交结果。

DataSource 同步还有独立的宿主级互斥，不由插件实现：

```text
trigger-lock  串行化：检查 pending/running → 创建 SyncLog → Asynq 入队
TaskID        每次运行独立：dssync:<dataSourceID>:<syncLogID>
sync-lock     Worker 执行期可续租锁；所有权丢失会取消同步并返回错误
```

Redis 模式使用 token-owned 锁，续租和释放均校验 owner；Redis 故障时 fail closed。Lite 单进程模式使用同语义的本地锁。

同步意图先写入数据库 Outbox，再投递消息队列；投递失败会保留 `pending`
记录并指数退避重试，达到告警阈值后输出高等级日志。不会因为一次 Redis
故障把同步意图标成成功或静默丢弃。

## 2. 实现 SDK Handler

核心数据源函数是四个，外加一个可选的祖先解析回调：

```go
pluginapi.DataSourceHandler{
    PluginID: "your-org.example-datasource",
    OnValidate: func(ctx context.Context, req pluginapi.Request) error { ... },
    OnListResources: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.Resource, error) { ... },
    OnFetchAll: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.FetchedItem, error) { ... },
    OnFetchIncremental: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.FetchedItem, map[string]any, error) { ... },
    // 可选：解析资源的祖先链（返回祖先 ExternalID 列表），宿主用它补全层级关系
    OnResolveResourceAncestors: func(ctx context.Context, req pluginapi.Request) ([]string, error) { ... },
}
```

> `OnResolveResourceAncestors` 是可选的；不实现时对应 RPC 返回空。它用于返回某个资源在源端的祖先 ExternalID 列表（例如目录树的父级路径），宿主据此补全父子关系。

大数据源可以使用可选的流式回调，按批次发送项目并在 cursor 到达安全边界时发送 checkpoint：

```go
OnFetchAllStream: func(ctx context.Context, req pluginapi.Request, emit func(pluginapi.Response) error) error {
    for _, item := range fetchNextPage(req) {
        if err := emit(pluginapi.Response{Items: []pluginapi.FetchedItem{item}}); err != nil {
            return err
        }
    }
    return emit(pluginapi.Response{Cursor: nextCursor})
},
```

宿主会在收到带 cursor 的响应后调用 `StreamHandler.Checkpoint`。如果插件只实现 unary 回调，SDK 仍会提供兼容的单批 fallback；这种模式适合中小规模数据，不具备同步中途恢复能力。

启动代码读取 `WEKNORA_PLUGIN_ADDR`，然后调用 `pluginapi.Serve(ctx, address, handler)`。主程序会先调用 `Handshake` 和 `Health`，再执行同步。

## 3. 数据契约

### 3.1 请求 `Request`（SDK 结构体字段）

| 字段 | 类型 | 说明 |
|---|---|---|
| `Config` | `map[string]any` | 宿主透传的用户配置（含凭证，已按 `config_schema` 校验） |
| `ResourceIDs` | `[]string` | 本次要同步的资源 ID 列表 |
| `ParentID` | `string` | 父资源 ID（`ListResources` 懒加载子节点时用） |
| `Cursor` | `map[string]any` | 上次同步返回的游标（增量同步时） |

### 3.2 响应 `Response`（unary 返回值与流式 `emit` 共用）

| 字段 | 类型 | 说明 |
|---|---|---|
| `Error` | `string` | 错误信息，非空表示失败 |
| `Resources` | `[]Resource` | 资源列表（`ListResources` 返回） |
| `Ancestors` | `[]string` | 祖先 ExternalID 列表（`ResolveResourceAncestors` 返回） |
| `Items` | `[]FetchedItem` | 拉取的文档条目 |
| `Cursor` | `map[string]any` | 本次同步后的新游标 |
| `Warnings` | `[]string` | 非致命告警 |

### 3.3 `Resource` 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `ExternalID` | `string` | 资源在源端的稳定 ID |
| `Name` | `string` | 资源名称 |
| `ParentID` | `string` | 父资源 ID（树形结构） |
| `Type` | `string` | 资源类型（如 `wiki_space`、`document`） |
| `Metadata` | `map[string]string` | 附加信息 |

### 3.4 `FetchedItem` 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `ExternalID` | `string` | 源端稳定 ID，**必填** |
| `Title` | `string` | 文档标题 |
| `FileName` | `string` | 文件名（含扩展名） |
| `Content` | `[]byte` | 文档内容（二进制） |
| `URL` | `string` | 原始 URL（仅 URL 无内容时用） |
| `MIMEType` | `string` | MIME 类型 |
| `UpdatedAt` | `string` | 最后更新时间（RFC3339） |
| `Metadata` | `map[string]string` | 元数据（来源、作者等） |
| `IsDeleted` | `bool` | 标记删除（增量同步） |
| `ReplacesSubtree` | `bool` | 用本次返回项整体替换某子树 |
| `SubtreeKeep` | `[]string` | 子树替换时要保留的子项 ExternalID |

### 3.5 核心契约

`FetchedItem.ExternalID` 必须是源端稳定 ID，例如 `file:docs/readme.md`、`github:repo:blob:path`。不要把 WeKnora 的内部 DataSource ID 拼进去，否则导入/导出或重建数据源后会产生重复文档。

首次同步返回全部文件，并返回一个插件自定义的 cursor map。之后 `FetchIncremental` 收到上次返回的 cursor，只返回新增、修改和删除的项目。删除使用 `IsDeleted: true`；不要把删除的文件重新读成空文档。

目录型数据源可以用 `ReplacesSubtree: true` 声明「用本次返回的项整体替换某个子树」，并用 `SubtreeKeep` 列出要保留（不删除）的子项 ExternalID。这适用于「目录被整体重建/改名」这类场景，宿主据此做子树级替换，而不是逐项 diff。

文件型插件可以使用内容 SHA-256；远端 API 插件可以使用版本号、更新时间和分页 token。cursor 存在 WeKnora 数据库的数据源记录中，不属于插件进程，因此进程重启、容器替换后仍然可以继续增量同步。

## 4. 构建、安装和验证

```powershell
go mod tidy
go build -o weknora-plugin.exe .
```

把插件目录放进 WeKnora 的插件目录，目录中包含 `plugin.yaml` 和可执行文件。主程序发现 Manifest 后校验版本、权限、协议版本，再由 ProcessRuntime（开发态）或 DockerRuntime（安全验收/服务器）启动。

配置插件目录示例（按扩展类型分别指向五个目录）：

```powershell
$env:WEKNORA_PLUGIN_DIR_DATASOURCE = "D:\weknora-plugins\datasource"
$env:WEKNORA_PLUGIN_DIR_PARSER     = "D:\weknora-plugins\parser"
$env:WEKNORA_PLUGIN_DIR_SEARCH     = "D:\weknora-plugins\search"
$env:WEKNORA_PLUGIN_DIR_MODEL      = "D:\weknora-plugins\model"
$env:WEKNORA_PLUGIN_DIR_RETRIEVER  = "D:\weknora-plugins\retriever"
```

五个环境变量分别对应五类扩展点，每个变量指向存放该类插件的目录。未设置的环境变量会被忽略；如果显式设置了路径，但路径不存在或不是目录，主程序会返回错误（例如 `stat plugin root ... not a directory`），而不是静默跳过。每个目录下可以放置多个独立插件包：

```text
D:\weknora-plugins\datasource\
├── my-localdir\
│   ├── plugin.yaml
│   └── weknora-plugin.exe
└── my-github\
    ├── plugin.yaml
    └── weknora-plugin.exe
```

每个目录内可以放多个独立插件包，扫描时递归查找 `plugin.yaml` / `plugin.yml` / `plugin.json`。插件的扩展类型以 Manifest 里的 `extension_type` 字段为准；同一个 `id` 重复时主程序会拒绝启动，避免加载顺序造成歧义。

### 4.1 运行环境与构建目标（ProcessRuntime）

外部插件通过 `entrypoint` 由宿主启动。`entrypoint` 指向的可执行文件**必须与宿主的运行环境匹配**，否则插件无法启动，且宿主会因加载失败而 panic（例如 `connect plugin process: context deadline exceeded`、`stat plugin root ... not a directory`）：

| 宿主运行方式 | 宿主的操作系统/架构 | 插件应使用 |
| --- | --- | --- |
| 本地直接 `go run` / 跑编译后的 server | 与当前开发机一致 | 与开发机一致的原生二进制（如 Windows 下 `.exe`） |
| docker 容器（`app` 服务跑在 Linux） | **Linux/amd64** | **Linux 二进制**（无 `.exe`），由 `GOOS=linux GOARCH=amd64 go build` 产出 |
| WSL 内启动宿主 | Linux（WSL 发行版） | Linux 二进制 |

要点：

- `WEKNORA_PLUGIN_DIR_DATASOURCE` 等目录变量填的必须是**宿主进程能访问到的路径**。docker/WSL 下用容器/挂载路径（如 `/mnt/d/weknora-plugins/datasource`），不要填 Windows 盘符（`D:\...` 在 Linux 里会被解析成相对路径 `D`，导致 `stat plugin root "D"` 报错）。
- 使用 ProcessRuntime 时，插件作为宿主进程的**子进程**运行，共享宿主容器的网络命名空间。因此 `network: egress`/`allowlist` 的插件能直接访问公网（与内置连接器走同一条网络通路）；这也是需要联网的插件（GitHub、钉钉等）应使用 ProcessRuntime 的原因——`DockerRuntime` 目前强制 `--network none`，仅适用于 `network: none` 的插件。
- `go.mod` 里的 `replace github.com/Tencent/WeKnora => ../..` 这类路径在 Windows 和 WSL 间不通用；若需跨环境构建，建议改用相对路径（如 `../../../WeKnora-fork`）或发布版 SDK 依赖，避免构建环境差异导致编译失败。

第三方作者可以使用 SDK 的 `pluginapi.RunDataSourceConformance` 对已经连接的 gRPC client 做握手、健康、校验、全量和增量接口的冒烟测试。该函数签名要求分别传入控制面 client（`PluginControlClient`，负责握手与健康检查）和业务 client（`DataSourcePluginClient`，负责校验/同步），两个 client 共享同一个 gRPC 连接。验收时还要检查：首次同步有 N 个文件，改动一个文件后插件只返回该文件，主流程的 parser/embedding/index 计数也只增加该文件对应的批次。

流式插件还应验证：同步过程中收到多个响应批次；每个 cursor 都能被重新传给插件；在 checkpoint 后模拟进程中断，重启后不会重复处理已经确认的项目，也不会漏掉未确认项目。

真实外部进程验收测试可以直接指向独立插件仓库（推荐，这会读取该仓库
自己的 Manifest）：

```powershell
$env:GOCACHE = "D:\WeKnora-fork\tmp\go-cache-plugin"
$env:WEKNORA_LOCALDIR_PLUGIN_ROOT = "D:\weknora-plugin-localdir"
go test ./internal/plugin -run TestExternalLocalDirectoryProcessRuntime -count=1
```

如果只有已经构建好的可执行文件，也可以设置 `WEKNORA_LOCALDIR_PLUGIN_BIN`；
测试会临时生成 Manifest。使用 `PLUGIN_ROOT` 的路径更严格，因为它会启动
独立仓库自己的可执行文件并读取真实的 `plugin.yaml`，经过
`ProcessRuntime`、gRPC、`ConnectorResolver` 和 cursor 路径。

## 5. 最小独立开发盲测

盲测者只拿到本指南和 `plugins/template-datasource`，不得修改 WeKnora 主仓。其交付应当能够：

1. 修改 Manifest 的 ID 和配置字段；
2. 实现四个 Handler；
3. 独立构建并启动 gRPC 服务；
4. 被主程序发现、握手、健康检查；
5. 完成至少一个文件的全量和增量同步。

如果实现者需要改 `internal/container`、`ConnectorRegistry` 或同步服务，说明插件协议仍然泄漏了主仓内部细节，应先修订 SDK/文档。

## 5.1 运行期健康巡检

按需 `Manager.Health` 适合开发态检查；部署环境应显式启动健康监督器：

```go
err := manager.StartHealthSupervisor(ctx, plugin.HealthSupervisorConfig{
    Interval:         30 * time.Second,
    FailureThreshold:  3,
    RestartEnabled:    true,
    MaxRestartCount:   3,
})
```

监督器会更新 `running`、`degraded`、`unhealthy`、`failed` 状态。自动重启默认不强制开启，开启后也有最大次数限制；超过限制的插件进入 `failed`，避免无限重启和外部 API 风暴。

## 6. 其他扩展点的制作指南

五类扩展点各自有独立的插件制作文档。本文件只讲数据源；其余四类的指南见：

| 扩展点 | `extension_type` | 文档 |
| --- | --- | --- |
| 文档解析 | `parser` | [plugin-development-parser.md](plugin-development-parser.md) |
| 网络搜索 | `search` | [plugin-development-websearch.md](plugin-development-websearch.md) |
| 模型管理 | `model` | [plugin-development-model.md](plugin-development-model.md) |
| 检索引擎 | `retriever` | [plugin-development-retriever.md](plugin-development-retriever.md) |

它们共享本文第 1、1.1、1.2、1.3、1.4、4 节描述的同一套 manifest / runtime / manager / 权限 / 治理骨架，只是各自的业务协议与 SDK Handler 不同。开发对应插件前，请先读本文，再读对应扩展点的文档。
