# Grok：直接使用 sub2api 提供的模型 API

当前方式就是普通 OpenAI 兼容调用：审核系统填写 sub2api 提供的 Base URL、普通 API Key 和 Grok 模型名，然后请求 `/v1/chat/completions`。

不调用 sub2api 的 auth/OAuth/管理员 API，不要求它部署专用审核入口，也不要求 `GROK_AUDIT_SERVICE_BINDINGS`。之前的专用入口方案已撤回并保留在 Git 历史中。

## 1. 审核系统自身的地址白名单

仅在**独立审核系统**的 `.env` 中允许实际 sub2api 源地址：

```dotenv
AUDIT_SUB2API_ORIGINS=https://sub2api.example.com
```

这是审核服务发送已保存密钥时使用的目标地址限制，不是 sub2api 配置。填写协议、主机和端口，不带 `/v1` 或末尾斜杠。HTTP 内网/本机地址同样须明确列出，多个源地址以逗号分隔；跨主机生产连接使用 HTTPS。修改后重新加载 `.env` 并启动审核服务。

## 2. 后台填写普通 API 信息

1. “模型密钥”选择 **Grok · sub2api API**。
2. Base URL 填 `https://sub2api.example.com` 或 `https://sub2api.example.com/v1`，API Key 填 sub2api 原有、能够正常调用 Grok 的普通模型密钥。
3. 新建或复制审核策略，在“模型与判定”选择 **Grok（sub2api API）**，选中已保存连接。
4. 模型名使用 sub2api 提供的名称，例如实际可用的 `grok-4.6`；自定义模型别名也可以直接填写。
5. “读取模型列表”通过标准 `GET /v1/models` 请求，仅是可选辅助。即使网关不提供此接口，也可手动填写模型并发布或试跑。
6. 先试跑，再发布。审核系统的外部接口仍为 `/v1/moderations`，提示词继续由当前策略版本控制。

实际请求结构：

```http
POST /v1/chat/completions
Authorization: Bearer <sub2api 普通 API Key>
Content-Type: application/json
```

```json
{
  "model": "grok-4.6",
  "stream": false,
  "temperature": 0,
  "max_tokens": 512,
  "response_format": {"type": "json_object"},
  "messages": [
    {"role": "system", "content": "后台保存的原审核提示词"},
    {"role": "user", "content": "<user_input>待审核内容</user_input>"}
  ]
}
```

不传 DeepSeek 的 `thinking` 参数，不要求专用响应头。审核系统不执行模型工具调用，Grok JSON 结果继续严格解析为 `confidence` / `reason`；普通网关自身的模型映射、账号管理和路由逻辑保持原样。

## 3. 避免回接循环

这是一条普通模型请求，不具有跳过 sub2api 审核的权限。如果该 Grok API 所属分组恰好把请求再次送到本审核服务，就会形成循环。

使用已经能够直接调用、且没有回接本审核服务的 Grok API。若现有部署本身有这条回接关系，需要选择另一已有分组或独立的模型 API 实例；不能声称标准接口能在任意循环配置下自动免除审核，也不能用可伪造的跳过审核请求头解决。

## 4. 凭证、缓存与计费

- 保留供应商和目标地址绑定。修改目标地址需创建新连接，不能把已有密钥转发到另一个源。
- 禁止带凭证跟随 HTTP 重定向。
- 只有模型列表按钮会访问 `/v1/models`，每次正式审核不做强制连接探测，也不调用管理员接口。
- 精确结果缓存仍按调用方、供应商、连接修订、凭证、策略版本及完整输入隔离。连接在本审核系统停用后不再复用缓存；sub2api 侧权限变化时应同步停用连接或更新连接修订。缓存命中不发起新的上游请求。
- Grok 推理和缓存 tokens 继续归一化记录。未经核对的货币成本保持未知，不套用 DeepSeek 价格。
- 已启用人民币预算的调用方，在 Grok 价格不可确定时仍可能被成本保护拒绝；这与普通 API Key 的鉴权能力是两回事。可先在后台试跑验证模型调用。

## 5. Git 与撤回

原 DeepSeek 基线标签：`baseline-before-grok-20260912`。

- 独立审核系统基线：`a62bbf2`。
- sub2api 基线：`37e832258`。
- 原 sub2api 专用入口提交 `041c83bd9` 已由 `25febb713` 撤回，其工作树回到上述基线。

独立审核系统的标准 API 改动另行提交，可以使用 `git revert` 撤回，不改写历史。代码版本不包含 `.env` 或数据库数据；不要用整库恢复覆盖后续业务数据。

已通过标准接口请求、无专用头响应、可选模型列表、手填别名、地址白名单、重定向保护、凭证停用及 DeepSeek/计费回归测试。真实调用需使用你现有的 sub2api 地址和普通 API Key。
