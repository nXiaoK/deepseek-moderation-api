# Grok：直接使用 sub2api 提供的模型 API

当前方式就是普通 OpenAI 兼容调用：审核系统填写 sub2api 提供的 Base URL、普通 API Key 和 Grok 模型名，然后请求 `/v1/responses` 流式接口。对外审核接口仍是一次返回完整 JSON 的 `/v1/moderations`。

不调用 sub2api 的 auth/OAuth/管理员 API，不要求它部署专用审核入口，也不要求 `GROK_AUDIT_SERVICE_BINDINGS`。之前的专用入口方案已撤回并保留在 Git 历史中。

## 1. 审核系统自身的地址白名单

仅在**独立审核系统**的 `.env` 中允许实际 sub2api 源地址：

```dotenv
AUDIT_SUB2API_ORIGINS=https://sub2api.example.com
```

这是审核服务发送已保存密钥时使用的目标地址限制，不是 sub2api 配置。填写协议、主机和端口，不带 `/v1` 或末尾斜杠。HTTP 内网/本机地址同样须明确列出，多个源地址以逗号分隔；跨主机生产连接使用 HTTPS。修改后重新加载 `.env` 并启动审核服务。

## 2. 后台填写普通 API 信息

1. “连接密钥”选择 **Grok · sub2api API**。
2. Base URL 填实际 sub2api 根地址或带 `/v1` 的地址，API Key 填能够正常调用 Grok 的普通模型密钥。
3. 在“审核模型”创建通道，选择该连接，模型名填写 sub2api 实际支持的模型或别名。
4. 在“审核策略 → 模型调度”添加通道。与 DeepSeek 同优先级时共同分流，较低优先级时作为备用。
5. “审核试跑”可按调度规则执行，或指定 Grok 通道单独验证。
6. 点击“保存并生效”并启用策略。对外接口继续为 `/v1/moderations`，不用修改 sub2api 项目代码。

实际请求结构：

```http
POST /v1/responses
Authorization: Bearer <sub2api 普通 API Key>
Content-Type: application/json
Accept: text/event-stream
```

```json
{
  "model": "grok-4.6",
  "stream": true,
  "temperature": 0,
  "max_output_tokens": 512,
  "text": {"format": {"type": "json_object"}},
  "reasoning": {"effort": "none"},
  "instructions": "后台保存的原审核提示词",
  "input": "<user_input>待审核内容</user_input>"
}
```

不传 DeepSeek 的 `thinking` 参数，不要求专用响应头。审核分类不需要长推理，请求带 `reasoning.effort=none`，避免 Grok 先写几千个隐藏推理 tokens。若所用网关或模型拒绝 `none`，改用该网关支持的最低档（例如 `low`）或非推理型号。审核系统不执行模型工具调用，从 SSE（`response.output_text.delta` / `response.completed`）或非流式 Responses JSON 中组装文本，再严格解析为 `confidence` / `reason`。若网关忽略 `stream` 并返回普通 JSON，仍按 Responses 对象解析。普通网关自身的模型映射、账号管理和路由逻辑保持原样。sub2api 需要提供 `/v1/responses`；仅实现 `/v1/chat/completions` 的网关不能用于 Grok。

## 3. 避免回接循环

这是一条普通模型请求，不具有跳过 sub2api 审核的权限。如果该 Grok API 所属分组恰好把请求再次送到本审核服务，就会形成循环。

使用已经能够直接调用、且没有回接本审核服务的 Grok API。若现有部署本身有这条回接关系，需要选择另一已有分组或独立的模型 API 实例；不能声称标准接口能在任意循环配置下自动免除审核，也不能用可伪造的跳过审核请求头解决。

## 4. 凭证、缓存与计费

- 保留供应商和目标地址绑定。修改目标地址需创建新连接，不能把已有密钥转发到另一个源。
- 禁止带凭证跟随 HTTP 重定向。
- 每次正式审核不做模型列表或管理接口探测，直接调用模型。
- 精确结果缓存按调用方、当前规则与调度配置、模型通道、凭证及完整输入隔离。停用连接后不再复用缓存；sub2api 模型映射改变时在审核模型页点击“清缓存 / 重试连接”。缓存命中不发起新的上游请求。
- Grok 推理和缓存 tokens 继续归一化记录。在“成本与预算 → 模型单价”按通道配置的模型名填写价格，后续请求按用量和价格快照计价；无单价或用量缺失时保持待核对。上游返回的模型名不改变选价，历史记录不补算。
- 已启用人民币预算的调用方，会排除无法确定价格的 Grok 通道；这与普通 API Key 的鉴权能力是两回事。可先在后台试跑验证模型调用。

## 5. 数据库与兼容范围

该实现使用新的当前配置及模型通道表结构，不迁移旧开发数据。使用空数据库初始化。本次仅更改独立审核项目，不修改 sub2api 的代码、数据库或部署。

对外适配原版 sub2api：`model=策略别名`，`results[0].flagged` 为策略判定，`categories.illicit` 同步判定，`category_scores.illicit` 在命中时为 1、未命中时为 0。`illicit` 仅作为原版内置分类的兼容载体；真实评分、阈值和原因保留在 `audit` 元数据及本服务后台。原版 sub2api 忽略 `audit`，无需 `custom_audit` 代码。接入配置与前置 Hash 缓存等限制见 [README](README.md#sub2api-接入)。
