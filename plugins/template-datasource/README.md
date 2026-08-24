# WeKnora 最小数据源插件模板

这是一个可以复制到独立仓库继续开发的最小数据源插件。模板默认返回一份
固定的 Markdown 示例文档，因此可以直接被 WeKnora 装载并完成一次最小同步；
复制后再把 `Validate`、资源列表、全量同步和增量同步函数替换成真实数据源逻辑。

## 1. 创建插件

复制本目录到新的独立仓库，并修改：

1. `plugin.yaml` 中的 `id`、`name`、版本、配置项和权限；
2. `main.go` 中 `DataSourceHandler.PluginID`，使其与 Manifest 的 `id` 一致；
3. 四个 Handler：`OnValidate`、`OnListResources`、`OnFetchAll`、
   `OnFetchIncremental`。

插件不需要修改 WeKnora 主仓库中的 ConnectorRegistry、容器装配或同步服务。

模板的 `go.mod` 使用 `replace ../..` 便于在 WeKnora 源码树内调试。复制到
独立仓库后，请删除这条 replace，改用发布版 SDK，或将 `pkg/pluginapi` vendor
进自己的仓库；运行时仍只通过 gRPC 与 WeKnora 通信。

## 2. 本地编译和运行

在模板目录执行：

```powershell
go mod tidy
go build -o weknora-plugin-example.exe .
$env:WEKNORA_PLUGIN_ADDR = "127.0.0.1:9778"
.\weknora-plugin-example.exe
```

实际部署时，宿主会注入 `WEKNORA_PLUGIN_ADDR`。插件应监听该地址，并通过
`pluginapi.Serve` 提供 gRPC 服务。

## 3. 配置和返回值约定

宿主把 Manifest 中的配置编码到 `pluginapi.Request.Config`：

```go
settings, _ := request.Config["settings"].(map[string]any)
endpoint, _ := settings["endpoint"].(string)
```

每个可同步对象都应返回稳定的 `ExternalID`。文件类数据源还应让 `Title`
保留文件扩展名，因为 v1 协议会用它兼容现有文档解析入口。

`FetchIncremental` 应读取 `request.Cursor`，只返回新增或变化的对象；删除的
对象返回 `FetchedItem{ExternalID: id, IsDeleted: true}`，并返回下一次同步
需要保存的 cursor。

数据量较大时，推荐同时实现 SDK 提供的流式 Handler，在批次之间返回 cursor，
这样宿主可以在同步中途保存检查点。只实现 unary Handler 仍可兼容中小规模
数据源，但不具备中途恢复能力。

## 4. 构建独立插件包

最终至少需要把下面文件放在同一个插件目录：

```text
my-plugin/
├── plugin.yaml
└── weknora-plugin-example.exe
```

然后设置宿主的插件目录：

```powershell
$env:WEKNORA_PLUGIN_DIRS = "D:\path\to\my-plugin"
```

宿主会发现 Manifest、检查版本和权限、启动进程、完成握手和健康检查，
然后把数据源元数据加入统一的数据源类型列表。新增插件不需要重新修改
WeKnora 主仓代码。

## 5. 网络权限

Manifest 中声明 `network: none` 时，生产验收应使用 OCI/Docker 运行时验证
`--network none`。插件业务代码若需要主动发起 HTTP 请求，应使用 SDK 的
`pluginapi.NewPluginHTTPClient()`；被拒绝的请求会写入结构化审计行，宿主
会从插件 stderr 读取并记录。`ProcessRuntime` 仅适合开发态，不能作为操作系统
级网络隔离的证明。
