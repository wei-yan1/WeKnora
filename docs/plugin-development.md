# WeKnora 外部数据源插件开发指南

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
| `weknora_version` | 兼容范围，例如 `>=1.0 <2.0` |
| `config` / `config_schema` | 用户配置项、类型、是否必填/敏感；推荐使用 `config_schema` |
| `permissions.network` | `none`、`egress` 或 `allowlist` |
| `permissions.read_paths` | 允许读取的路径声明；运行时还要用只读挂载/沙箱执行 |
| `permissions.data` | 声明可处理的租户、知识库和数据源范围；`self` 表示当前调用上下文 |
| `entrypoint` | 开发态可执行文件路径，或 `docker://镜像:标签` |

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

## 1.1 gRPC 协议与配置契约

插件运行时通信使用 `pkg/pluginapi/proto/plugin.proto` 定义的强类型 Protobuf，不再使用 `google.protobuf.Struct` 作为业务请求/响应外壳。

- 数据源使用 `DataSourceRequest` / `DataSourceResponse`；
- 解析器使用 `ParserRequest` / `ParserResponse`；
- 网络搜索使用 `WebSearchRequest` / `WebSearchResponse`；
- 生命周期使用 `HandshakeRequest`、`HandshakeResponse`、`HealthRequest`、`HealthResponse`；
- `DataSourceRequest.config` 在 v1 仍使用 `google.protobuf.Struct`，但宿主会依据 Manifest 的 `config_schema`（或兼容的 `config` 列表）在请求发出前做必填项、类型和枚举校验，插件作者不需要猜字段；
- `DataSourceResponse.cursor` 使用 `Cursor.data` 的 `bytes`，由插件自行编码和解码，推荐 JSON 或 protobuf 私有消息。
- `FetchedItem.updated_at` 使用 RFC3339/RFC3339Nano 字符串传输；格式错误会被宿主拒绝，不再静默替换为当前时间。后续协议版本会增加原生 protobuf Timestamp 字段，v1 插件继续兼容当前字段。

外部插件只需要依赖 `pkg/pluginapi` SDK，不需要手写 gRPC service descriptor。SDK 会把普通 Go 结构转换为强类型 Protobuf 消息。

模板仓库里的 `go.mod` 可能包含 `replace github.com/Tencent/WeKnora => ../..`，
这只是当前源码树内联调的便利配置。复制到真正独立仓库后，应删除该
`replace`，改为依赖发布版 WeKnora SDK，或把经过审核的 `pkg/pluginapi` 版本
vendor 到插件仓库；插件运行时仍然不需要链接宿主主程序。

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

## 2. 实现 SDK Handler

只需要实现四个数据源函数：

```go
pluginapi.DataSourceHandler{
    PluginID: "your-org.example-datasource",
    OnValidate: func(ctx context.Context, req pluginapi.Request) error { ... },
    OnListResources: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.Resource, error) { ... },
    OnFetchAll: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.FetchedItem, error) { ... },
    OnFetchIncremental: func(ctx context.Context, req pluginapi.Request) ([]pluginapi.FetchedItem, map[string]any, error) { ... },
}
```

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

`FetchedItem.ExternalID` 必须是源端稳定 ID，例如 `file:docs/readme.md`、`github:repo:blob:path`。不要把 WeKnora 的内部 DataSource ID 拼进去，否则导入/导出或重建数据源后会产生重复文档。

首次同步返回全部文件，并返回一个插件自定义的 cursor map。之后 `FetchIncremental` 收到上次返回的 cursor，只返回新增、修改和删除的项目。删除使用 `IsDeleted: true`；不要把删除的文件重新读成空文档。

文件型插件可以使用内容 SHA-256；远端 API 插件可以使用版本号、更新时间和分页 token。cursor 存在 WeKnora 数据库的数据源记录中，不属于插件进程，因此进程重启、容器替换后仍然可以继续增量同步。

## 4. 构建、安装和验证

```powershell
go mod tidy
go build -o weknora-plugin.exe .
```

把插件目录放进 WeKnora 的插件目录，目录中包含 `plugin.yaml` 和可执行文件。主程序发现 Manifest 后校验版本、权限、协议版本，再由 ProcessRuntime（开发态）或 DockerRuntime（安全验收/服务器）启动。

配置插件目录示例：

```powershell
$env:WEKNORA_PLUGIN_DIRS = "D:\weknora-plugins"
```

多个目录可用 Windows 的 `;` 分隔。每个目录下可以放多个独立插件包；同一个 `id` 重复时主程序会拒绝启动，避免加载顺序造成歧义。

第三方作者可以使用 SDK 的 `pluginapi.RunDataSourceConformance` 对已经连接的 gRPC client 做握手、健康、校验、全量和增量接口的冒烟测试。验收时还要检查：首次同步有 N 个文件，改动一个文件后插件只返回该文件，主流程的 parser/embedding/index 计数也只增加该文件对应的批次。

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

## 6. Parser 插件（文档解析引擎扩展）

Parser 插件走的是同一套 manifest / runtime / manager 体系，只是业务协议从 datasource 的 `Validate / ListResources / FetchAll / FetchIncremental` 换成了 `Parse`。

### 6.1 manifest 约定

Parser 插件的 manifest 关键字段：

| 字段 | 说明 |
| --- | --- |
| `extension_type` | 必须是 `parser` |
| `capabilities` | 至少声明插件实际实现的能力，例如 `parse` |
| `metadata.engine_name` | 暴露给 WeKnora 的解析引擎名；不写时默认用插件 `id` |
| `metadata.file_types` | 该引擎支持的文件类型列表，不能为空 |
| `metadata.description` | 可选，显示在引擎列表里的描述 |

示例：

```yaml
api_version: weknora.plugin/v1
id: example.template-markdown-parser
name: Template Markdown Parser
version: 0.1.0
extension_type: parser
protocol_version: v1
weknora_version: ">=1.0 <2.0"
entrypoint: ./template-markdown-parser
permissions:
  network: none
capabilities: [parse]
metadata:
  engine_name: template_markdown
  description: Minimal parser example
  file_types: [md, markdown, txt]
```

### 6.2 SDK 入口

Parser 插件推荐直接使用 `pkg/pluginapi` 里的 `ParserHandler` 和 `ServeParser`：

```go
pluginapi.ServeParser(ctx, address, pluginapi.ParserHandler{
    PluginID: "example.template-markdown-parser",
    Capabilities: []string{"parse"},
    OnParse: func(ctx context.Context, req pluginapi.ParserRequest) (pluginapi.ParserResponse, error) {
        ...
    },
})
```

宿主侧在加载后会把这个插件注册到现有的文档解析引擎目录里，所以 `docparser.NewReader(...)` 和 `docparser.ListAllEngines(...)` 会把它当成一个正常引擎看待。

### 6.3 最小可运行模板

仓库里已经提供：

- `plugins/template-parser`：Parser 脚手架
- `plugins/template-parser/README.md`：最小构建说明

第三方作者可以按以下步骤起步：

1. 复制 `plugins/template-parser` 到独立仓库；
2. 修改 `plugin.yaml` 里的 `id`、`engine_name` 和 `file_types`；
3. 实现 `OnParse`；
4. 本地 `go build` 出可执行文件；
5. 让主仓扫描到 `plugin.yaml` 并启动；
6. 在 WeKnora 的解析引擎列表中确认它出现，并对一份测试文档完成解析。

### 6.4 验证方式

Parser 插件的最小验证建议使用两层检查：

1. SDK 级烟雾测试：用 `pluginapi.RunParserConformance` 跑握手、健康检查和一次 Parse；
2. 宿主级集成测试：让 WeKnora 通过 `LoadExternal` 加载插件，然后用 `docparser.NewReader(...)` 读一份样本文档，确认结果进入后续的切块 / 向量化 / 索引链路。

## 7. Web Search 插件（网络搜索引擎扩展）

Web Search 与数据源、Parser 的区别是：它不是一次性同步，而是一个按租户配置即时调用的 `ProviderFactory`。外部插件进程可以长期运行，WeKnora 每次从租户配置创建一个轻量代理，把该租户的参数随请求传入插件。

### 7.1 manifest 约定

```yaml
extension_type: search
capabilities: [search]
permissions:
  network: egress
metadata:
  provider_type: my_search
  description: My Search API
```

`metadata.provider_type` 是保存到 Web Search provider 配置中的类型 ID；不填写时默认使用插件 `id`。插件类型必须唯一，不能覆盖内置或其他外部类型。

`config` 用来声明插件需要的配置项，例如 `api_key`、`engine_id`、`base_url`。API Key 不会写入插件进程的全局状态，而是由宿主在创建 provider 实例时按租户传给每次 Search 请求。

### 7.2 SDK 入口

```go
pluginapi.ServeWebSearch(ctx, address, pluginapi.WebSearchHandler{
    PluginID: "my-org.search",
    Capabilities: []string{"search"},
    OnSearch: func(ctx context.Context, req pluginapi.WebSearchRequest) (pluginapi.WebSearchResponse, error) {
        // 使用 req.Query、req.MaxResults 和 req.APIKey 等字段调用你的服务
        return pluginapi.WebSearchResponse{Results: ...}, nil
    },
})
```

返回结果中的 `PublishedAt` 使用 RFC3339 字符串；宿主会把它转换为现有 `types.WebSearchResult` 的时间字段。

### 7.3 最小模板

仓库提供 `plugins/template-web-search`。它返回一个确定性的本地示例结果，故意声明 `network: none`，用于验证协议和注册流程；真实搜索服务需要把权限改为合适的网络策略，并使用 Docker/OCI 运行时或受控出口来满足安全边界。

外部搜索插件加载后，宿主会把它注册到现有 `web_search.Registry`。因此既有的 `WebSearchService.resolveProvider`、租户参数传递、结果过滤和后续 RAG 压缩流程无需按插件 ID 增加分支。

### 7.4 验证方式

1. 用 `pluginapi` 客户端执行 Handshake、Health、Search 烟雾测试；
2. 用 `LoadExternalWithRegistries` 加载外部包；
3. 通过 `Registry.CreateProvider(provider_type, tenantParams)` 创建实例；
4. 执行 `Search`，确认 API Key 等参数只属于当前请求；
5. 检查返回的 URL、摘要、来源和发布时间能映射回 WeKnora 现有搜索结果结构。
