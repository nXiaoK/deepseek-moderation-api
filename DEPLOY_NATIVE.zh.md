# 不使用 Docker：本地打包，上传 Linux 服务器运行

适用：Ubuntu 24.04 / Debian 12 或更新版本，使用 systemd 管理服务。服务器架构可以是 x86_64 或 ARM64。本地 macOS 也能交叉编译 Linux 安装包。

**服务器不需要 Go、Node、pnpm 或 Docker。** 上传包里已包含后端可执行程序和编译后的后台页面，但当前系统仍需 PostgreSQL。下面使用服务器原生安装的 PostgreSQL；已配置外部 PostgreSQL 的用户可直接填连接地址。

程序、原始提示词、数据库建表代码、时区数据均已打包；`.env`、本地管理员密码、DeepSeek 密钥及本地数据库数据不会被放入安装包。首次部署是一个全新系统，本地已有配置不会自动迁移。

## 1. 在本地打包

本地需要 Go 1.26+、Node 20.19+、pnpm 10。进入项目：

```bash
cd /Users/xiaok/github/deepseek-moderation-api
bash deploy/package.sh all
```

脚本安装锁文件声明的前端依赖、构建后台、运行 Go 测试，然后生成两份 Linux 安装包及对应 SHA256 文件：

```text
dist/
  deepseek-audit-版本号-linux-amd64.tar.gz
  deepseek-audit-版本号-linux-amd64.tar.gz.sha256
  deepseek-audit-版本号-linux-arm64.tar.gz
  deepseek-audit-版本号-linux-arm64.tar.gz.sha256
```

`go test ./...` 在未设置测试数据库地址时会跳过数据库集成测试；如需运行它们，先配置独立测试 PostgreSQL 的 AUDIT_TEST_DATABASE_URL。打包脚本不会启动 Docker。

在服务器运行 `uname -m` 选择包：

| 服务器输出 | 选择安装包 |
| --- | --- |
| `x86_64` | `linux-amd64`，常见 Intel/AMD 云服务器 |
| `aarch64` / `arm64` | `linux-arm64`，ARM 云服务器 |

也可以只打包一种架构，指定版本号：

```bash
bash deploy/package.sh amd64 v0.1.0
```

压缩包解压后包含 `audit-server`、`frontend/dist/`、systemd 配置、环境初始化脚本和本教程。不要只上传二进制而漏掉后台静态文件。

## 2. 上传压缩包

以下示例用 `v0.1.0` 和 `amd64`，替换成你实际生成的版本及架构；将 `deploy@SERVER_IP` 换成服务器 SSH 用户和地址。

本地执行：

```bash
cd /Users/xiaok/github/deepseek-moderation-api
scp dist/deepseek-audit-v0.1.0-linux-amd64.tar.gz \
    dist/deepseek-audit-v0.1.0-linux-amd64.tar.gz.sha256 \
    deploy@SERVER_IP:/tmp/
ssh deploy@SERVER_IP
```

也可以通过 SFTP 上传同样的两个文件到 `/tmp`。

服务器验证：

```bash
cd /tmp
sha256sum -c deepseek-audit-v0.1.0-linux-amd64.tar.gz.sha256
```

看到 `OK` 后继续。

## 3. 服务器安装原生 PostgreSQL

在服务器执行：

```bash
sudo apt update
sudo apt install -y postgresql postgresql-client ca-certificates openssl curl
sudo systemctl enable --now postgresql
sudo useradd --system --user-group --home-dir /opt/deepseek-audit --shell /usr/sbin/nologin audit
sudo -u postgres createuser --no-superuser --no-createdb --no-createrole audit
sudo -u postgres createdb --owner=audit audit
```

这些是首次安装命令；如果系统用户、数据库角色或数据库已存在，先检查是否为本应用创建，再跳过对应创建命令。不要覆盖其他应用的同名资源。

本教程使用 PostgreSQL Unix socket + peer 认证：程序以 Linux 用户 `audit` 运行，连接同名数据库角色 `audit`，无需把数据库密码存到环境文件。数据库角色仅拥有自己的库，没有超级用户权限。数据库无需开放公网 5432 端口。

验证连接：

```bash
sudo -u audit psql -h /var/run/postgresql -U audit -d audit -c 'SELECT current_user, current_database();'
```

应看到用户与数据库均为 `audit`。Ubuntu/Debian 原生 PostgreSQL 通常默认支持 local peer；如你修改过认证，先运行 `sudo -u postgres psql -Atc 'SHOW hba_file;'` 找到配置，再确认存在适用于该库和角色的 `local audit audit peer` 规则。不要改成 `trust` 或放宽其他数据库的认证。

## 4. 解压应用

服务器执行：

```bash
sudo install -d -m 755 /opt/deepseek-audit/releases
sudo tar -xzf /tmp/deepseek-audit-v0.1.0-linux-amd64.tar.gz -C /opt/deepseek-audit/releases
sudo chmod 755 /opt/deepseek-audit/releases/deepseek-audit-v0.1.0-linux-amd64/audit-server
sudo ln -s /opt/deepseek-audit/releases/deepseek-audit-v0.1.0-linux-amd64 /opt/deepseek-audit/current
```

最终目录：

```text
/opt/deepseek-audit/
  current -> releases/deepseek-audit-v0.1.0-linux-amd64
  releases/
    deepseek-audit-v0.1.0-linux-amd64/
      audit-server
      frontend/dist/
      deploy/
/etc/deepseek-audit.env
```

应用文件可以由 root 持有，`audit` 用户只需读取及执行；运行时业务数据保存在 PostgreSQL 中。

## 5. 生成服务器配置

暂时没有域名也能使用，先采用 SSH 隧道访问：

```bash
sudo bash /opt/deepseek-audit/current/deploy/init-native-env.sh
sudo nano /etc/deepseek-audit.env
```

脚本生成随机管理员密码和 32 字节加密主密钥，文件权限为 600，不会覆盖已存在的文件。配置内容的结构如下，密钥已由脚本自动填入：

```dotenv
MASTER_KEY=脚本自动生成
ADMIN_USER=admin
ADMIN_PASSWORD=脚本自动生成
DATABASE_URL='postgres://audit@/audit?host=/var/run/postgresql&sslmode=disable'
PUBLIC_URL=http://localhost:8090
LISTEN_ADDR=127.0.0.1:8090
STATIC_DIR=/opt/deepseek-audit/current/frontend/dist
```

记录 `ADMIN_PASSWORD`，之后用于后台登录。**升级或重启时保留原 MASTER_KEY**：它用于解密 DeepSeek 密钥及可选输入原文。不要重新运行初始化脚本或生成新主密钥代替原文件。

这里的 sslmode=disable 只用于本机 Unix socket。如果连接远程 PostgreSQL，改成供应商提供的连接串并配置证书验证，例如 sslmode=verify-full；数据库主机、用户名、密码需要 URI 编码。

程序不会自动读取工作目录中的 `.env`。下面的 systemd 配置会显式读取 `/etc/deepseek-audit.env`，不要漏掉它。

## 6. 用 systemd 启动与开机自启

```bash
sudo cp /opt/deepseek-audit/current/deploy/deepseek-audit.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now deepseek-audit
sudo systemctl status deepseek-audit --no-pager
curl --fail http://127.0.0.1:8090/readyz
```

看到 `active (running)` 和 `{"ok":true}` 表示应用与数据库已连通。首次启动自动建表并创建管理员，不需要手工导入 SQL。这个健康检查不代表 DeepSeek API 已配置或模型审核已成功。

常用操作：

```bash
sudo journalctl -u deepseek-audit -n 100 --no-pager
sudo journalctl -u deepseek-audit -f
sudo systemctl restart deepseek-audit
sudo systemctl stop deepseek-audit
```

## 7. 不配置域名，直接从本地访问

在你的本地电脑执行，保持此 SSH 会话运行：

```bash
ssh -N -L 8090:127.0.0.1:8090 deploy@SERVER_IP
```

打开 **http://localhost:8090**，用户名 `admin`，密码为服务器 `/etc/deepseek-audit.env` 中的 ADMIN_PASSWORD。

如果本地原有审核后台已经占用了 8090，可以使用 18090：先把**服务器**环境文件的 PUBLIC_URL 改为 `http://localhost:18090` 并重启服务，然后执行：

```bash
ssh -N -L 18090:127.0.0.1:8090 deploy@SERVER_IP
```

再打开 http://localhost:18090。PUBLIC_URL 必须等于浏览器实际访问的来源（协议、域名、端口），否则登录或保存时会被来源校验拒绝。不能随意改成 `http://服务器IP:8090`；非 localhost 的后台访问要求 HTTPS。

首次配置流程：连接密钥中添加 API Key → 审核模型中创建通道 → 审核策略绑定通道并设置优先级/权重 → 编辑提示词/阈值并试跑 → 保存并生效、启用策略 → 为 sub2api 创建访问密钥。

## 8. 有域名时，用 Nginx + HTTPS

推荐用于长期使用及跨服务器接入。假设域名 `audit.example.com` 已解析到服务器；替换下面所有示例域名。服务器入站开放 80/443，应用继续只监听 `127.0.0.1:8090`。

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
sudo nano /etc/nginx/sites-available/deepseek-audit
```

写入：

```nginx
server {
    listen 80;
    server_name audit.example.com;
    client_max_body_size 1m;

    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    # This example has a single public-facing proxy; discard client-supplied XFF.
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_connect_timeout 5s;
    proxy_read_timeout 45s;
    proxy_send_timeout 45s;

    # Image moderation and policy trials accept JSON bodies up to 32 MiB.
    location = /v1/moderations {
        client_max_body_size 32m;
        proxy_pass http://127.0.0.1:8090;
    }

    location ~ ^/admin/policies/[^/]+/test$ {
        client_max_body_size 32m;
        proxy_pass http://127.0.0.1:8090;
    }

    location / {
        proxy_pass http://127.0.0.1:8090;
    }
}
```

正式审核和策略试跑单独允许 32 MiB 的 JSON 请求体（包含图片 Base64 编码开销）；其余接口保留 1 MiB 限制。后端仍独立校验：正式纯文本请求最多 1 MiB，含图审核和试跑最多 32 MiB。

启用配置并申请证书：

```bash
sudo ln -s /etc/nginx/sites-available/deepseek-audit /etc/nginx/sites-enabled/deepseek-audit
sudo nginx -t
sudo systemctl reload nginx
sudo certbot --nginx -d audit.example.com --redirect
```

Certbot 会交互询问邮箱及服务条款。需要域名解析和 80 端口可达；如使用其他证书或代理，按对应环境安装证书即可。已有站点勿覆盖其配置。

接着编辑 `/etc/deepseek-audit.env`：

```dotenv
PUBLIC_URL=https://audit.example.com
AUDIT_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

不要带末尾 `/`，然后执行 `sudo systemctl restart deepseek-audit`。之后访问 **https://audit.example.com** 登录；无需把应用端口 8090 开放到公网。

`AUDIT_TRUSTED_PROXIES` 只填写应用实际直连的可信代理地址或网段，默认留空时忽略所有转发头。上述同机 Nginx 覆盖 `X-Forwarded-For` 后，登录及审核入口会按真实客户端 IP 分桶限流；登录同时保留全局速率与并发保护。不要信任 `0.0.0.0/0`、`::/0` 或公网客户端网段。如果前面还有 CDN/其他代理，应限制 Nginx 的来源并正确设置其真实 IP 模块，或逐层追加转发链并精确配置可信代理；应用从右向左跳过可信节点，在首个不可信节点停止。容器部署应填写应用看到的实际代理来源地址，不能直接照抄回环地址。

## 9. sub2api 连接设置

兼容原版 sub2api 的内容审计接口，只需更新本审核服务并配置原有内容审计设置，无需修改 sub2api 代码。

| 项目 | 填写值 |
| --- | --- |
| 运行模式 | 前置拦截 |
| Base URL | 同机原生运行：`http://127.0.0.1:8090`；跨服务器：`https://audit.example.com` |
| 模型名 | `abuse-audit-v1`，或后台自定义策略别名 |
| API Key | 本审核后台生成的调用方密钥 |
| HTTP 超时 | 初始 15000 ms，需覆盖策略总调用时限及约 5 秒收尾时间 |
| 重试次数 | 初始 0 |
| 分类阈值 | 沿用默认值；所有分类须大于 0 |

Base URL 不加 `/v1` 或 `/v1/moderations`。如果 sub2api 在容器里运行，其 `127.0.0.1` 指向容器自身，应使用可达的 HTTPS 域名。

本服务把策略命中映射为 `category_scores.illicit=1`，未命中映射为 `0`；真实评分和原因在本服务后台及 `audit` 扩展字段中保留。sub2api 日志显示的 100% 是兼容判定信号。原版的采样、分组范围、关键词和前置 Hash 缓存仍然生效，详见 [sub2api 接入说明](README.md#sub2api-接入)。

## 10. 更新与备份

更新前保留旧版本目录、数据库备份和原环境文件。每次上传使用新版本号，并校验 SHA256：

```bash
sudo tar -xzf /tmp/deepseek-audit-v0.1.1-linux-amd64.tar.gz -C /opt/deepseek-audit/releases
sudo ln -s /opt/deepseek-audit/releases/deepseek-audit-v0.1.1-linux-amd64 /opt/deepseek-audit/current.next
sudo mv -Tf /opt/deepseek-audit/current.next /opt/deepseek-audit/current
sudo systemctl restart deepseek-audit
curl --fail http://127.0.0.1:8090/readyz
```

不要复制包内示例配置覆盖 `/etc/deepseek-audit.env`。应用二进制回退只需将软链接切回旧目录并重启，但应先确认数据库结构与旧版兼容；涉及不兼容数据库变更时应遵循该版本升级说明，必要时恢复升级前备份。

服务器上备份数据库：

```bash
sudo install -d -m 700 /var/backups/deepseek-audit
sudo bash -c 'umask 077; sudo -u postgres pg_dump -Fc audit > /var/backups/deepseek-audit/audit-$(date +%Y%m%d-%H%M%S).dump'
```

另外加密保存 `/etc/deepseek-audit.env`，与数据库备份分开保管。迁移本地已有数据时，必须同时迁移匹配的数据库备份和 MASTER_KEY，否则原来保存的 DeepSeek Key 无法解密；新部署无需迁移旧数据。

## 常见故障

| 现象 | 检查 |
| --- | --- |
| `Exec format error` | 安装包架构不匹配，重新选择 amd64/arm64 |
| `Permission denied` | audit-server 是否有执行权限，目录是否允许 audit 用户读取，磁盘是否挂载为 noexec |
| peer authentication failed | systemd User=audit，数据库角色 audit，local peer 规则及 Unix socket 路径是否匹配 |
| 502 / 503 或服务启动后退出 | 查看 journalctl；核对数据库连接、MASTER_KEY 格式、静态目录 |
| 登录来源无效 / CSRF 校验失败 | PUBLIC_URL 必须与浏览器访问来源完全一致，修改后重启；反向代理不要改写 Origin |
| 首次密码配置修改后不生效 | ADMIN_PASSWORD 只用于首次建管理员；已有账户在后台改密码 |
| “请先选择有效的 DeepSeek 密钥” | 添加连接密钥和模型通道，在策略中绑定通道，试跑后保存并启用 |
| 接口健康但无法真实审核 | 继续核对 DeepSeek Key、模型、网络与余额；readyz 不测试模型 |

本教程的服务器安装命令尚未在你的目标服务器执行。本地 Linux 交叉编译与压缩包检查可验证构建产物，但不能代替目标服务器的数据库、systemd 和域名联调。

本次多模型实现不提供旧开发数据库迁移。请使用新的空数据库初始化，不要直接复用旧草稿/历史版本表结构。运行实例默认总调用时限 9000 ms，sub2api HTTP 超时建议从 15000 ms 起，重试次数为 0。
