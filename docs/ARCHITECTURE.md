# Router 架构分析

## 结论

当前 Router 已经具备“一个入口、多个上游 Provider、按模型能力和运行时约束选路”的骨架，但它现在的核心决策单位仍然是“逻辑模型 + Provider binding”，不是完整的“Provider + Credential + Wire capability”候选。

因此，增加 Codex OAuth 并不只是再注册一个 OpenAI API key。Codex OAuth 是一种带 `ChatGPT-Account-ID` 的订阅凭证，只能发往 ChatGPT Codex backend 的原生 Responses API；它必须成为独立的受约束 Provider 候选，不能被当成普通 `api.openai.com` 凭证，也不能被转发给其他厂商。

这个结论直接解释了此前的几类错误：

- `No provider keys available for any deployed model`：路由器找不到同时满足“模型、Provider、凭证、部署策略”的候选。
- `leave no eligible candidates`：安装级排除或 gateway-exclusive 规则把候选池过滤空了。
- `401 invalid_key`：Codex OAuth bearer 被送到了不接受它的 endpoint，或者被一个无关的 OpenAI gateway key 抢走了。
- `502 http://127.0.0.1:8088/v1/responses`：本地服务虽能收到请求，但上游选路或凭证/协议派发失败，客户端只看到网关错误。

## 分析基准和架构图

分析基于当前工作树；Archify 图谱的源码证据固定在仓库 revision `09912e87b13b21a8df48bd90049507e4f16bcffe`，以保证图中每条 source reference 可复核。

- [交互式架构图](router-architecture.html)
- [Archify 图谱源文件](router-architecture.archify.json)
- [Archify 视觉检查回执](router-architecture.visual-check.json)

图谱通过了 Archify showcase 的 9 项结构检查，并在 1440×900、1600×1000、1920×1080、2048×1320 的明暗主题视口中通过 containment 检查。

## 主要组件和职责

| 组件 | 当前职责 | 不应承担的职责 |
| --- | --- | --- |
| `internal/api/*`、`internal/server` | 接收 OpenAI / Anthropic / Gemini 请求，做 HTTP 映射和超时 | 直接访问 SQL、选择具体 Provider |
| `internal/auth` | Router key、安装身份、BYOK 元数据和密钥解析 | 决定模型质量或执行上游 HTTP |
| `internal/proxy` | 解析请求、推导约束、调用 Router、翻译协议、派发、重试和记录用量 | 把某一个 OAuth 实现硬编码成通用路由规则 |
| `internal/router` | 定义 `Request`、`Decision`、`Router`，承载请求特征和决策结果 | 持有 HTTP client 或读取数据库 |
| `internal/router/cluster` | 对符合约束的逻辑模型做候选评分 | 绕过凭证和协议约束直接选模型 |
| `internal/router/catalog` | 维护逻辑模型、Provider binding、上游模型 ID、价格和能力数据 | 保存运行时秘密 |
| `internal/providers/*` | Provider client、认证、上游 wire format、SSE 和错误分类 | 读取安装配置或反向调用 API 层 |
| `internal/translate` | OpenAI、Anthropic、Gemini 之间的纯协议转换 | 决定是否允许某个秘密发往某个 Provider |
| `internal/postgres` | SQLC 数据访问、BYOK 和 session pin 等持久化适配 | 组合业务流程 |
| `cmd/router/main.go` | 唯一的 composition root：构造并注入全部具体实现 | 承载请求级业务逻辑 |

这套边界是项目当前最有价值的资产：外圈是 HTTP 和适配器，中圈是 `proxy` 编排，内圈是 Router 的值类型、策略和纯转换；依赖方向向内，具体实现只在 composition root 组装。

## 一次请求的实际生命周期

1. 客户端向 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 或 Gemini endpoint 发请求。
2. middleware 先建立 request correlation，再做 Router key、安装策略和请求级身份绑定。
3. API handler 将外部协议解析成 proxy 能消费的 envelope。
4. `internal/proxy` 提取模型、工具、图片、上下文长度、reasoning、流式和协议端点等硬约束，生成 `router.Request`。
5. Router 根据 catalog、cluster artifact、session pin、安装级排除、允许模型和 Provider 可用性筛选候选，并返回 `router.Decision{Model, Provider}`。
6. proxy 按最终 Provider 解析凭证；必要时通过 `internal/translate` 将客户端协议转换为上游协议。
7. Provider adapter 添加该 Provider 所需的认证和 headers，执行 HTTP/SSE 请求，分类上游错误并抽取 usage。
8. proxy 将响应转换回客户端协议，并写入 request logs、OTel、analytics、session pin 和 billing 所需的事实。

关键点是第 5 和第 6 步不能颠倒：不能先选一个逻辑模型，再随意找一个能发请求的 key；必须将“这个模型的这个 Provider binding 是否有合法凭证和 wire path”作为候选资格的一部分。

## 当前的凭证模型

当前系统至少存在三类凭证，它们的信任边界不同：

1. Router key：用于识别安装/调用方，让 Router 允许请求进入。
2. BYOK 或 deployment key：对应具体 Provider，能够代表安装方调用某个厂商或 gateway。
3. 客户端 OAuth passthrough：例如 Claude OAuth 或 Codex ChatGPT OAuth，代表用户自己的订阅，只能进入它所属的官方 backend。

当前 Codex 路径已经在 OpenAI client 内实现了必要的协议事实：

- 目标是 `https://chatgpt.com/backend-api/codex`，不是 `https://api.openai.com`。
- 只走 Responses API。
- bearer 必须和 `ChatGPT-Account-ID` 配对。
- Codex backend 与公开 OpenAI Responses API 支持的字段并不完全相同，需要按 endpoint 清理或重写字段。
- OAuth 只对显式支持的 Codex 模型族有意义；对基础设施模型不能把该 bearer 当作普通 API key。

这说明 OAuth 处理应当由 Provider adapter 和 credential resolver 共同保证，而不是通过“给 OpenAI provider 一个空 deployment key”来模拟。

## 当前架构的优势

- `ProviderFamilies` 把 Provider 名称与 wire family 分开，新增 OpenAI-compatible 上游时不必复制一套翻译分支。
- `catalog.ProviderBinding` 已经表达了一个逻辑模型可以绑定多个 Provider，并能携带上游模型 ID、价格和上下文窗口。
- `router.Request` 已经包含工具、图片、结构化输出、reasoning replay、prompt cache 等候选约束的入口。
- `proxy` 已经有 session pin、failover、usage、handover 和 translation 编排能力。
- composition root 集中在 `cmd/router/main.go`，适合把新的 Provider、credential source 和 feature flag 作为显式依赖接入。
- 日志规范明确禁止输出原始 token，并且 request correlation 可以把一次请求的路由和上游错误串起来。

## 目前的结构性缺口

### 1. Provider、模型和凭证仍然是分离的平行信息

`Decision` 主要返回 `Model` 和 `Provider`，而凭证在 proxy 的后续阶段解析。对于普通 deployment key 这可以工作；对于 Codex OAuth、Claude OAuth、未来的其他订阅型 Provider，后续解析可能改变候选的真实可用性。

目标应当是让候选在进入 scorer 前就带有 credential requirement 和 wire capability，或者至少让 scorer 使用一个只读的 `CredentialAvailability` 接口做硬过滤。

### 2. Codex OAuth 已有独立 Provider 边界，但仍复用 OpenAI Responses wire 实现

当前工作树已注册 `ProviderCodex` 和 `internal/providers/codex` adapter。adapter 复用 OpenAI Responses 的底层 HTTP/SSE 实现，但凭证和 provider identity 分开，避免把 OAuth bearer 当作普通 OpenAI API key。普通 cluster 在 boot 时排除 credential-only provider，native Codex request 在 scorer 前固定到 Codex binding。P3 已接入非秘密的 model/endpoint 级 `CredentialBinding`，cluster 与 policy resolver 会在 binding resolution 时应用它；空候选错误会携带不含秘密的模型、Provider、endpoint、credential source 和 exclusion reason；最终决策也附带 `RoutingCandidate`/`DispatchPlan`。P5 已让实际 adapter 和 failover dispatcher 消费该 plan，旧字段仅作为兼容和遥测保留。

### 3. Prompt 路由还没有独立的意图层

当前 `PromptText` 和 `ConversationMessages` 已可供策略使用；意图事实层已新增纯本地、可解释的标签（coding、agentic、vision、long_context、deep_reasoning、summarization），并通过策略请求传递。标签目前只提供偏好输入，不绕过能力/凭证过滤；直接让一个 LLM 再调用 Router 判断 Provider 仍会引入额外延迟、递归调用和隐私边界问题。

首版应先用确定性的请求能力和轻量分类标签完成路由；需要模型语义分类时，单独作为可关闭的 classifier，不得成为唯一可用路径。

### 4. 任意 Provider 不是免费的抽象

Router 可以支持多个 Provider，但每个 Provider 至少需要：认证方式、wire family、流式语义、工具/图片/reasoning 能力、模型别名、错误分类、usage 计费和健康状态。不能只添加一个 URL 就宣称任意厂商都已支持。

## 目标架构原则

目标形态是：

```text
客户端请求
  -> 入口协议解析
  -> 请求能力/意图事实
  -> 合法候选生成
       (logical model, provider, credential source, wire path)
  -> Router 评分
  -> Provider-specific credential resolver
  -> translation 或 native passthrough
  -> 上游 Provider
```

必须遵守以下不变量：

- Codex OAuth token 永远只发往 Codex backend，不发送给 `api.openai.com`、gateway 或其他厂商。
- 一个候选只有在模型能力、Provider wire family、凭证来源和 deployment policy 全部满足时才进入 scorer。
- 失败重试只能在授权的候选集合内切换；不能因为 OAuth 失败就静默花费另一个 Provider 的预算。
- 明确的 force-model / native-only 请求优先于价格或质量偏好，但仍不能突破凭证和物理协议约束。
- Router key、BYOK secret、OAuth access token、account ID 的存储、日志和传输路径必须分别可审计。
- 不把用户 OAuth 视为 Router 的平台预算；billing、quota、usage attribution 必须按 credential source 区分。

## 相关源码入口

- [composition root](../cmd/router/main.go)
- [HTTP route registration](../internal/server/server.go)
- [routing request and decision types](../internal/router/router.go)
- [model catalog and provider bindings](../internal/router/catalog/catalog.go)
- [provider families and dispatch contract](../internal/providers/provider.go)
- [proxy orchestration](../internal/proxy/service.go)
- [OpenAI/Codex protocol adapter](../internal/providers/openai/client.go)
- [Codex provider boundary](../internal/providers/codex/client.go)
- [local Codex OAuth loader](../cmd/router/codex_auth.go)

下一步实施顺序见 [多 Provider 路由实现计划](MULTI_PROVIDER_ROUTING_PLAN.md)。
