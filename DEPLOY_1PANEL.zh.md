# 本地打包，上传 1Panel，连接外部 PostgreSQL

适用于 Debian x64（Docker 架构名 linux/amd64）。本地需要运行中的 Docker；服务器需要 1Panel 的容器管理功能。镜像包含后端和前端，不包含数据库或本地 .env。镜像内部采用项目现有的 Alpine 基础镜像，可在 Debian Docker 上运行。

## 1. 本地打包

```bash
bash deploy/package-1panel.sh
```

输出 `dist/deepseek-audit-时间戳-1panel-amd64.tar.gz`。包内包含 `image.tar`、`compose.yaml`、`.env.example`、本说明和 `SHA256SUMS`。后续更新重新打包，镜像标签自动包含版本号。

## 2. 上传并导入镜像

通过 1Panel 文件管理将整个压缩包上传到服务器，例如 `/opt/deepseek-audit-upload`，并解压。在解压后的目录运行 `sha256sum -c SHA256SUMS` 校验。

在 1Panel 的「容器 → 镜像」中使用导入功能，选择解压得到的 **image.tar**。外层 `.tar.gz` 是交付包，不是 Docker 镜像文件。不同面板版本的入口名称可能略有不同；也可以在面板终端执行：

```bash
docker load -i /实际解压路径/image.tar
```

## 3. 配置环境变量和外部数据库

在外部 PostgreSQL 中创建本应用专用的空数据库及拥有该库建表、读写权限的用户，并允许应用服务器连接。不要复用其他应用的库或本项目旧开发版的草稿/历史版本数据库。

在面板终端分别执行，保存输出作为主密钥和初始管理员密码：

```bash
openssl rand -base64 32
openssl rand -hex 24
```

将 `.env.example` 复制为同目录 `.env`，在面板文件编辑器中填写：

```dotenv
DATABASE_URL='postgres://audit:数据库密码@数据库地址:5432/audit?sslmode=verify-full&sslrootcert=/etc/ssl/certs/ca-certificates.crt'
MASTER_KEY=第一条命令生成的值
ADMIN_USER=admin
ADMIN_PASSWORD=第二条命令生成的值
PUBLIC_URL=https://audit.example.com
AUDIT_SUB2API_ORIGINS=
```

将 `.env` 权限设置为 600。也可以使用 1Panel 编排提供的环境变量设置，填写相同变量。变量设置页面填写值时无需包裹单引号；上述单引号属于 `.env` 文件语法。

数据库密码中的 `@`、`#`、`%` 等特殊字符需 URL 编码。TLS 示例使用镜像内置的系统 CA；供应商使用专用 CA 时，需要将 CA 文件只读挂载进容器，并让 `sslrootcert` 指向容器内路径。遵循供应商提供的连接参数。仅在明确使用可信私网且数据库未启用 TLS 时，按实际情况使用 `sslmode=disable`。

**容器中的 127.0.0.1 指向应用容器自身。** 若数据库运行在同一服务器，可以使用 `host.docker.internal` 和数据库发布到宿主机的端口；数据库必须监听容器可达地址，仅绑定宿主机 127.0.0.1 的数据库端口无法通过此方式访问。若 PostgreSQL 也是 1Panel 容器，可将应用加入数据库所在 Docker 网络，使用数据库容器名和容器端口 5432。跨服务器数据库直接使用其可达域名/IP。

## 4. 创建编排并启动

在「容器 → 编排」中新建编排，名称例如 `deepseek-audit`。优先选择从路径创建，使用上传目录中的 `compose.yaml`，确保 `.env` 位于同目录并被编排读取。若你的面板只有编辑器方式，粘贴 `compose.yaml`，并在其环境变量设置中填写上述变量。

编排只运行应用，不会安装 PostgreSQL。`pull_policy: never` 表示使用已导入的本地镜像。若面板报环境变量缺失，先检查其编排实际工作目录和环境变量设置。

也可在面板终端进入解压目录执行（显式指定环境文件）：

```bash
docker compose --env-file .env -p deepseek-audit -f compose.yaml up -d
```

启动后在面板检查容器日志，并在服务器终端验证：

```bash
curl --fail http://127.0.0.1:8090/readyz
```

返回 `{"ok":true}` 表示应用和数据库已连通。首次启动自动建表并创建管理员；此接口不会验证模型 API 是否可用。

## 5. 用 1Panel 网站功能访问

在 1Panel 创建反向代理网站，将域名解析到服务器，配置 HTTPS 证书，设置 `PUBLIC_URL` 为实际 HTTPS 来源，不带末尾斜杠。登录使用 `admin` 及前面生成的密码。

若代理运行在宿主机网络，可将上游设置为 `http://127.0.0.1:8090`。若 1Panel 的 OpenResty 使用桥接容器网络，其 127.0.0.1 指向代理容器：应让应用和代理加入同一 Docker 网络，通过应用服务名和端口 8090 连接，并按实际配置调整编排。不要直接照填回环地址。

没有域名时，将 PUBLIC_URL 设置为 `http://localhost:8090`，重建容器后从本地使用 SSH 隧道：

```bash
ssh -N -L 8090:127.0.0.1:8090 用户@服务器IP
```

浏览器访问 http://localhost:8090。非 localhost 的后台访问要求 HTTPS。

## 更新与备份

重新打包、上传并导入新 image.tar，将现有编排 image 改为新包 compose.yaml 中的镜像标签，然后重新部署。修改环境变量后同样需要重新部署/重建容器，单纯重启不会更新环境变量。

保留原 `.env`，尤其是 MASTER_KEY；数据库和主密钥分别备份。业务配置及记录保存在外部 PostgreSQL，应用不需要持久化卷。ADMIN_PASSWORD 只在首次创建管理员时生效，后续在后台修改密码。
