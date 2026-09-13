# DeepSeek 内容审核系统

支持 **DeepSeek 与 Grok（通过 sub2api 标准 API）**，一个策略可绑定多个模型通道，按优先级和权重分流，并在失败时自动切换。[Grok 连接配置](GROK_SETUP.zh.md)。

独立审核 API 与 Vue 管理后台。策略只保留当前配置，支持直接编辑、未保存内容试跑和“保存并生效”。模型输出 `confidence` 和 `reason`，程序使用当前请求读取的阈值决定是否命中。没有草稿、发布、版本历史或回滚功能。

本次只修改审核服务，保持现有 sub2api `custom_audit` 接口约定。不提供旧开发数据库迁移；请配置新的空数据库，原数据库不会被程序自动清空。

**部署文档：** [Docker Compose 与手动部署指南](DEPLOY.zh.md)。手动部署也可以只将 PostgreSQL 放在 Docker 中；需要原生 systemd 部署时，另见[本地打包并上传 Linux 服务器的教程](DEPLOY_NATIVE.zh.md)。

## 启动

需要 Go 1.26+、Node 20.19+、pnpm 10，以及 PostgreSQL 17 或 Docker。

```sh
go run ./cmd/setup
docker compose up -d --build
```

打开 http://localhost:8090 ，用户名默认 `admin`。初始密码在本地 `.env` 的 `ADMIN_PASSWORD` 中，初始化命令不会把密码打印进日志。已有 `.env` 不会被覆盖。管理员密码仅在首次初始化时写入数据库；后续通过后台修改，修改会让所有管理员会话失效。

首次使用：

1. “连接密钥”添加 DeepSeek 官方或第三方 Key，并填写对应 API 地址；也可添加 sub2api 普通 Grok API Key。
2. “审核模型”新增通道，填写模型名、选择连接密钥，设置单次超时和并发上限。
3. “审核策略 → 模型调度”绑定一个或多个通道，设置优先级、权重、总调用时限和最多调用次数。
4. “审核规则”编辑提示词与阈值；“审核试跑”直接验证当前编辑内容，不自动保存。
5. 点击“保存并生效”，然后启用策略。新建策略默认停用。
6. “访问密钥”为 sub2api 创建密钥，选择允许调用的策略。

例如：DeepSeek A/B 均设优先级 1，权重 70/30；Grok 设优先级 2，主组无法完成审核时接替。同级采用加权随机；数值越小越优先，每个通道每请求最多调用一次，有效判定直接返回。模型拒答、无效输出、超时或上游故障才触发切换。

初始提示词的原始文本保存在 `internal/audit/initial-prompt.txt`，包括原有转义和实体，导入不自动改写。编辑器展示将实际发送的内容。保留输出契约：confidence 为 0～1 数值，reason 为最多 80 个 Unicode 字符的字符串；输出重复字段、额外字段、空内容、截断和类型错误均视为审核失败。

第一版支持文本及文本内容块数组，最多 64000 字符；收到图片明确报错。模型调用不使用工具、不改变管理员的审核范围。试跑记录与正式流量隔离；未填写真实 DeepSeek 密钥前，不能进行真实模型审核。

## 本地开发

使用独立本地 PostgreSQL；设置 `.env` 中 DATABASE_URL。以下仅适用于全新本机开发数据库，不用于生产：

```sh
docker run -d --name deepseek-audit-dev-db \
  -e POSTGRES_USER=audit -e POSTGRES_DB=audit \
  -e POSTGRES_HOST_AUTH_METHOD=trust \
  -p 127.0.0.1:55439:5432 postgres:17-alpine
cd frontend
pnpm install --frozen-lockfile
pnpm build
cd ..
set -a
. ./.env
set +a
go run ./cmd/server
```

后端同源提供构建后的后台。使用 Vite 热更新时，将 PUBLIC_URL 设为浏览器实际使用的 `http://localhost:5173`，再启动后端及 `pnpm dev`；CSRF 来源校验使用该值。

## sub2api 接入

需搭配本次添加的 `custom_audit` 适配，旧版本不能仅换 URL：它不读取自定义策略判定。

| 配置 | 值 |
| --- | --- |
| 审核服务类型 | 自定义审核服务 |
| Base URL | 审核服务根地址，不加 `/v1` |
| 模型名 | `abuse-audit-v1`，或后台策略别名 |
| API Key | 本系统为调用方生成的访问密钥，不是 DeepSeek 官方密钥 |
| HTTP 超时 | 初始 15000 ms；需覆盖总调用时限及约 5 秒收尾时间 |
| 重试次数 | 初始 0 |
| 失败策略 | 根据业务选择放行或前置拦截返回 503 |

阈值由本系统后台管理。响应保留 `audit.schema_version=1`、正整数 `audit.policy_version` 及原评分、阈值、原因字段，以适配现有 sub2api 严格校验；`policy_version` 对应内部自动维护的保存标识，不提供历史配置管理。自定义服务不使用旧内容审核的前置 Hash 黑名单，避免策略更新后沿用旧判定。邮件、自动封禁、采样和分组范围仍由 sub2api 控制。

```http
POST /v1/moderations
Authorization: Bearer <访问密钥>
Content-Type: application/json

{"model":"abuse-audit-v1","input":"待审核文本"}
```

响应 `results[0]` 包含 flagged、`category_scores.custom_policy` 与 audit 元数据。比较使用未经显示舍入的数值：`flagged = confidence >= threshold`。后台初始阈值 0.80 是可调整起点，不是经过校准的准确率保证。

## 运行与备份

- PostgreSQL 自动初始化独立表；当前策略、模型通道、访问密钥和审核记录持久化。
- DeepSeek 密钥与可选输入原文使用 AES-GCM 加密；调用方 API Key 只保存摘要，管理员密码使用 PBKDF2-SHA256。
- MASTER_KEY 必须备份并与数据库备份分开保管。更换它会导致旧密钥和原文无法解密；目前不提供无迁移的主密钥轮换。
- 输入原文默认不保存；打开保存原文后，管理员可查看详情。记录按请求执行时的保留期限过期，每分钟清理。管理操作保留一年。
- `/healthz` 检查进程；`/readyz` 检查数据库。它们不会调用付费模型，也不保证 DeepSeek 实时可用。
- 生产使用 HTTPS 反向代理并设置 PUBLIC_URL。Compose 默认仅绑定回环端口，数据库不向宿主机公开。
- 更改提示词、阈值和调度参数后点击“保存并生效”；通道编辑保存后直接影响引用策略。停用策略、通道或密钥后不再启动后续调用；已经发出的模型请求可能完成。
- 每个调用方可配置每分钟请求上限；全局模型并发上限 16，另有共享的通道并发上限，容量不足返回 503。结果缓存默认关闭，按调用方、当前规则与调度配置、通道、密钥和完整输入隔离。

备份例子（备份文件包含密钥密文与可能保存的输入，请保存在受控位置）：

```sh
docker compose exec -T db pg_dump -U audit -d audit > audit-backup.sql
```

## 验证

```sh
go test -race ./...
AUDIT_TEST_DATABASE_URL='postgres://audit@127.0.0.1:55439/audit?sslmode=disable' go test -race ./...
cd frontend
pnpm build
```

数据库测试创建并清理独立的随机 schema，覆盖登录/CSRF、密钥加密、原提示词传输、未保存试跑隔离、保存即生效、优先级加权分流、失败切换、并发更新、阈值边界、模型无效输出和审计记录。测试中的模型 HTTP 响应由本地受控替身提供，不会向 DeepSeek 发送真实用户数据或产生模型调用费用。

## 成本统计与预算

后台已增加“成本与预算”：支持缓存命中/未命中/输出分项计价、时段价格、每笔价格快照、调用方日/月预算、待核对费用和精确结果缓存。[详细规则与优化建议](COSTS_AND_OPTIMIZATION.md)。

## 多模型运行说明

- 单次超时默认 4000 ms，总调用时限默认 9000 ms，最多调用 3 次（包含首次）。剩余时限不足时提前终止切换。每次结算最多 1 秒，整个请求最后的费用汇总与日志保存最多 4 秒；这些操作共享请求总调用时限或有界收尾，不无限重试。
- 连续模型失败 3 次冷却 30 秒，429 遵守有效 Retry-After（最多 24 小时）。冷却后只放行一个恢复试探。401/403 和其他上游 4xx 标记配置异常，修正后保存通道或点击“清缓存 / 重试连接”。无定时付费探测。
- 健康状态与共享并发为进程内状态，重启后重新学习；本实现面向单进程服务，不提供多副本全局容量协调。
- 审核记录按外部请求计一次，详情列出每次尝试；费用按每次尝试独立记录并汇总。超时调用可能仍有费用，保留待核对金额。
- Grok 无法可靠估计人民币成本，配置人民币预算的调用方会排除 Grok 通道；未知费用不作为免费。无可用通道时返回明确错误，沿用 sub2api 原有失败策略。
- 修改 sub2api 的上游模型映射后，在审核模型页面清除对应通道缓存。Grok 通道所属调用链不能再次把审核请求送回本服务，详见 Grok 接入文档。

## DeepSeek 第三方接口

“连接密钥 → DeepSeek（官方 / 第三方）”支持填写 HTTP(S) 根地址或 `/v1` 地址，例如 `https://api.example.com/v1`；第三方请求统一发送到 `/v1/chat/completions`，不会重复拼接 `/v1`。官方地址仍使用原 `/chat/completions`。第三方服务需支持模型当前的 Chat Completions 参数和 JSON 输出格式。

地址与凭证绑定，已保存凭证更换地址时请新建连接，再修改模型通道的连接密钥。第三方实际价格未知，费用标记待核对，不套用官方价；启用人民币预算的调用方不会选择第三方通道。
