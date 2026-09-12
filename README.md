# DeepSeek 内容审核系统

现已支持 **Grok（复用 sub2api OAuth 账号）**，原 DeepSeek 策略默认不变。[Grok 连接配置与撤回说明](GROK_SETUP.zh.md)。新增功能在 `codex/grok-audit-provider` 分支，基线标签为 `baseline-before-grok-20260912`。

独立审核 API 与 Vue 管理后台。采用用户提供的初始提示词，支持自由编辑、草稿试跑、原子发布与回滚；模型输出 `confidence` 和 `reason`，程序使用该发布版本的阈值决定是否命中。

**不使用 Docker 部署：** [本地打包并上传 Linux 服务器的完整教程](DEPLOY_NATIVE.zh.md)。本地运行 `bash deploy/package.sh all` 可生成 amd64/arm64 安装包；服务器只需 PostgreSQL 和 systemd，无需 Go 或 Node。

## 启动

需要 Go 1.26+、Node 20.19+、pnpm 10，以及 PostgreSQL 17 或 Docker。

```sh
go run ./cmd/setup
docker compose up -d --build
```

打开 http://localhost:8090 ，用户名默认 `admin`。初始密码在本地 `.env` 的 `ADMIN_PASSWORD` 中，初始化命令不会把密码打印进日志。已有 `.env` 不会被覆盖。管理员密码仅在首次初始化时写入数据库；后续通过后台修改，修改会让所有管理员会话失效。

首次使用：

1. “模型密钥”中添加 DeepSeek 官方 API Key。
2. “审核策略 → 模型与判定”中选择该密钥、模型和阈值。
3. “提示词配置”中编辑提示词，输入测试文本并运行真实试跑。
4. 点击“发布配置”，使新版本用于正式请求。
5. “访问密钥”中为 sub2api 创建密钥，选择允许调用的策略。

初始提示词的原始文本保存在 `internal/audit/initial-prompt.txt`，包括原有转义和实体，导入不自动改写。编辑器展示将实际发送的内容。保留输出契约：confidence 为 0～1 数值，reason 为最多 20 个 Unicode 字符的字符串；输出重复字段、额外字段、空内容、截断和类型错误均视为审核失败。

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
| HTTP 超时 | 初始 10000 ms；依实测调整 |
| 重试次数 | 初始 0 |
| 失败策略 | 根据业务选择放行或前置拦截返回 503 |

阈值由本系统后台管理，sub2api 展示实际评分、阈值、原因与版本。自定义服务不使用旧内容审核的前置 Hash 黑名单，避免策略更新后沿用旧判定。邮件、自动封禁、采样和分组范围仍由 sub2api 控制。

```http
POST /v1/moderations
Authorization: Bearer <访问密钥>
Content-Type: application/json

{"model":"abuse-audit-v1","input":"待审核文本"}
```

响应 `results[0]` 包含 flagged、`category_scores.custom_policy` 与 audit 元数据。比较使用未经显示舍入的数值：`flagged = confidence >= threshold`。后台初始阈值 0.80 是可调整起点，不是经过校准的准确率保证。

## 运行与备份

- PostgreSQL 自动初始化独立表；后台草稿、版本、访问密钥和审核记录持久化。
- DeepSeek 密钥与可选输入原文使用 AES-GCM 加密；调用方 API Key 只保存摘要，管理员密码使用 PBKDF2-SHA256。
- MASTER_KEY 必须备份并与数据库备份分开保管。更换它会导致旧密钥和原文无法解密；目前不提供无迁移的主密钥轮换。
- 输入原文默认不保存；打开保存原文后，管理员可查看详情。记录按请求执行时的保留期限过期，每分钟清理。管理操作保留一年。
- `/healthz` 检查进程；`/readyz` 检查数据库。它们不会调用付费模型，也不保证 DeepSeek 实时可用。
- 生产使用 HTTPS 反向代理并设置 PUBLIC_URL。Compose 默认仅绑定回环端口，数据库不向宿主机公开。
- 更改提示词、阈值和模型参数后必须发布。停用策略/密钥是独立的立即生效操作；已经发出的模型请求可能完成。
- 每个调用方可配置每分钟请求上限；默认模型并发上限 16，容量不足返回 503。结果缓存可在策略后台配置，默认关闭；缓存按调用方、策略版本、配置、密钥和完整输入隔离。

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

数据库测试创建并清理独立的随机 schema，覆盖登录/CSRF、密钥加密、原提示词传输、草稿隔离、发布/回滚、并发更新、阈值边界、模型无效输出和审计记录。测试中的模型 HTTP 响应由本地受控替身提供，不会向 DeepSeek 发送真实用户数据或产生模型调用费用。

## 成本统计与预算

后台已增加“成本与预算”：支持缓存命中/未命中/输出分项计价、时段价格、每笔价格快照、调用方日/月预算、待核对费用和精确结果缓存。[详细规则与优化建议](COSTS_AND_OPTIMIZATION.md)。
