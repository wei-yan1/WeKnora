# WeKnora 插件前端 UI 开发提示词（给 AI 编程助手）

> 用途：把这段提示词 + 一个插件的 `plugin.yaml`（或插件元数据）发给 AI 编程助手，
> 让它为**非数据源**的扩展点插件（WebSearch / Model / Parser / Retriever）生成对应的前端 UI。
>
> 核心目标：让这些扩展点的前端也像「数据源插件」一样——**识别到一个插件，就自动渲染出它的配置 UI，无需在宿主前端里为每个插件手写代码**。

---

## 一、背景（让 AI 先理解现状）

WeKnora（Vue 3 + TypeScript + TDesign `tdesign-vue-next` + vue-i18n + Less，前端源码在 `frontend/src/`）有一个插件框架，五类扩展点：`datasource` / `parser` / `search` / `model` / `retriever`。

目前**数据源（datasource）和网络搜索（search）插件**已实现"插件元数据 → 自动生成配置表单"的前端范式（search 以 datasource 为样板，见 §一.2），其余三类（model / parser / retriever）的前端仍是**硬编码**或**未接入**。

### 数据源插件的既有范式（必须读懂并复用，这是样板）

参考文件：`frontend/src/views/knowledge/settings/DataSourceEditorDialog.vue`

它的做法（核心 3 步）：

1. **拉取插件元数据**：调用 `getConnectorTypes()`（对应后端 `GET /api/v1/datasource/types`），得到外部插件列表，每个含 `type` / `name` / `description` / `icon` / `config_schema` 等字段（见 `frontend/src/api/datasource/index.ts` 的 `ConnectorMeta` 类型）。

2. **合并内置 + 外部**：内置连接器是 `connectorDefs`（硬编码数组），外部插件从服务端元数据映射出来，标记 `external: true`，两者合并成 `availableConnectors`：

```ts
const availableConnectors = computed<ConnectorDef[]>(() => {
  const builtinTypes = new Set(connectorDefs.value.map(d => d.type))
  const external: ConnectorDef[] = serverMetadata.value
    .filter(m => m.external || !builtinTypes.has(m.type))
    .map(m => ({ type: m.type, external: true, name: m.name, description: m.description, icon: m.icon, configSchema: m.config_schema }))
  return [...connectorDefs.value, ...external]
})
```

3. **动态渲染 `config_schema`**：`parseSchemaProperties()` 把插件 `config_schema` 里的 `properties.settings`（基本设置）和 `properties.credentials`（凭证，`secret: true` 用密码框）解析成字段列表，前端按字段类型（`string` / `string[]`(array) / `boolean` / `number`）渲染 `t-input` / `t-switch` / `t-input-number`，敏感字段带锁头图标。**凭证编辑模式**下已保存的敏感字段只显示「已配置」，不回显明文，替换走 `/credentials` 子资源。

关键约束（照抄这份范式时必须遵守）：
- `secret: true` 字段 → 密码框 + 锁头 `prefix-icon`，编辑时不回显、空值不覆盖原凭证；
- 凭证与 settings 分两个区渲染（`properties.credentials` vs `properties.settings`）；
- `url` 类型的字段后端已做 SSRF 校验，前端无需额外处理；
- 外部插件不能与内置类型重名（后端 `Register` 会 fail-closed 拒绝重复 ID，前端只是展示）。

### 网络搜索已实现的范式（第二个样板，本次实战沉淀）

参考文件：`frontend/src/views/settings/WebSearchSettings.vue` + 后端 `internal/plugin/web_search_registration.go`。

它与数据源范式的差异（照抄时必须注意）：

1. **扩展参数用 `config_schema` 平铺声明**（无 settings/credentials 分区——web search 只有一个凭证 `api_key`，已由宿主专用表单区管理）：

```yaml
config:
  - key: api_key            # 保留 key：只驱动能力标志，不产生自定义输入
    type: secret
    required: true
config_schema:
  type: object
  properties:
    search_depth:
      type: string
      title: Search Depth
      description: basic 或 advanced
      enum: [basic, advanced]
      default: basic
    page_size:
      type: integer
      title: Page Size
      default: 5
```

2. **四个保留 key 的行为语义**（宿主 `webSearchReservedKeys`，见 `web_search_registration.go`）：

- `api_key` / `engine_id` / `base_url` / `proxy_url` 只驱动能力标志（`RequiresAPIKey` / `RequiresBaseURL` / `SupportsProxy` 等），**由宿主内置表单区渲染，不会作为自定义字段再次渲染**；
- `required: true` 决定对应输入框**显示**，`required: false` 则**完全不显示**——框架没有"可选显示"中间态。

3. **config_schema 字段 → 控件映射**（宿主 `webSearchConfigFieldsFromSchema` + 前端 config_fields v-for 已实现）：

| schema 声明 | 前端控件 |
|---|---|
| 有 `enum` | 下拉框 |
| `type: string` | 文本输入（`secret: true` 为密码框） |
| `type: integer` / `number` | 数字输入框 |
| `type: boolean` | 开关 |
| `type: array` / `string[]` | 逗号分隔文本输入 |

4. **`ExtraConfig` 值统一为字符串**：前端保存时 `String(value)`、编辑时按类型反序列化（`WebSearchSettings.vue` 的 `deserializeConfigValue` / `isConfigFieldValueEmpty`）；插件侧自行解析。**扩展字段明文存储，敏感凭证必须用保留 key `api_key`**。

5. **图标**：manifest 的 `metadata.icon`（相对文件名或 http(s) URL），宿主流式服务；**图标端点需加入免认证白名单**（`internal/middleware/auth.go` 的 `noAuthAPI`，前缀如 `/api/v1/web-search-providers/icon/*`）——浏览器 `<img>` 无法携带认证头。

6. **网络守卫边界**：插件出站用 SDK 的 `NewPluginHTTPClient()`（依赖宿主注入 `WEKNORA_PLUGIN_NETWORK_POLICY` / `ALLOWLIST` 环境变量）。**内网/私有地址会被反 SSRF 拦截**（`internal/pluginapi/guarded_client.go` 的 `isForbiddenIP` 含 `IsPrivate()`）——对接内网服务的插件目前无法出站，设计此类插件前先确认是否需要改守卫。

7. **调试**：宿主设置页「插件服务」的「刷新插件」按钮（`POST /api/v1/plugins/rescan`）可增量装载新插件，无需重启宿主。

---

## 二、你的任务（按扩展点选一个或多个）

为以下扩展点插件补齐前端 UI，**目标与数据源一致**：识别到一个插件就自动渲染其配置 UI，不新增"每插件一份硬编码"。

| 扩展点 | extension_type | 前端现状 | 后端元数据来源 |
|---|---|---|---|
| 网络搜索 | `search` | ✅ **已实现**（样板见 §一.2）：`provider` 联合类型已放宽为 `string`，config_fields 已全类型渲染 | `GET /api/v1/web-search-providers/types`（已有） |
| 模型 | `model` | `ModelEditorDialog.vue` 有 `source` 字段但无 `plugin` source | 待确认/补齐后端返回外部 model provider 的元数据 |
| 文档解析 | `parser` | `ParserEngineSettings.vue` 引擎列表 | 待确认后端是否返回外部 parser 元数据 |
| 检索引擎 | `retriever` | 向量库后端列表 | 待确认后端是否返回外部 retriever 元数据 |

**search 已落地（本次实战完成），下一步按 model → parser → retriever 推进**，每类的先补项：

- **model**：① 确认/补齐宿主返回外部 model provider 元数据的接口；② `ModelEditorDialog.vue` 加 `source: plugin`，照 §一.2 的动态注入渲染。
- **parser**：① 确认宿主是否暴露外部 parser 元数据；② `ParserEngineSettings.vue` 引擎列表动态化。
- **retriever**：① 同上确认元数据接口；② 向量库后端列表动态化。

---

## 三、硬性要求（AI 必须遵守）

### 3.1 技术栈与风格
- Vue 3 `<script setup lang="ts">`，TypeScript，TDesign 组件（`tdesign-vue-next`），Less `<style scoped lang="less">`。
- 文案一律走 `vue-i18n`（`useI18n()` 的 `t()`），**不要在模板里写死中文/英文字符串**；新增的文案 key 要加进 `frontend/src/i18n/locales/zh-CN.ts` 和 `en-US.ts`（至少这两个语言，若有对应文件也补 `ru-RU.ts` / `ko-KR.ts`）。
- 复用现有公共组件：`SettingDrawer`（`@/components/settings/SettingDrawer.vue`）、`SettingCard` 等；不要新造一套风格。
- 视觉上对齐 `DataSourceEditorDialog.vue` / `WebSearchSettings.vue` / `ModelEditorDialog.vue` 的既有抽屉与卡片样式。

### 3.2 功能语义（照数据源范式）
- **动态渲染**：外部插件的配置字段必须由服务端返回的 `config_schema` / 元数据驱动，不得在 Vue 组件里为某个具体插件写 `if (type === 'xxx')` 分支。
- **内置与外部共存**：内置类型保留其现有专用表单/标签/logo，外部插件作为新增条目动态注入；两者按 `type`/`provider` 区分，不互相覆盖。
- **凭证安全**：`secret: true` 字段用密码框、编辑时不回显明文、替换走 `/credentials` 子资源、空值不覆盖。
- **测试连接**：若扩展点有"测试连接"语义（数据源有 `validateCredentials`，web_search 有 `testWebSearchProvider`），外部插件也要能走通同一套测试流程。
- **保留 key 不重复渲染**：扩展点的保留 key（web search 的 `api_key` / `engine_id` / `base_url` / `proxy_url`）已有宿主专用表单区，**不要**为它们再写自定义输入分支；渲染自定义字段时遇到保留 key 直接跳过，避免产生 label-only 空壳。
- **扩展字段值统一字符串**：前端表单绑定 typed 值（boolean / number），但**保存时 `String(value)`、编辑时按 `field.type` 反序列化**（参考 `WebSearchSettings.vue` 的 `deserializeConfigValue` / `isConfigFieldValueEmpty`，后者正确处理了 `false` / `0` 的 falsy 误判）。

### 3.3 不要做的事
- 不要改动后端 Go 代码（除非明确发现后端缺元数据接口，此时**先报告**，不要擅自改后端）。
- 不要为单个插件写死专属 UI 分支。
- 不要引入新的 UI 框架或第三方依赖。
- 不要删除/破坏现有的内置 provider 的展示逻辑。
- 不要对保留 key 写专属输入 UI（宿主已处理，重复渲染会产生 label-only 空壳）。

---

## 四、可复用的样板文件（AI 应主动阅读）

- 动态表单范式：`frontend/src/views/knowledge/settings/DataSourceEditorDialog.vue`
- **（已完成样板）网络搜索设置页：** `frontend/src/views/settings/WebSearchSettings.vue`——config_fields 全类型渲染（secret / number / boolean / array / select）+ `deserializeConfigValue` / `isConfigFieldValueEmpty` / `providerConfigDefaults` 序列化约定
- **（已完成样板）网络搜索后端 schema 转换：** `internal/plugin/web_search_registration.go`——`webSearchConfigFieldsFromSchema` / `webSearchConfigFieldsFromLegacy` / `webSearchReservedKeys`
- 网络搜索 API 客户端：`frontend/src/api/web-search-provider.ts`
- 网络搜索协议文档（含 config_schema 章节、保留 key 语义、图标、网络守卫）：`docs/plugin-development-websearch.md`
- 网络守卫实现（了解 allowlist / 反 SSRF 边界）：`internal/pluginapi/guarded_client.go`
- 模型编辑器（要改造的对象）：`frontend/src/components/ModelEditorDialog.vue`
- 数据源 API 客户端（元数据类型参考）：`frontend/src/api/datasource/index.ts`
- 公共抽屉：`frontend/src/components/settings/SettingDrawer.vue`

---

## 五、交付与验收标准

完成后，AI 应自检并报告：

1. **能跑**：`pnpm install` 后 `pnpm build`（或项目的类型检查/构建命令）无报错，`read_lints` 无新增 error。
2. **动态注入**：在插件目录（如 `WEKNORA_PLUGIN_DIR_SEARCH`）放入一个外部 `search` 插件并启动宿主后，前端 provider 类型列表里**自动出现**该插件类型，无需改前端代码。
3. **配置表单**：选中该外部插件后，按其 `config_schema` 渲染出正确的 settings + credentials 字段，secret 字段为密码框且不显示明文。
4. **不与内置冲突**：内置的 `tavily` / `bing` / `google` 等仍正常展示与工作，外部插件用独立 `provider_type`（如 `tavily-plugin`）并存。
5. **i18n 完整**：新增文案在 `zh-CN` 与 `en-US` 都有，无遗漏的硬编码文案。

---

## 六、给 AI 的执行建议（可选的步骤拆解）

1. 先读 §四 样板文件，理解两个范式：数据源（§一.1，settings/credentials 分区 + 凭证子资源）与 search（§一.2，平铺 properties + 保留 key 语义）。
2. **先抽公共再接入**（避免样板代码继续三处重复）：
   - 后端：把 `config_schema` 的 properties / title / description / enum / default / required 解析抽成公共函数（放 `internal/plugin`），各扩展点只写"转换目标结构"那一小段（参照 `webSearchConfigFieldsFromSchema`）。
   - 前端：把 string / secret / number / boolean / array / select 渲染模板抽成公共组件（如 `SchemaFieldInput.vue`），各扩展点设置页直接引用。
3. 按 model → parser → retriever 逐个接入，每类**先补"宿主 → 前端"的元数据通道**（后端接口 + 前端入口），再复用公共渲染。
4. 补 i18n key，跑构建 + lint 自检，按 §五 逐条报告验收结果。
