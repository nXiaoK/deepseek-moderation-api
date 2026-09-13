# 部署指南

本文按当前代码提供两种方式：

- **Docker Compose**：应用和 PostgreSQL 都由 Compose 管理，适合大多数服务器。
- **手动部署**：应用以本地编译的 Go 二进制运行，PostgreSQL 仍可使用 Docker；也可以替换为已有的 PostgreSQL。

两种方式都要求使用全新的数据库。程序首次启动会自动建表并创建默认策略；已有数据库不会自动迁移或清空。

## 运行要求

- 生产服务器建议 Ubuntu 24.04 或 Debian 12，至少 2 个 CPU、2 GB 内存。
- 服务器需要能访问 DeepSeek 或配置的第三方接口。
- 对外提供服务时使用 HTTPS 反向代理。应用自身只监听本机地址即可。
- `MASTER_KEY` 必须是随机的 Base64 密钥，并与数据库备份分开保管。更换它会导致已保存的供应商密钥和可选原文无法解密。

## 方式一：Docker Compose

服务器需要 Docker Engine 24+ 和 Docker Compose v2（`docker compose` 命令）。

### 1. 准备项目和环境文件

```bash
git clone <仓库地址> deepseek-moderation-api
cd deepseek-moderation-api
cp .env.example .env
```

生成随机值并写入 `.env`（不要把 `.env` 提交到 Git）：

```bash
MASTER_KEY="$(openssl rand -base64 32)"
ADMIN_PASSWORD="$(openssl rand -hex 24)"
POSTGRES_PASSWORD="$(openssl rand -hex 24)"
sed -i.bak \
  -e "s|^MASTER_KEY=.*|MASTER_KEY=$MASTER_KEY|" \
  -e "s|^ADMIN_PASSWORD=.*|ADMIN_PASSWORD=$ADMIN_PASSWORD|" \
  -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$POSTGRES_PASSWORD|" .env
rm -f .env.bak
chmod 600 .env
```

记录 `ADMIN_PASSWORD`，首次登录后台使用它。`ADMIN_PASSWORD` 只在数据库没有管理员时生效，后续请在后台修改密码。

### 2. 启动

```bash
docker compose up -d --build
docker compose ps
curl --fail http://127.0.0.1:8090/readyz
```

看到 `{"ok":true}` 表示应用和数据库已连通。Compose 默认把应用端口绑定到 `127.0.0.1:8090`，数据库不暴露到宿主机公网。

查看日志或停止服务：

```bash
docker compose logs -f app
docker compose stop
docker compose start
```

### 3. 访问和 HTTPS

临时访问可使用 SSH 隧道：

```bash
ssh -N -L 8090:127.0.0.1:8090 user@SERVER_IP
```

浏览器打开 `http://localhost:8090`。如果使用 Nginx、Caddy 或云负载均衡，设置 `.env` 中的 `PUBLIC_URL` 为浏览器实际访问的完整来源，例如 `https://audit.example.com`，不要带路径或末尾 `/`，然后重建应用：

```bash
docker compose up -d --force-recreate app
```

反向代理应把请求转发到 `http://127.0.0.1:8090`，并保留 `Host`、`X-Forwarded-Proto` 和 `X-Forwarded-For`。应用会拒绝非 localhost 的 HTTP `PUBLIC_URL`。

### 4. 数据、升级和备份

PostgreSQL 数据保存在 Compose 卷 `audit-postgres`。升级前备份数据库和 `.env`，尤其是原 `MASTER_KEY`：

```bash
mkdir -p backups
docker compose exec -T db pg_dump -U audit -d audit > backups/audit-$(date +%Y%m%d-%H%M%S).sql
docker compose pull
docker compose up -d --build
curl --fail http://127.0.0.1:8090/readyz
```

恢复前先停止应用，并确认恢复目标是本系统的空库：

```bash
docker compose stop app
cat backups/audit-YYYYMMDD-HHMMSS.sql | docker compose exec -T db psql -U audit -d audit
docker compose start app
```

数据库备份可能包含加密密钥密文和审核原文，必须限制文件权限并加密保存。

## 方式二：手动部署，数据库使用 Docker

这种方式适合不希望应用容器化、需要由 systemd 管理应用进程的服务器。服务器需要 Go 1.26+、Node 20.19+、pnpm 10；也可以在另一台机器交叉编译后只上传产物。

### 1. 启动 PostgreSQL 容器

```bash
mkdir -p /opt/deepseek-audit/postgres
docker run -d --name deepseek-audit-db \
  --restart unless-stopped \
  -e POSTGRES_USER=audit \
  -e POSTGRES_DB=audit \
  -e POSTGRES_PASSWORD='CHANGE_ME_RANDOM' \
  -v /opt/deepseek-audit/postgres:/var/lib/postgresql/data \
  -p 127.0.0.1:55439:5432 \
  postgres:17-alpine
docker exec deepseek-audit-db pg_isready -U audit -d audit
```

将 `CHANGE_ME_RANDOM` 替换为随机密码，并在应用的 `DATABASE_URL` 中使用同一密码。数据库只绑定回环地址，不要开放 5432 到公网。

### 2. 构建应用

在项目根目录执行：

```bash
cd frontend
corepack enable
corepack prepare pnpm@10.12.3 --activate
pnpm install --frozen-lockfile
pnpm build
cd ..
go build -trimpath -ldflags='-s -w' -o audit-server ./cmd/server
```

将 `audit-server`、`frontend/dist/` 和 `internal/audit/initial-prompt.txt`（提示词已嵌入二进制，后者仅供审阅）部署到例如 `/opt/deepseek-audit/current/`。生产升级时保留旧版本目录，使用软链接切换版本。

### 3. 创建环境文件

```bash
sudo install -m 600 /dev/null /etc/deepseek-audit.env
openssl rand -base64 32
openssl rand -hex 24
sudo nano /etc/deepseek-audit.env
```

至少填写：

```dotenv
MASTER_KEY=<openssl 生成的 Base64 值>
ADMIN_USER=admin
ADMIN_PASSWORD=<首次启动的 16-256 字符密码>
POSTGRES_PASSWORD=<PostgreSQL 容器密码>
DATABASE_URL=postgres://audit:<密码>@127.0.0.1:55439/audit?sslmode=disable
PUBLIC_URL=http://localhost:8090
LISTEN_ADDR=127.0.0.1:8090
STATIC_DIR=/opt/deepseek-audit/current/frontend/dist
```

密码中的 `@`、`:`、`/` 等字符必须进行 URL 编码；使用 `openssl rand -hex 24` 可避免该问题。不要在升级时重新生成 `MASTER_KEY`。

### 4. 使用 systemd

仓库中的 `deploy/deepseek-audit.service` 默认以 `audit` 用户运行，并读取 `/etc/deepseek-audit.env`。先创建运行用户并确保它能读取应用目录：

```bash
sudo useradd --system --user-group --home-dir /opt/deepseek-audit --shell /usr/sbin/nologin audit
sudo chown -R root:audit /opt/deepseek-audit
sudo chmod 755 /opt/deepseek-audit/current/audit-server
sudo cp deploy/deepseek-audit.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now deepseek-audit
sudo systemctl status deepseek-audit --no-pager
curl --fail http://127.0.0.1:8090/readyz
```

首次启动自动建表。日志和常用操作：

```bash
sudo journalctl -u deepseek-audit -n 100 --no-pager
sudo systemctl restart deepseek-audit
sudo systemctl stop deepseek-audit
```

### 5. 反向代理、升级和备份

手动部署的 HTTPS、`PUBLIC_URL`、SSH 隧道和 sub2api 配置与 Compose 方式相同。升级时上传新的版本目录，切换 `current` 软链接后重启并检查 `/readyz`。备份 Docker PostgreSQL：

```bash
umask 077
docker exec deepseek-audit-db pg_dump -U audit -d audit > /var/backups/deepseek-audit-$(date +%Y%m%d-%H%M%S).sql
```

## 首次业务配置

登录后台后依次完成：添加 DeepSeek 或 sub2api 连接密钥；创建审核模型通道；在审核策略中绑定通道并设置优先级、权重、超时和调用次数；编辑提示词和阈值并试跑；点击“保存并生效”后启用策略；最后创建供 sub2api 使用的访问密钥。审核服务的 API 地址是根地址，不要在 Base URL 中追加 `/v1`。

健康检查只验证进程（`/healthz`）或数据库（`/readyz`），不会调用付费模型。真实审核失败时继续检查供应商密钥、模型、网络和余额。

## 常见故障

| 现象 | 处理 |
| --- | --- |
| 容器反复重启 | `docker compose logs app db`，检查 `MASTER_KEY`、数据库密码和端口 |
| `readyz` 返回 503 | 检查 PostgreSQL 容器状态、连接串、卷权限和 `pg_isready` |
| 登录或保存时报来源/CSRF 错误 | `PUBLIC_URL` 必须与浏览器来源完全一致，改后重启应用 |
| 首次密码不生效 | 数据库已有管理员；使用后台改密，不要只改环境文件 |
| systemd `Permission denied` | 检查 `audit` 用户对二进制和 `frontend/dist` 的读取/执行权限 |
| 接口健康但无法审核 | 检查已启用策略是否绑定有效通道，以及供应商密钥和网络 |
