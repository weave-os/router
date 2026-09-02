# 多 Provider / Codex OAuth 路由实现计划

## 目标

把 Router 从“逻辑模型绑定到若干已注册 Provider”升级成“按请求能力、路由意图、可用凭证和协议能力选择 Provider + model”的中转站，同时保证 Codex OAuth 是一个受约束的独立来源。

用户侧最终可以继续使用一个 OpenAI-compatible 入口；Router 在内部完成：

```text
prompt + session + client credential
  -> capability / intent facts
  -> eligible provider-model candidates
  -> quality / price / latency policy
  -> provider-specific auth and wire path
```

## 当前进度

第一阶段（credential provenance contract）已完成：`router.Decision` 现在可以携带不含秘密的 `CredentialSource`，proxy 在最终 Provider/Model 确定并完成凭证解析后标注实际来源；Codex native passthrough 明确标记为 `codex_oauth`。相关回归覆盖 deployment key、BYOK、client API key、Claude OAuth、Codex OAuth 和 gateway 冲突保护。

第二阶段（Codex 独立 Provider 边界）已完成首个可运行切片：新增 `ProviderCodex`、credential-only provider 标记、Codex catalog bindings 和独立 adapter；普通 cluster 不会把它当作部署级候选，native Responses + 合法 Codex OAuth 会在 scorer 前固定到 `codex`。保留旧 `openai` fallback 仅用于兼容旧的测试/组合，生产 composition root 会优先使用 `codex` adapter。完整的候选生成器和语义意图路由仍属于 P3/P4。

这一步先建立可审计的信任边界，已经完成 Codex 的独立 Provider 边界，但没有打开语义 prompt 分类；后者属于 P4，避免在凭证候选模型尚未稳定时引入跨 Provider 误路由。

第三阶段（credential-aware candidate resolver）已完成：`router.Request` 携带非秘密的 `CredentialBinding`，cluster 和 policy resolver 在绑定解析时同时检查模型与 endpoint 范围；proxy 为 Codex OAuth 生成 Responses-only、模型级候选约束，并在生产组合中停止把 OAuth-only 请求加入 legacy OpenAI provider。候选池为空时现在返回带安全 `CandidateDiagnostic` 的 typed error，policy resolver 也保留 Provider、endpoint 和 credential source 诊断；日志可以直接区分 credential missing、credential scope、model policy 等原因。最终决策现在附带 `RoutingCandidate`/`DispatchPlan`，冻结 Provider、upstream model、凭据来源、endpoint、native/translated 模式和 failover 权限。

## 非目标

- 不把任意 OAuth token 当成通用 API key。
- 不把 Codex OAuth 请求静默转到其他厂商或 gateway。
- 不在首版支持“只填一个 URL 就自动支持所有 Provider”；未知 Provider 仍需显式 adapter 和能力声明。
- 不用第二个 LLM 作为唯一的路由器。
- 不改变现有 force-model、session pin、excluded-models 和 managed/selfhosted 的安全语义。

## 目标领域模型

先在现有 `internal/router`、`internal/providers` 和 `internal/auth` 边界中扩展类型，避免立即创建新的横向 service locator。建议最终形成以下几个概念：

| 概念 | 关键字段 | 作用 |
| --- | --- | --- |
| `ProviderRegistration` | provider、family、adapter、health、deployment mode | 描述一个可派发的 Provider |
| `CredentialBinding` | source、provider、scope、expires_at、account_ref、billing_mode | 描述凭证属于谁、能发往哪里、如何计费 |
| `ModelCapability` | model、context、tools、images、reasoning、structured_output、native_only | 描述模型可承载什么请求 |
| `RoutingCandidate` | logical model、provider、upstream ID、credential source、wire path | Router 真正评分的最小候选单位 |
| `RoutingIntent` | coding、reasoning、vision、long_context、fast、economy 等 | 将 prompt/请求事实归一为策略信号 |
| `DispatchPlan` | provider、model、credential、translation path、fallback policy | 将决策冻结成可审计的派发计划 |

其中 `CredentialBinding` 和 `RoutingCandidate` 是解决当前问题的关键：决策不能只返回一个 model string，再在 proxy 里猜凭证。

## 分阶段计划

### P0：基线、可观测性和兼容性契约

状态：当前代码已经有大部分基础设施；将其固化为实施前的回归基线。

工作项：

- 固定现有入口：OpenAI Responses、Chat Completions、Anthropic Messages、Gemini。
- 为每个请求记录 `requested_model`、候选数量、排除原因、最终 `provider/model`、credential source 和 upstream status；不记录原始秘密和完整 prompt。
- 将 `ErrNoEligibleProvider`、`ErrProviderNotConfigured`、credential unavailable、translation unsupported 区分开。
- 保留现有 8088 本地运行方式和 `/health`、`/readyz` 验证路径。
- 增加 feature flag，使新候选系统可以 shadow 计算但不改变真实派发。

完成标准：现有 Codex CLI、普通 OpenAI key、Anthropic OAuth/BYOK 和 gateway 回归测试全部通过；线上日志能回答“候选为何被排除”。

### P1：把 Provider 与凭证能力显式建模

建议修改范围：`internal/router`、`internal/providers`、`internal/auth`、`internal/proxy`。

工作项：

- 增加 credential source 类型：deployment key、installation BYOK、client API key、client OAuth、Codex subscription OAuth。
- 增加只读的 credential availability resolver；它只回答“某 Provider/模型能否使用某种凭证”，不返回明文秘密。
- 将 `ProviderFamilies`、catalog binding 和 credential requirement 组合成统一的候选描述。
- 保持明文秘密只在 request lifetime 的 context/adapter 内存中出现。
- 将 `Decision` 扩展为可表达 credential source 和 dispatch mode，但保留旧字段，分阶段兼容现有 telemetry 和 API。

完成标准：对于同一个逻辑模型，Router 能区分“OpenAI deployment key 候选”“OpenAI BYOK 候选”“Codex OAuth 候选”，并能在没有合法凭证时给出明确排除原因。

### P2：Codex OAuth 独立 Provider 化

建议修改范围：`internal/providers/codex/`（或从现有 OpenAI adapter 中抽出共享代码）、`internal/router/catalog/`、`cmd/router/main.go`、`internal/proxy/`。

工作项：

- 新增明确的 Provider 常量，例如 `ProviderCodex`，并注册其 translation family、adapter 和 dispatch validation。
- 将 `access_token + ChatGPT-Account-ID` 作为不可拆分的 credential pair。
- 将 Codex backend base URL、Responses-only、headers、字段清理、OAuth 可覆盖模型族写成 adapter 的能力契约。
- catalog 中为 Codex 模型创建明确 binding，禁止同一 binding 被普通 OpenAI endpoint 复用。
- 本地 loader 只负责读取 Codex CLI session；不把 token 写入 Router 配置、日志、数据库或 `X-Weave-Router-Key`。
- 认证刷新、过期和“本地 auth.json 不存在”的错误要变成可区分的 credential state，而不是空 key。

完成标准：

- Codex OAuth 请求只命中 Codex backend，并带正确 account header。
- OpenAI gateway、普通 OpenAI key 与 Codex OAuth 同时存在时，Codex OAuth 仍选择 Codex binding。
- Codex OAuth 不会被选去处理不在覆盖范围内的基础设施模型。
- Codex 上游 401/403 不会触发未经授权的跨 Provider 付费 failover。

### P3：候选生成先于 scorer

当前状态：已完成。除保留旧字段兼容外，实际派发器已消费 `DispatchPlan`：上游 adapter 使用计划中的 provider-specific upstream model，failover 按计划权限执行，跨 Provider / baseline / sibling rescue 会重建对应 plan。

建议修改范围：`internal/proxy/service.go`、`internal/router/router.go`、`internal/router/catalog/`、`internal/router/cluster/`。

工作项：

- 从请求 envelope 生成统一 `TranslationRequirements` 和 `RoutingIntent`。
- 生成候选时同时检查：模型能力、上下文窗口、工具/图片/文件、reasoning、协议 endpoint、credential availability、安装排除和 deployment mode。
- scorer 只对 eligible candidates 评分，不再通过 model 名称事后推断 Provider。
- 候选排除保留结构化原因，例如 `credential_missing`、`native_only`、`context_overflow`、`unsupported_tools`、`policy_excluded`。
- session pin 保存 `model + provider + dispatch mode`，避免下一轮重新解析出不兼容的 Provider。

完成标准：日志中的“eligible pool”与实际可派发候选一致；不会再出现列表看起来有模型、但所有候选在 dispatch 时才发现没有 key 的情况。

### P4：加入 prompt/请求意图路由

先采用低风险、可解释的两层策略：

1. 硬能力事实：是否有 tools/images/files、上下文长度、Responses/Anthropic/Gemini endpoint、structured output、reasoning 等。
2. 轻量意图标签：coding、agentic、vision、long_context、deep_reasoning、fast、economy、summarization。

工作项：

- 由 ingress 从结构化请求和有限的 prompt features 产生标签；默认不持久化原文。
- 将标签作为 routing preference，而不是绕过 capability filter 的硬命令。
- 可选 classifier 必须有超时、采样率、fallback 和脱敏边界；classifier 不可用时仍能正常路由。
- 将“prompt 选 Provider”的解释写入 debug/analytics：触发的标签、权重、最终候选。

完成标准：同一个入口可将不同类型请求导向不同的合适候选；标签失效或 classifier 超时不会导致 502。

当前状态：已完成。Router 在本地、无额外模型调用的情况下，从有限 prompt 摘要和结构化请求事实生成稳定的 `coding`、`agentic`、`vision`、`long_context`、`deep_reasoning`、`summarization` 标签，并把标签传给策略 sidecar。catalog 对明确支持的模型声明 intent preference，cluster 仅对已经通过能力、策略和凭证过滤的候选加小幅软偏置；cluster/sidecar 的路由元数据保留标签但不保存 prompt 原文。未声明偏好的模型和未知标签都是 no-op。

### P5：统一 DispatchPlan 和跨协议派发

建议修改范围：`internal/proxy`、`internal/translate`、`internal/providers/*`。

工作项：

- Router 返回不可变的 `DispatchPlan`，proxy 不再二次猜测 endpoint、credential 或 upstream model ID。
- 每个 adapter 声明 native path 和 translation path；`NativeOnly` 候选不得经过有损转换。
- 统一错误分类：认证失败、限流、上游不可用、协议不支持、响应流中断。
- failover 只在候选系统已经判定为合法且授权的候选中进行；OAuth 和平台 key 的 billing/source 规则显式控制是否允许切换。
- 保留 SSE 顺序、usage、reasoning signature、tool call 和 cache control 等现有语义。

完成标准：OpenAI、Anthropic、Gemini 和 OpenAI-compatible provider 的 wire path 都能被测试覆盖；任何 translation failure 都能在 response 和 correlated log 中定位。

当前状态：核心切片已完成。四类 adapter 已接入 plan 的 upstream model 解析；native/translated 模式和 fallback 权限在 dispatch 前校验，跨 binding、baseline 与 sibling failover 会刷新 plan。动态 BYOK alias 也会覆盖计划中的最终上游 model ID，避免自定义 endpoint 收到 catalog 名称。

### P6：动态 Provider 配置和管理面

建议修改范围：`internal/api/admin`、`internal/auth`、`internal/postgres`、`cmd/router/main.go`、`docs/CONFIGURATION.md`。

工作项：

- 管理面支持已知 adapter family 的 Provider registration、base URL、model alias、capability override 和启用状态。
- BYOK secret 使用既有加密边界，管理 API 只返回安全前缀/后缀和状态，不返回原文。
- Codex OAuth 作为本地 session credential，不在 dashboard 中伪装成可复制 API key；可以显示连接状态、account scope、过期状态和最近一次失败原因。
- 增加 `/v1/router/providers` 或等价只读诊断接口，展示 provider/model/capability/credential state，不泄露秘密。
- selfhosted 和 managed 对配置写入、BYOK opt-in、billing attribution 的权限继续分离。

当前状态：selfhosted 管理面已覆盖已知 adapter family 的 BYOK provider key、base URL、model alias、header/identity 配置、启用状态和上游模型发现；密文由既有 auth encryptor 保存，列表只返回安全 key 元数据。provider/model 列表、策略能力和路由分布已有只读诊断端点。Codex OAuth 仍保持本地 session credential 语义，不会在 dashboard 中生成可复制的 API key。任意未知 Provider 的动态代码加载仍明确不支持，需要新增 adapter 时按 provider constant、family、catalog、adapter 和 composition root 一起实现。

完成标准：管理员能看懂当前哪些 Provider、哪些模型、哪些凭证可用；删除或过期一个 key 不会让整个 Router 进程崩溃。

### P7：灰度、回归和上线

工作项：

- 先以 shadow mode 运行新候选系统，比较旧决策与新决策的 provider/model、价格、延迟、错误率和 failover 率。
- 按 installation、client app、model family 分批开启。
- 为 Codex OAuth 单独配置速率、过期、401、403 和 quota 指标。
- 使用真实 upstream 前，补充 record/replay smoke 场景；单元测试覆盖所有候选排除原因。
- 提供 router status、provider status、credential status 和最近路由解释的 CLI/UI 查询。

上线门槛：新系统不能提高无资格请求的成功率（不能靠越权 fallback 换成功率），不能记录秘密，且 502/401 的定位信息比旧路径更完整。

## 测试矩阵

| 场景 | 必须验证 |
| --- | --- |
| Codex OAuth + `/v1/responses` | 命中 Codex backend，带 account ID，不命中 `api.openai.com` |
| Codex OAuth + OpenAI gateway 同时存在 | Codex binding 不被 gateway key 抢走 |
| Codex OAuth 请求普通 OpenAI 模型 | OAuth 候选被排除，不能伪装为普通 API key |
| 无 OAuth、仅 deployment/BYOK key | 普通 Provider 正常进入候选池 |
| 多 Provider 同一逻辑模型 | 按 capability、价格、延迟和策略选 binding |
| tools/images/reasoning 不兼容 | 候选在 scorer 前被排除，并有结构化原因 |
| 安装排除列表耗尽候选 | 返回 `ErrNoEligibleProvider`，不出现含糊的上游 401 |
| OAuth Provider 401/403 | 不发生未经授权的跨 Provider 付费 failover |
| 流式 tool call / reasoning | SSE 顺序、signature、usage 和错误分类保持不变 |
| key/status 查询 | 只显示安全元数据，不返回 bearer、API key 或完整 account token |

## 关键风险和需要在实现前确认的决策

1. Codex OAuth 的 token refresh 是否由 Codex CLI 负责，还是 Router 需要只读检测过期状态；首版应避免复制 refresh token。
2. OAuth 订阅的 quota、速率和计费如何暴露给 Router；不能把订阅使用量错误记为 Router 平台成本。
3. 哪些 Codex 模型族允许通过 Router 暴露，是否按客户端/安装配置 allowlist。
4. 用户是否同意在一个 Router 进程中把自己的 OAuth session 提供给其他本地客户端；默认应限定为本机用户和明确的本地服务。
5. 一个请求失败时，是否允许由用户自己的 OAuth 转到安装方的 BYOK；这应是明确策略，而不是隐式重试。
6. 需要支持的 Provider 是固定的一组官方/兼容 family，还是允许插件化 adapter；首版建议固定 family，避免动态代码加载和不可控安全面。

## 建议的第一期交付切片

第一期不要同时做完整的“任意 prompt 智能分类”。最小可用切片是：

1. `ProviderCodex` 独立注册和 catalog binding。
2. credential-aware candidate resolver。
3. Codex OAuth native Responses adapter。
4. OpenAI gateway 与 Codex OAuth 的明确互斥/优先级规则。
5. `/v1/responses`、Codex CLI、普通 OpenAI key、gateway 并存的回归测试。
6. provider/model/credential 的可解释诊断输出。

完成这六项后，Router 才真正具备“多个 Provider 的可靠中转”基础；P4 的 prompt 意图分类应建立在这个基础上，而不是用分类逻辑掩盖凭证和协议边界问题。

## 交付顺序和变更原则

按 `P0 → P1 → P2 → P3 → P5 → P4 → P6 → P7` 实施。先让“候选合法性”和“Codex OAuth 安全边界”稳定，再引入语义偏好；任何阶段都必须：

- 遵守 `cmd/router/main.go` 作为 composition root 的规则。
- 不编辑 `internal/sqlc/` 生成文件，数据库变化走 migration/query/generate 流程。
- 新增 Provider 时同步 provider constant、family、catalog binding、adapter 和 dispatch validation。
- 为每个新候选约束增加能在删除生产逻辑时失败的行为测试。
- 先通过本地 8088 服务的 `/health`、`/readyz` 和最小请求，再进入 smoke/灰度。
