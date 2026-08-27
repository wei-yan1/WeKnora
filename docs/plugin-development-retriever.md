# 检索引擎（Retriever）插件制作指南

> 本文面向要独立开发一个 **检索引擎（Retriever）扩展点插件** 的开发者。
> 它只依赖本仓库的 `pkg/pluginapi` SDK 和主仓的插件扫描机制，无需改动 WeKnora 主仓源码。
>
> 阅读前置：先读 [plugin-development-datasource.md](plugin-development-datasource.md) 了解统一的插件骨架（`plugin.yaml` 字段、`PluginControl` 握手/健康、`WEKNORA_PLUGIN_ADDR`、目录环境变量、构建与装载）。本文只讲 Retriever 特有的部分，不重复通用骨架。

## 1. Retriever 插件是做什么的

Retriever 插件负责「把**宿主已经算好的向量**写入某个向量/关键词存储后端，并按查询从后端检索出来」。它对应主仓的 `RetrieveEngineRepository`，但插件**不需要**理解主仓的 embedding、分块、融合等概念——它只做最窄的一件事：**向量/关键词的存取**。

与另外四类扩展点的关键区别：

| 特性 | Retriever | 数据源/Parser/搜索/模型 |
|---|---|---|
| 状态 | **有状态**（一个 Store 是一个打开的会话，持有连接、索引） | 无状态（一次调用一次结果） |
| embedding | **宿主算好传入**，插件只负责存取 | 各司其职 |
| 生命周期 | `OpenStore` → 读写 → `CloseStore` | 无会话概念 |

宿主在「创建 VectorStore」时按 `engine_type` 找到插件，`OpenStore` 得到一个不透明的 `store_handle`，之后所有读写都携带这个 handle。

## 2. 五类扩展点的统一骨架（速览）

Retriever 插件与其他四类一样，遵循同一套发现与生命周期协议：

| 项 | Retriever 插件取值 |
|---|---|
| `api_version` | `weknora.plugin/v1` |
| `extension_type` | `retriever` |
| `protocol_version` | `v1` |
| `entrypoint` | 必填，见 quickstart |
| 环境变量 | `WEKNORA_PLUGIN_ADDR`；插件目录用 `WEKNORA_PLUGIN_DIR_RETRIEVER` |

启动后必须实现两个统一服务：

- `PluginControl`：`Handshake`（回报插件 ID、`extension_type=retriever`、能力、版本）+ `Health`（健康上报）。
- `Retriever`：本扩展点特有的存取协议（见下）。

> 用 SDK 的 `ServeRetriever` 一行即可同时挂起这两个服务，无需手写 gRPC 注册。

### ⚠️ `PluginControl.Health` 的重要约束

**`Health` 只上报「插件进程本身是否正常」，绝不能因为某个 Store 后端不可达就返回 `unhealthy`。** 否则一个坏 Store 会导致宿主重启整个插件运行时，把其他健康的 Store 也一并打断。

单个 Store 的后端故障应通过该 Store 的业务调用返回错误来表达，而不是让整个插件的 `Health` 失败。

## 3. 核心协议：`RetrieverPlugin` 服务

```text
rpc Describe(RetrieverDescribeRequest) returns (RetrieverDescribeResponse)
rpc OpenStore(RetrieverOpenStoreRequest) returns (RetrieverOpenStoreResponse)
rpc CloseStore(RetrieverCloseStoreRequest) returns (RetrieverResponse)
rpc BatchPut(RetrieverBatchPutRequest) returns (RetrieverBatchPutResponse)
rpc Search(RetrieverSearchRequest) returns (RetrieverSearchResponse)
rpc Delete(RetrieverDeleteRequest) returns (RetrieverResponse)
rpc Patch(RetrieverPatchRequest) returns (RetrieverResponse)
```

### 3.1 `Describe`：声明身份与能力

返回插件是什么引擎、支持什么能力、返回的分数是什么语义：

| 字段 | 类型 | 说明 |
|---|---|---|
| `EngineType` | `string` | 引擎类型名，与 `metadata.engine_type` 一致 |
| `Capabilities` | `[]string` | 能力声明，见第 5 节 |
| `ScoreSemantics` | `string` | 分数语义，见 3.5 节 |

### 3.2 `OpenStore` / `CloseStore`：会话生命周期

- `OpenStore(config)` → 返回 `store_handle`。插件在这里建立连接、连接池、创建必要的 collection/table/index。
- `store_handle` 是**不透明的、由 SDK 生成**的字符串，插件不应解析它，也不该把租户、地址、凭证编码进去。
- `CloseStore(store_handle)` → 释放该 Store 的连接、连接池、缓存等资源。

`config`（`RetrieverStoreConfig`）由宿主从 VectorStore 配置映射而来，分三部分：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Settings` | `map[string]any` | 非敏感连接参数（addr、host、port、use_tls、database 等） |
| `Credentials` | `map[string]any` | 敏感凭证（username、password、api_key） |
| `IndexConfig` | `map[string]any` | 索引/collection 配置 |

### 3.3 `BatchPut`：批量写入

写入一批记录。`RetrieverRecord` 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `RecordID` | `string` | 记录唯一标识 |
| `Content` | `string` | 文本内容（用于关键词检索） |
| `Embedding` | `[]float32` | **强类型向量**，宿主算好传入，插件不重新计算 |
| `Metadata` | `map[string]string` | 过滤元数据（`chunk_id`、`source_id`、`knowledge_id`、`tag_id`、`is_enabled` 等） |

### 3.4 `Search` / `Delete` / `Patch`

- `Search`：按查询/向量/过滤条件检索，返回 `[]RetrieverHit`（`RecordID` + `Score` + `Metadata`）。
- `Delete`：按精确 `record_ids` **或**按 `filter` 逻辑范围删除。
- `Patch`：按 `record_ids` 或 `filter` 定位记录，更新元数据字段（如 `enabled`、`tag_id`）。

**`filter` 是多值过滤**（`map[string][]string`，语义为「字段 ∈ 值列表」的 IN 匹配）。这是硬性要求：宿主内部删除/更新都是按 `chunk_id`、`source_id`、`knowledge_id` 这些 metadata 字段的**多值列表**来过滤的，插件必须支持这种匹配，否则无法正确删除/更新一批记录。

### 3.5 `ScoreSemantics`：分数语义（诚实声明）

插件返回的原始分数，宿主需要知道它是什么语义才能做跨引擎归一化。必须声明为以下三者之一：

| 值 | 含义 |
|---|---|
| `similarity_higher_better` | 相似度，越大越相关（如余弦相似度归一化到 [0,1]） |
| `distance_lower_better` | 距离，越小越相关（如 L2 距离） |
| `rank_only` | 只保证相对排序，分数无绝对意义（如 BM25） |

插件**返回原始分数即可**，归一化、NaN/Inf 处理、跨引擎比较、RRF 融合全部由宿主负责。不要自己在插件里做不可逆的分数变换。

## 4. SDK 侧如何实现：`ServeRetriever`

SDK 提供了 `RetrieverProvider`（静态信息 + `Open` 工厂）和 `RetrieverBackend`（一个 Store 实例的薄接口）。你只需实现 `RetrieverBackend`，SDK 自动处理 gRPC 注册、`store_handle` 生成、会话 Map、并发安全、`PluginControl`。

```go
package main

import (
    "context"
    "os"

    "github.com/Tencent/WeKnora/pkg/pluginapi"
)

// 一个 Store 实例的实现
type myBackend struct {
    // 连接、连接池、collection/index 等，都藏在这里，宿主不关心
}

func (b *myBackend) BatchPut(ctx context.Context, records []pluginapi.RetrieverRecord) error {
    // 把 records 写入后端，record.Embedding 是已算好的向量
    return nil
}

func (b *myBackend) Search(ctx context.Context, req pluginapi.RetrieverSearchRequest) ([]pluginapi.RetrieverHit, error) {
    // req.RetrieverType 区分 "vector" / "keywords"
    return nil, nil
}

func (b *myBackend) Delete(ctx context.Context, recordIDs []string, filter map[string][]string) error {
    // filter 是多值 IN 匹配：chunk_id ∈ ["a","b"] 之类
    return nil
}

func (b *myBackend) Patch(ctx context.Context, recordIDs []string, filter map[string][]string, patch map[string]string) error {
    // 定位记录后更新 patch 里的字段（如 enabled="false"）
    return nil
}

func (b *myBackend) Close(ctx context.Context) error {
    // 释放连接池等资源
    return nil
}

func openBackend(ctx context.Context, config pluginapi.RetrieverStoreConfig) (pluginapi.RetrieverBackend, error) {
    // 用 config.Settings / config.Credentials / config.IndexConfig 建立连接
    return &myBackend{}, nil
}

func main() {
    provider := pluginapi.RetrieverProvider{
        PluginID:       "example.vector-db",
        EngineType:     "example-vector-db",
        Capabilities:   []string{"vector", "filter"},
        ScoreSemantics: "similarity_higher_better",
        Open:           openBackend,
    }
    addr := os.Getenv("WEKNORA_PLUGIN_ADDR")
    if err := pluginapi.ServeRetriever(context.Background(), addr, provider); err != nil {
        panic(err)
    }
}
```

要点：

- `RetrieverProvider.PluginID` 必须与 `plugin.yaml` 的 `id` 一致。
- `RetrieverProvider.EngineType` 必须与 `plugin.yaml` 的 `metadata.engine_type` 一致。
- `Open` 是工厂函数：每次 `OpenStore` 都会调用它创建**一个新的独立 Store 实例**（不同的连接/索引命名空间）。不要在多个 Store 之间共享连接或索引。
- `Handshake` / `Health` 由 SDK 自动实现，`Health` 默认返回 `running`（不聚合 Store 健康）。

## 5. `plugin.yaml` 示例

```yaml
api_version: weknora.plugin/v1
id: example.vector-db
name: Example Vector Database
version: 1.0.0
extension_type: retriever
protocol_version: v1
weknora_version: ">=0.7 <1.0"
entrypoint: ./example-vector-db

capabilities:
  - vector        # 支持向量检索
  - keywords      # 支持关键词检索（可选）
  - filter        # 支持元数据过滤

metadata:
  engine_type: example-vector-db   # 必填：宿主任凭它把 VectorStore 路由到本插件
  score_semantics: similarity_higher_better

config_schema:                     # 用 JSON Schema 描述 OpenStore 时需要的配置
  type: object
  properties:
    settings:
      type: object
      required: [endpoint]
      properties:
        endpoint:
          type: string
        collection_prefix:
          type: string
    credentials:
      type: object
      required: [api_key]
      properties:
        api_key:
          type: string
          secret: true            # 敏感字段，宿主会脱敏

permissions:
  network: allowlist               # 需要连后端数据库，声明网络策略
  allowed_destinations:
    - "example-vectordb.com"
```

### 必填校验

- `metadata.engine_type` **不能为空**——`RegisterExternalRetriever` 明确要求 retriever 插件声明引擎类型，否则无法装载。

### 能力声明（capabilities）原则

> **插件不需要能力齐全，但声明支持的能力必须真实可用。**

| 能力 | 含义 | 是否核心 |
|---|---|---|
| `vector` | 支持向量检索 | 核心 |
| `keywords` | 支持关键词（BM25 等）检索 | 核心 |
| `filter` | 支持元数据过滤 | 核心 |
| `copy_indices` | 支持直接复制索引（高级，暂未开放） | 可选 |
| `staged_rebuild` | 支持分阶段重建（高级） | 可选 |

宿主根据能力决定行为：例如插件只声明 `vector`，宿主就不会让它做关键词检索；声明了 `copy_indices` 才调用复制能力，否则宿主走「重新计算 embedding」的降级路径。

## 6. 关键语义约束（务必遵守）

1. **embedding 强类型**：`Embedding` 是 `[]float32`，由宿主算好传入。插件**不要**自己重新计算 embedding，也不要把向量塞进 `Metadata`。
2. **filter 是多值 IN 匹配**：`map[string][]string`，字段 ∈ 值列表。这是宿主删除/更新批量记录的硬性前提。
3. **幂等写入**：同一个 `RecordID` 重复写入不应产生重复记录（建议内部按 `RecordID` 做 upsert 语义）。
4. **维度不匹配要显式报错**：向量维度与已建索引不符时，必须返回错误，不能静默写入导致检索全乱。
5. **删除幂等**：删除不存在的记录不应报错。
6. **禁用记录不可被检索**：`Patch` 把 `enabled=false` 后，该记录必须立即从检索结果中排除（不能只在 metadata 里改个字段、检索时却不过滤）。
7. **分数语义诚实**：`score_semantics` 必须真实反映返回分数的含义。
8. **`Health` 不聚合 Store 健康**：见第 2 节的警告。

## 7. 校验与验证

Retriever 是五类扩展点里唯一「有状态」的，验证比 Parser/Search 复杂。建议按以下顺序自检：

1. 用 SDK 的 `RetrieverProvider` 实现一个**内存 Map 后端**（`recordID → {content, vector, metadata}`），跑通全流程。
2. 验证清单：
   - `OpenStore` 创建独立会话，两个 Store 数据互不影响；
   - `BatchPut` 写入后，`Search` 能返回正确 TopK；
   - 重复写同一 `RecordID` 不产生重复记录；
   - `Delete`（按 `chunk_id` 多值 filter）后记录不可检索；
   - `Patch(enabled=false)` 后记录不可检索；
   - 插件进程重启后，宿主能重新 `OpenStore` 恢复会话。
3. 建议用 Docker 隔离（`--network` 受限）验证网络策略生效。

## 8. 宿主侧如何被使用

宿主在「创建 VectorStore」时，按 `metadata.engine_type` 找到插件，`OpenStore` 得到 `store_handle`，再创建一个 `GRPCRetrieverRepository`（实现内部 `RetrieveEngineRepository`），包装进标准的 `KVHybridRetrieveEngine` 并注册到 `RetrieveEngineRegistry`。

关键点：

- **embedding 由宿主算**（`KeywordsVectorHybridRetrieveEngineService` 在调用 Repository 前批量算好向量，放进 `params["embedding"]`），插件只收到「已算好的向量」。
- **融合、权限过滤、租户隔离、重建索引编排全部由宿主处理**，插件不参与。
- 外部插件走 gRPC 路径；内置后端（PostgreSQL/Qdrant/Milvus 等）仍走进程内 SDK 路径，性能不受影响。

## 9. 与宿主的关系（边界）

Retriever 插件**只负责「向量的写入 / 检索 / 删除 / 元数据更新」**，不负责：

- **计算 embedding**——宿主算好传入；
- **RRF 融合、跨引擎归一化**——宿主处理；
- **权限过滤、租户隔离**——宿主处理；
- **重建索引的任务编排、进度跟踪**——宿主处理；
- **Collection/维度分表、连接池、后端重试、事务**——这是插件**自己内部**的事，宿主不关心，只要协议输出正确。

一句话：**插件管「具体后端怎么存怎么查」，宿主管「算好向量、编排流程、融合结果」**。插件内部可以自由使用任何数据库、任何索引结构、任何维度策略，只要守住上面的协议契约。
