# 已部署实例更新指南

源码仓库：https://github.com/nXiaoK/deepseek-moderation-api ，使用 `main` 分支。以下用于更新已经运行的实例，不要重新执行首次部署的初始化步骤。

本次更新补全审核请求的入口记录：鉴权失败、限流、格式错误、图片拒绝和模型失败也会出现在审核记录中，并显示 HTTP 状态、处理阶段和具体原因。更新不需要修改数据库表结构；沿用现有数据库、策略、调用方密钥和管理员账户。历史漏记的请求无法补回。

## 更新前保留

- 通过现有 PostgreSQL / 1Panel 备份功能备份应用数据库。
- 保存当前 `.env` 或编排环境变量，尤其是 `MASTER_KEY` 和 `DATABASE_URL`。沿用原值，不要运行初始化脚本生成新的主密钥；主密钥用于解密既有连接密钥和输入记录。
- 记录当前镜像标签或二进制版本目录，保留旧版本用于回退。

以下三种方式只选择与你现有部署一致的一种。更新应用容器或服务会有短暂中断；不要删除数据库或运行 `docker compose down -v`。

## A. 1Panel 导入镜像 / 外部 PostgreSQL

### 本地构建

首次从 GitHub 获取源码：

```bash
git clone https://github.com/nXiaoK/deepseek-moderation-api.git
cd deepseek-moderation-api
```

如果已有对应 Git 仓库，先确认工作区没有未提交修改，再更新：

```bash
git pull --ff-only origin main
```

在运行 Docker 的本地电脑中，为本次更新选一个未使用的版本号，例如：

```bash
bash deploy/package-1panel.sh audit-records-v1
```

生成 `dist/deepseek-audit-audit-records-v1-1panel-amd64.tar.gz`。这是 Debian / Linux x64 镜像；不要用于 ARM64 服务器。后续更新换一个版本号。

### 服务器更新

1. 上传压缩包到新目录并解压，在解压目录执行 `sha256sum -c SHA256SUMS`。
2. 在 1Panel「容器 → 镜像」导入包内的 `image.tar`，或在解压目录运行 `docker load -i image.tar`。
3. 编辑**现有**审计服务编排，只把应用的 `image` 改成 `deepseek-audit:1panel-amd64-audit-records-v1`。保留原有环境变量、数据库地址、网络、端口和挂载；不要用新包的示例配置覆盖现有生产配置，也不需要创建新数据库。
4. 保留 `pull_policy: never`，使用面板的重新部署 / 重建操作，令容器使用新镜像。只点击“重启”不会切换镜像。

如果原来用命令行管理编排，在**原编排目录**修改原 Compose 文件后执行（文件名、环境文件名和项目名以原部署为准）：

```bash
docker compose --env-file .env -p deepseek-audit -f compose.yaml up -d --no-deps --force-recreate app
```

不要更改原 Compose 项目名。如果出现问题，把 `image` 改回保留的旧标签，再重新部署；不要恢复或重建数据库来回退本次应用更新。

## B. Docker Compose 从源码构建

在原有源码和 Compose 目录操作，保留原 `.env`、Compose 项目名和数据库卷。先记录原镜像 ID，并给它添加一个未使用的备份标签：

```bash
docker compose images app
docker image tag <上一步的应用镜像ID> deepseek-audit:before-audit-records-v1
git pull --ff-only origin main
docker compose build app
docker compose up -d --no-deps app
docker compose logs --tail=100 app
```

`<上一步的应用镜像ID>` 需替换后再执行。构建先完成，随后才替换运行中的应用，数据库容器保持运行。

如果生产目录不是 Git 仓库，不要直接执行 `git pull`；可以在另一目录克隆源码并运行 `docker build -t deepseek-audit:audit-records-v1 .`，然后在原编排中把应用指向这个镜像并重建应用服务。保留原部署目录和环境文件。

回退时，在原编排目录新建只覆盖应用镜像的文件 `rollback.yaml`：

```yaml
services:
  app:
    image: deepseek-audit:before-audit-records-v1
    pull_policy: never
```

使用原项目名、环境文件和主 Compose 文件，增加覆盖文件并禁止构建：

```bash
docker compose -f compose.yaml -f rollback.yaml up -d --no-deps --no-build app
```

## C. 原生二进制 + systemd

在本地源码目录拉取更新，为服务器架构打包（x64 使用 `amd64`，ARM64 使用 `arm64`）：

```bash
git pull --ff-only origin main
bash deploy/package.sh amd64 audit-records-v1
```

上传 `dist/` 中对应的 `.tar.gz` 和 `.sha256` 到服务器临时目录。下列路径适用于现有 [原生部署文档](DEPLOY_NATIVE.zh.md) 的布局；自定义安装路径应使用实际路径。

```bash
sha256sum -c deepseek-audit-audit-records-v1-linux-amd64.tar.gz.sha256
readlink -f /opt/deepseek-audit/current
sudo tar -xzf deepseek-audit-audit-records-v1-linux-amd64.tar.gz -C /opt/deepseek-audit/releases
sudo ln -s /opt/deepseek-audit/releases/deepseek-audit-audit-records-v1-linux-amd64 /opt/deepseek-audit/current.next
sudo mv -Tf /opt/deepseek-audit/current.next /opt/deepseek-audit/current
sudo systemctl restart deepseek-audit
sudo systemctl status deepseek-audit --no-pager
```

记录 `readlink` 输出的旧目录以便回退。继续使用 `/etc/deepseek-audit.env`，不要重新运行 `init-native-env.sh` 或替换生产主密钥。若服务启动失败，将 `current` 链接恢复到原目录，再重启服务。

## 更新后验证

```bash
curl --fail http://127.0.0.1:8090/readyz
```

如果原部署映射了不同端口，使用实际地址。返回 `{"ok":true}` 表示应用和数据库已连通；这不等于模型调用已验证。

刷新审计后台，确认已有策略和调用方密钥仍在。通过 sub2api 发起一条纯文本请求，再发起一条含图片的请求，检查两条新记录：图片请求应显示 HTTP 400、输入校验阶段和“请求包含图片，尚未调用审核模型”，而不是完全没有记录。sub2api 的采样或审核范围可能使请求不发往本服务；仅实际到达审核接口的请求才会记录。

本次只更新审计服务，不需要重新部署 sub2api。图片请求仍会被拒绝，本次改动是补全记录和错误原因。
