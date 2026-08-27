# 模型管理（Model）插件制作指南

> 本文面向要独立开发一个 **模型管理（Model Provider）扩展点插件** 的开发者。
> 它只依赖本仓库的 `pkg/pluginapi` SDK 和主仓的插件扫描机制，无需改动 WeKnora 主仓源码。
>
> 五类扩展点共用的插件骨架见本文第 2 节。本文只讲 Model 特有的部分。

## 1. Model 插件是做什么的

Model 插件负责把一个外部模型服务（自有模型、第三方 API 等）接入 WeKnora。与其他四类「单一 RPC」不同，Model 插件**一个进程可以同时承载多达五种能力**：

| 能力 | capability 字符串 | 作用 |
|---|---|---|
| 对话 | `chat` | `Chat` / `ChatStream` |
| 向量化 | `embedding` | `Embed` / `BatchEmbed` |
| 重排序 | `rerank` | `Rerank` |
| 视觉语言模型 | `vllm` | `PredictVLM`（注意 capability 串是 `vllm`，不是 `vlm`） |
| 语音识别 | `asr` | `Transcribe` |

一个插件可实现全部或其中任意子集；未实现的能力把对应回调留 `nil` 即可，宿主调用时插件会返回「not implemented」。

## 2. 五类扩展点的统一骨架（速览）

| 项 | Model 插件取值 |
|---|---|
| `api_version` | `weknora.plugin/v1` |
| `extension_type` | `model` |
| `protocol_version` | `v1` |
| `entrypoint` | 必填，见 quickstart |
| 环境变量 | `WEKNORA_PLUGIN_ADDR` |

启动后必须实现两个统一服务：

- `PluginControl`：`Handshake` + `Health`。
- `Model`：本扩展点的模型协议（五种能力，见下）。

用 SDK 的 `ServeModel` 一行即可同时挂起这两个服务。

## 3. 核心协议：`ModelPlugin` 服务

Model 的 RPC 比其它四类多，但每个都很薄：

```text
rpc ModelInfo(ModelInfoRequest) returns (ModelInfoResponse)          // 回报能力列表
rpc ValidateConfig(ModelValidateRequest) returns (ModelValidateResponse)  // 校验配置
rpc Chat(ModelChatRequest) returns (ModelChatResponse)               // 对话
rpc ChatStream(ModelChatRequest) returns (stream ModelStreamResponse) // 流式对话
rpc Embed(ModelEmbedRequest) returns (ModelEmbedResponse)            // 单条向量化
rpc BatchEmbed(ModelBatchEmbedRequest) returns (ModelBatchEmbedResponse) // 批量向量化
rpc Rerank(ModelRerankRequest) returns (ModelRerankResponse)         // 重排序
rpc PredictVLM(ModelVLMRequest) returns (ModelVLMResponse)           // 视觉语言
rpc Transcribe(ModelASRRequest) returns (ModelASRResponse)           // 语音识别
```

## 4. SDK 侧如何实现：`ServeModel`

SDK 提供了 `ModelHandler` 结构体，你只需填充关心的回调，其余留 `nil`：

```go
package main

import (
    "context"
    "os"

    "github.com/Tencent/WeKnora/pkg/pluginapi"
)

type myModel struct{}

func (myModel) Chat(ctx context.Context, req pluginapi.ChatRequest) (pluginapi.ChatResult, error) {
    // req.Messages / req.Options 已由 SDK 解码好
    return pluginapi.ChatResult{Content: "hello"}, nil
}

func (myModel) Embed(ctx context.Context, text string) ([]float32, error) {
    return []float32{0.1, 0.2, 0.3}, nil
}

func (myModel) Rerank(ctx context.Context, query string, docs []string) ([]pluginapi.RerankResult, error) {
    return []pluginapi.RerankResult{{Index: 0, RelevanceScore: 0.9}}, nil
}

func main() {
    handler := pluginapi.ModelHandler{
        PluginID:     "example.mymodel",
        Capabilities: []string{"chat", "embedding", "rerank"}, // 只声明实现了的能力
        OnChat:       (myModel{}).Chat,
        OnEmbed:      (myModel{}).Embed,
        OnRerank:     (myModel{}).Rerank,
        // OnChatStream / OnBatchEmbed / OnPredictVLM / OnTranscribe 留 nil
    }
    addr := os.Getenv("WEKNORA_PLUGIN_ADDR")
    if err := pluginapi.ServeModel(context.Background(), addr, handler); err != nil {
        panic(err)
    }
}
```

### 各回调签名速查

| 回调 | 签名 |
|---|---|
| `OnValidateConfig` | `func(ctx, map[string]any) error` |
| `OnChat` | `func(ctx, ChatRequest) (ChatResult, error)` |
| `OnChatStream` | `func(ctx, ChatRequest, func(StreamChunk) error) error` |
| `OnEmbed` | `func(ctx, string) ([]float32, error)` |
| `OnBatchEmbed` | `func(ctx, []string) ([][]float32, error)` |
| `OnRerank` | `func(ctx, string, []string) ([]RerankResult, error)` |
| `OnPredictVLM` | `func(ctx, [][]byte, string) (string, error)` |
| `OnTranscribe` | `func(ctx, []byte, string) (string, error)` |
| `OnHealth` | `func(ctx) HealthResponse` |

要点：

- `ModelHandler.PluginID` 必须与 `plugin.yaml` 的 `id` 一致。
- `Capabilities` 数组声明实现了哪些能力，且**必须与 plugin.yaml 的 capabilities 一致**——声明了却没实现回调，会在调用时返回「not implemented」。
- 向量返回维度由 `Embed` 的返回长度决定（`BatchEmbed` 用首个向量的长度作为 `Dimensions`）。

### 流式对话（`OnChatStream`）

流式能力通过回调式 API 实现，插件不需要手写 gRPC stream：

```go
func (myModel) ChatStream(ctx context.Context, req pluginapi.ChatRequest, emit func(pluginapi.StreamChunk) error) error {
    // 逐块产出
    for _, chunk := range myStreamFromRemote(ctx, req) {
        if err := emit(pluginapi.StreamChunk{Content: chunk}); err != nil {
            return err // 下游取消
        }
    }
    return emit(pluginapi.StreamChunk{Done: true})
}
```

## 5. `plugin.yaml` 示例

```yaml
api_version: weknora.plugin/v1
id: example.mymodel
name: My Model Provider
version: 1.0.0
extension_type: model
protocol_version: v1
weknora_version: ">=0.7 <1.0"
entrypoint: ./mymodel

capabilities:
  - chat
  - embedding
  - rerank
  - vllm
  - asr

metadata:
  provider: mymodel                # 宿主路由用的 provider 名（与 id 不同时用这个）

config:
  - key: api_key
    type: string
    description: 模型服务 API Key
    required: true
    secret: true
  - key: base_url
    type: string
    description: 模型服务地址
    required: true

permissions:
  network: allowlist                # 模型插件通常需要出站调用模型服务
  allowed_destinations:
    - "api.example.com"
```

## 6. 宿主如何调用模型插件（重要：调用期解析机制）

模型插件与其他四类的一个关键差异是：**宿主不会缓存一个固定的 gRPC client**，而是**每次调用时动态解析**。

原因是模型插件承载有状态的长连接能力，且可能被健康监控重启。宿主侧（`internal/plugin`）注册的是一个「resolver 回调」，每次模型调用时：

1. 通过 admission control 申请一次调用租约（并发限制 + drain 检查）；
2. 读取**当前**运行时连接（插件重启后自动拿到新连接，不会用到已关闭的旧 client）；
3. 注入租户上下文（`WithInvocationContext`）；
4. 调用结束后释放租约（`release`，流式场景在流结束后释放）。

> 对你的意义：插件进程重启后，宿主会自动恢复调用，**插件作者无需做任何特殊处理**；但也意味着插件实现应当是**无状态或能承受重启**的（重启期间的请求会失败一次并走宿主重试）。

## 7. 校验与验证

SDK 提供 `RunModelConformance`，可对模型插件做协议级冒烟测试（与 DataSource / Parser / WebSearch 三者对齐）。它依次：

1. 握手回报 `extension_type=model` 且 `protocol_version=v1`；
2. 健康检查返回非空 state；
3. 读取 `ModelInfo` 声明的能力列表；
4. 对**每个声明的能力**发一个空请求，校验响应信封无 error。

要点：

- **只测声明的能力**：未声明的能力会被跳过，不会误判为失败；
- **声明了但返回 error 记为失败**：例如声明 `embedding` 却没实现 `OnEmbed`（返回「not implemented」），该能力会被标记失败并写入 `Errors`。

用法：

```go
report := pluginapi.RunModelConformance(ctx, controlClient, modelClient)
// report.HandshakeOK / report.HealthOK / report.ChatOK / report.EmbeddingOK ...
// report.RerankOK / report.VLMOK / report.ASROK / report.Errors
```

建议流程：本地起 server（`ServeModel`）→ 跑 `RunModelConformance` → 全部通过后再放入 `WEKNORA_PLUGIN_DIR_MODEL` 交给宿主装载。

补充：还需额外验证**重启恢复**（kill 进程 → 重启 → 再次调用），这对应宿主侧 resolver 机制，宿主已有自动化回归测试覆盖；插件作者只需确保自己的实现无状态或能承受重启。

## 8. 与宿主的关系（边界）

Model 插件**只负责「模型能力的实际调用」**（把请求发给模型服务、把结果返回），不负责：

- 模型配置的存储与管理——宿主管理；
- 知识库检索、融合、切分——其他扩展点 / 宿主的职责；
- 租户校验、权限、限流——宿主处理（通过 resolver 里的 admission + invocation context 注入）。

插件内部可自行管理模型服务的连接池、重试、密钥轮换等，宿主不关心。
