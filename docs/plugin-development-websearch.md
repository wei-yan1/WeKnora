# 网络搜索（Web Search）插件制作指南

> 本文面向要独立开发一个 **网络搜索（Web Search）扩展点插件** 的开发者。
> 它只依赖本仓库的 `pkg/pluginapi` SDK 和主仓的插件扫描机制，无需改动 WeKnora 主仓源码。
>
> 阅读前置：先读 [plugin-development-datasource.md](plugin-development-datasource.md) 了解统一的插件骨架。本文只讲 Web Search 特有的部分。

## 1. Web Search 插件是做什么的

Web Search 插件负责在「联网搜索」环节，把一个查询交给某个搜索引擎（或自有搜索服务），返回一批结果（标题、URL、摘要、正文、来源、发布时间）。它对应主仓的网络搜索 Provider 注册机制。

宿主在用户触发联网检索时，按 provider 类型找到对应插件，把查询透传过去，拿到结果后交给后续的融合/引用流程。

## 2. 五类扩展点的统一骨架（速览）

Web Search 插件与其他四类一样，遵循同一套发现与生命周期协议：

| 项 | Web Search 插件取值 |
|---|---|
| `api_version` | `weknora.plugin/v1` |
| `extension_type` | `search`（**注意不是 `web_search`**，源码常量就是 `search`） |
| `protocol_version` | `v1` |
| `entrypoint` | 必填，见 quickstart |
| 环境变量 | `WEKNORA_PLUGIN_ADDR` |

启动后必须实现两个统一服务：

- `PluginControl`：`Handshake` + `Health`。
- `WebSearch`：本扩展点特有的检索协议（见下）。

用 SDK 的 `ServeWebSearch` 一行即可同时挂起这两个服务。

## 3. 核心协议：`WebSearchPlugin` 服务

```text
rpc Search(WebSearchRequest) returns (WebSearchResponse)
```

### 3.1 请求 `WebSearchRequest`（SDK 结构体字段）

| 字段 | 类型 | 说明 |
|---|---|---|
| `Query` | `string` | 搜索查询词 |
| `MaxResults` | `int` | 期望返回的最大结果数 |
| `IncludeDate` | `bool` | 是否要求返回发布时间 |
| `APIKey` | `string` | 搜索引擎 API Key（由宿主从配置注入） |
| `EngineID` | `string` | 自定义搜索引擎 ID |
| `BaseURL` | `string` | 搜索服务地址 |
| `ProxyURL` | `string` | 代理地址 |
| `ExtraConfig` | `map[string]string` | 其他引擎特定配置 |

### 3.2 响应 `WebSearchResponse`（SDK 结构体字段）

| 字段 | 类型 | 说明 |
|---|---|---|
| `Results` | `[]*WebSearchResult` | 结果列表 |
| `Error` | `string` | 错误信息，非空表示失败 |

`WebSearchResult` 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Title` | `string` | 标题 |
| `URL` | `string` | 链接 |
| `Snippet` | `string` | 摘要 |
| `Content` | `string` | 正文 |
| `Source` | `string` | 来源（通常是搜索引擎名） |
| `PublishedAt` | `string` | 发布时间 |

关键语义约束：

1. **错误要显式**：`OnSearch` 返回 error 时，SDK 会自动把 error 填入 `Error` 字段（若你未手动填）；宿主看到 `Error` 非空就按失败处理。
2. **`Source` 要诚实**：结果来源要真实标注，供宿主引用展示，不能伪造。
3. **遵守 `MaxResults`**：尽量不超过请求上限，避免宿主侧结果过多。

## 4. SDK 侧如何实现：`ServeWebSearch`

```go
package main

import (
    "context"
    "os"

    "github.com/Tencent/WeKnora/pkg/pluginapi"
)

type mySearch struct{}

func (mySearch) Search(ctx context.Context, req pluginapi.WebSearchRequest) (pluginapi.WebSearchResponse, error) {
    results, err := callMySearchEngine(req.Query, req.MaxResults, req.APIKey)
    if err != nil {
        return pluginapi.WebSearchResponse{}, err // SDK 会填到 Error 字段
    }
    return pluginapi.WebSearchResponse{Results: results}, nil
}

func main() {
    handler := pluginapi.WebSearchHandler{
        PluginID: "example.mysearch",
        OnSearch: (mySearch{}).Search,
    }
    addr := os.Getenv("WEKNORA_PLUGIN_ADDR")
    if err := pluginapi.ServeWebSearch(context.Background(), addr, handler); err != nil {
        panic(err)
    }
}
```

要点：

- `WebSearchHandler.PluginID` 必须与 `plugin.yaml` 的 `id` 一致。
- `OnSearch` 为 nil 时返回 `Error: "web search handler is not implemented"`。
- `Handshake` / `Health` 由 `WebSearchHandler` 自动实现。

## 5. `plugin.yaml` 示例

```yaml
api_version: weknora.plugin/v1
id: example.mysearch
name: My Search Provider
version: 1.0.0
extension_type: search
protocol_version: v1
weknora_version: ">=0.7 <1.0"
entrypoint: ./mysearch

capabilities:
  - web_search

metadata:
  provider_type: mysearch           # 供宿主注册与路由的 provider 类型名

config:
  - key: api_key
    type: string
    description: 搜索引擎 API Key
    required: true
    secret: true                    # 敏感字段，宿主会脱敏处理
  - key: base_url
    type: string
    description: 搜索服务地址
    required: false

permissions:
  network: allowlist                # 联网插件必须声明网络策略
  allowed_destinations:             # 白名单：只写域名，支持通配，不带 https:// 前缀
    - "api.example.com"
    - "*.example.com"
```

### 网络策略（联网插件必读）

Web Search 插件需要出站联网，所以 `permissions.network` 不能是 `none`。两种选择：

- `egress`：受控出站（生产化的联网插件）。
- `allowlist`：白名单模式，**必须同时提供 `allowed_destinations`**（源码 `manifest.go` 会校验：`allowlist` 且无白名单会直接装载失败）。

白名单匹配按**域名（host）+ 通配**，不是字符串前缀匹配——写 `api.example.com` 不会误放行 `api.example.com.evil.com`。

## 6. 校验与验证

SDK 提供 conformance 测试入口 `RunWebSearchConformance`，可对插件做协议级自检：

- 握手回报 `extension_type=search`；
- 正常查询返回结构化结果；
- 错误查询显式报错。

建议：本地起 server → 跑 conformance → 通过后再放入 `WEKNORA_PLUGIN_DIR_SEARCH` 指向的目录交给宿主装载。

## 7. 宿主侧如何被使用

Web Search 与数据源、Parser 的区别是：它不是一次性同步，而是一个按租户配置即时调用的 `ProviderFactory`。外部插件进程可长期运行，宿主每次从租户配置创建一个轻量代理，把该租户的参数随请求传入插件。

加载后，宿主会把 Web Search 插件注册到现有 `web_search.Registry`，因此既有的 `WebSearchService.resolveProvider`、租户参数传递、结果过滤和后续 RAG 压缩流程无需按插件 ID 增加分支。API Key 不会写入插件进程全局状态，而是由宿主创建 provider 实例时按租户传给每次 Search 请求。

## 8. 与宿主的关系（边界）

Web Search 插件**只负责「查询 → 搜索结果列表」**，不负责：

- 结果与知识库检索的融合（RRF 等）——宿主处理；
- 结果内容的抓取/解析——那是 Parser / DataSource 的职责；
- 权限过滤、租户隔离——宿主处理。

插件内部可自行决定调用哪个搜索引擎、如何翻页、如何限流，宿主不关心。
