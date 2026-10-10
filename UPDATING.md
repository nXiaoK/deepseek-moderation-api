# 快捷安装与更新

根目录 `install.sh` / `update.sh` 借鉴 `quota-watch` 的操作方式，为本项目的标准 Docker Compose 部署提供安装、备份、构建、健康检查及失败处理。

> 只接管仓库 `compose.yaml` 的 `app` + `db` 部署，以及 PostgreSQL 的单个命名数据卷。1Panel、外部 PostgreSQL、原生 systemd、绑定目录或额外 Compose 覆盖文件，继续按 [UPGRADE.zh.md](UPGRADE.zh.md) 操作。脚本不会自动安装系统依赖、设置域名、申请证书、推送 Git 或清理数据库卷。

## 1. 首次安装

宿主机需要 Bash、Git、Docker、Docker Compose v2（需支持 `config --no-env-resolution`）、Python 3、OpenSSL；不需要 Go、Node 或 pnpm，前后端均在 Docker 中构建。当前用户必须能访问 Docker daemon。

```bash
git clone https://github.com/nXiaoK/deepseek-moderation-api.git
cd deepseek-moderation-api
./install.sh
```

安装脚本：

- 将 Compose 项目名固定为 `deepseek-audit`，避免首次安装因为目录名称不同而改变数据卷名称。
- 生成 32 字节加密主密钥、随机管理员密码和随机数据库密码，原子写入权限为 `600` 的 `.env`。不会把凭据输出到终端日志。
- 发现已有 `.env`、部署容器或 `audit-postgres` 数据卷时拒绝生成新凭据；不会覆盖旧密钥或接管旧数据库。
- 构建前后端、启动 PostgreSQL 和应用，等待应用健康检查通过（最多约 3 分钟；构建时间另计）。

打开 `http://localhost:8090`，初始账号为 `admin`，密码从本地 `.env` 的 `ADMIN_PASSWORD` 读取。管理员密码只在数据库首次初始化时生效，后续后台修改的密码不会被更新脚本覆盖。`MASTER_KEY` 用于解密已存连接密钥和输入记录，必须与数据库一起长期保留。

首次安装构建或启动失败时，先查看日志、修复环境，再运行：

```bash
./install.sh --resume
```

`--resume` 只接受本安装脚本生成的 `.env`，不会重新生成凭据；已有容器必须来自同一个目录和项目，环境变量不能与配置不一致。不要为了解决构建失败而删除 `.env` 或数据库卷。

### 远程访问

默认端口只绑定服务器 `127.0.0.1:8090`。无域名时在本机执行：

```bash
ssh -N -L 8090:127.0.0.1:8090 deploy@SERVER_IP
```

然后访问 `http://localhost:8090`。公网使用请设置 HTTPS 反向代理，并将服务器 `.env` 的 `PUBLIC_URL` 改为实际 HTTPS 来源，不要带路径或尾斜杠。其他环境变量可参考 `.env.example`。非 localhost 的后台访问要求 HTTPS；不要简单把监听端口暴露到公网。参考 [DEPLOY.zh.md](DEPLOY.zh.md) 配置代理。

修改 `.env` 后需要通过原项目重新创建容器应用配置，例如本脚本首次安装的项目：

```bash
docker compose --env-file .env -p deepseek-audit -f compose.yaml up -d --no-deps app
```

已有部署使用实际的原项目名。**不要通过这种方式更换 MASTER_KEY 或 POSTGRES_PASSWORD**：前者会使旧密文无法解密，后者不会自动改变已有数据库角色密码。更新脚本会拒绝尚未应用或意外变化的环境变量。

## 2. 后续更新

在**原服务器、原源码目录**运行：

```bash
./update.sh
```

默认要求 `main` 分支、干净 Git 工作区，执行 `git fetch origin main` 和 `git merge --ff-only FETCH_HEAD`；不会 reset、stash、覆盖本地修改或自动推送。未提交文件（包括非忽略的未跟踪文件）会阻止更新，请先自己处理。`.env`、`backups/` 和部署锁已被 Git 忽略。

如果已经自行更新源码，或明确需要部署当前检出的其他版本：

```bash
./update.sh --no-pull
```

即使源码版本没有变化，也会重新构建和部署，可用于重新尝试之前失败的构建。仍需要干净的 Git 工作区。

### 每小时自动升级（Debian）

已有标准 Docker Compose 部署可在 **Debian 宿主机的原源码目录**安装 systemd timer：

```bash
# 先手动升级到包含这两个新脚本的版本
./update.sh
sudo ./install-auto-update.sh
systemctl list-timers deepseek-audit-auto-update.timer
```

默认以部署目录的所有者运行检查和升级，该用户需要能读写源码与 `.git`、读取 `.env`、访问 Docker，并能无需交互地读取 Git 远端。私有仓库请为该用户配置 Git 凭据或 SSH 密钥。不要只给 root 配置远端凭据，却让另一个用户运行任务。安装前会检查该用户的本地 Git 和 Docker 权限；系统依赖包括 Bash、Git、Python 3、`util-linux`（`flock` / `runuser`）及 `coreutils`（`timeout`）。

需要明确指定已有部署用户时：

```bash
sudo ./install-auto-update.sh --user deploy
# 仅预览生成的 service / timer，不写系统配置、不执行升级
./install-auto-update.sh --print --user deploy
```

timer 每小时整点检查，另加最多 60 秒随机延迟；服务器关机期间错过的检查会在重新启动后补做一次，不补跑每个错过的小时。安装完成后可主动执行一次检查：

```bash
sudo systemctl start deepseek-audit-auto-update.service
journalctl -u deepseek-audit-auto-update.service -n 100 --no-pager
# 持续查看下一次升级的输出
journalctl -u deepseek-audit-auto-update.service -f
```

也可用任务的运行用户手动运行 `./auto-update.sh`。脚本只跟踪 `origin/main`：无新提交时不构建；发现可快进的新提交后执行 `./update.sh`，沿用现有备份、Compose 身份检查和健康检查。分支不是 `main`、工作区存在未提交改动、本地领先远端或分叉时拒绝自动升级。远端获取超过 120 秒或失败时保留当前部署，等待下一小时重试。自动检查之间使用文件锁；已有安装或更新锁时跳过本次检查，不自动删除遗留的 `.deploy.lock`。

升级成功前，在 `.git/audit-auto-update.pending` 保存重试标记。即使 `update.sh` 已快进 Git、随后构建失败，下次也会重试，不会误认为已经部署完成。标记在成功后删除，不影响 Git 工作区。镜像回退、数据库迁移不兼容等仍需按下文人工恢复，定时器不会重置 Git 或自动还原数据库。

自动升级仍会停止应用以备份数据库，并且每次升级保留备份和旧镜像；应按下文说明管理磁盘空间。它只适用于本文件支持的 `app` + `db` 标准 Compose 部署，不用于 1Panel、外部 PostgreSQL 或原生应用部署。

暂时停用或卸载：

```bash
sudo systemctl disable --now deepseek-audit-auto-update.timer
# 重新启用
sudo systemctl enable --now deepseek-audit-auto-update.timer
# 在原源码目录卸载本项目的两个 unit 文件
sudo ./install-auto-update.sh --uninstall
```

停用或卸载只阻止后续检查，已开始的升级会继续完成；不要在数据库备份或容器切换期间强行终止 service。一个宿主机默认只安装一套 `deepseek-audit-auto-update` 定时任务，重复安装可更新同一目录的配置，不能直接覆盖其他部署目录的任务。

### 更新顺序与保护

1. 从容器的 Compose 标签识别原目录、项目名、应用容器、数据库容器和数据卷。兼容原先按目录名创建的项目，不会强制改为新安装的项目名。
2. 要求原应用及数据库已经健康，当前 `.env` / Compose 环境变量与运行中的容器一致。调用 Compose 时清除可能覆盖部署配置的同名 shell 环境变量。
3. 保存 `.env`、原 Compose 配置、渲染配置、源码提交号和旧镜像标签。不会把密钥打印到日志。
4. 快进源码，检查新 Compose 不会更换既有凭据、数据库镜像、数据卷、端口等部署设置。允许新增加 `AUDIT_` 配置默认值；已有环境变量改变需人工核对。
5. **先构建成功，再停止应用**。构建、拉取或检查失败时，原应用继续运行；源码可能已经快进，修复后重新运行脚本即可。
6. 停止应用写入，使用数据库容器中的 `pg_dump -Fc` 保存逻辑备份，并用 `pg_restore --list` 检查归档目录可读。这个检查不是完整恢复演练；生产环境仍应定期验证恢复。要求没有其他服务直接写入该数据库。
7. 仅重建 `app`（`--no-deps --no-build --pull never`），不停止或重建 `db`，不删除数据卷，等待健康检查通过。

应用停机时长包括数据库备份和容器启动等待，数据库越大可能中断越久；请安排合适的维护窗口。

备份或归档检查失败时会重新启动原应用。重建或新应用健康检查失败时会尝试用备份配置切回旧镜像，**脚本仍返回失败**，不会把回退说成更新成功。

健康检查只确认应用和数据库可用，不代表模型供应商、审核策略及计价结果已经验证。更新后仍应登录后台、检查日志并试跑实际业务。

### 备份位置

默认保存在原目录的 `backups/<UTC时间>.<随机后缀>/`，每次单独创建目录，权限为 `700`，文件默认 `600`。也可以指定仓库外的持久目录：

```bash
AUDIT_BACKUP_ROOT=/srv/deepseek-audit-backups ./update.sh
```

自定义仓库内目录必须被 Git 忽略。不要把备份放进数据库的数据目录。

备份包括：

- `.env`：原始凭据；
- `compose.yaml` / `compose.json`：原始和已渲染部署配置；
- `target-compose.json`：目标部署配置（同样包含凭据）；
- `RELEASE.txt`：原 Git 提交、Compose 项目名、备份镜像标签、数据库卷名；
- `database.dump`：切换前的逻辑数据库备份，仅备份验证完成后才发布此文件名；
- `rollback.yaml`：固定旧镜像的完整回退编排（JSON 格式也是有效 YAML）；
- `compose_guard.py`：本次更新使用的安全检查器副本，避免源码更新后检查规则被中途替换。

旧镜像以 `deepseek-audit-backup:<时间>-<进程号>` 标记，**并未导出到备份文件**。Docker 镜像清理仍可能删除它；需要异机恢复时另外保存镜像：`docker image save -o /安全目录/old-image.tar <备份标签>`。备份含敏感信息，应加密保存到异机；脚本不会自动清理备份或镜像，请确认恢复点不再需要后再人工清理。

## 3. 失败恢复与数据库迁移边界

**旧镜像回退不等于数据库回退。** 新应用可能已应用 SQL 迁移；直接运行旧镜像可能因为迁移版本更高而拒绝启动，或者业务逻辑不兼容。脚本不自动恢复数据库，避免覆盖新写入的数据。即使旧镜像恢复健康，也应核对 [UPGRADE.zh.md](UPGRADE.zh.md) 的迁移与计价兼容边界。

镜像回退后的容器由备份 `rollback.yaml` 管理。下一次 `update.sh` 会拒绝直接更新这种状态，需要先人工决定：修复并继续部署目标源码，还是恢复原数据库与原源码。不要删除数据卷作为恢复方式。

### 仅回退镜像（必须确认数据库兼容）

从备份 `RELEASE.txt` 读取实际项目名，用本次备份目录替换下面路径：

```bash
BACKUP=/绝对路径/backups/本次备份目录
PROJECT=原Compose项目名
docker compose --project-directory "$PWD" --env-file "$BACKUP/.env" \
  -p "$PROJECT" -f "$BACKUP/rollback.yaml" \
  up -d --no-deps --no-build --pull never --force-recreate app
docker compose -p "$PROJECT" -f "$BACKUP/rollback.yaml" logs --tail=100 app
```

以上命令在原部署目录运行，不会拉取镜像或更换数据卷。不要在清理旧镜像后再执行。

### 必须恢复数据库时

先确认恢复会丢弃备份之后的业务写入，保存当前数据的额外备份，并确保没有其他写入者。**下面的 `--clean` 会删除归档中相应的数据库对象，仅在明确需要回退数据库时执行。** 在原部署目录先停应用，不要停止 PostgreSQL：

```bash
BACKUP=/绝对路径/backups/本次备份目录
PROJECT=原Compose项目名
docker compose -p "$PROJECT" -f "$BACKUP/rollback.yaml" stop app
DB_CONTAINER=$(docker compose -p "$PROJECT" -f "$BACKUP/rollback.yaml" ps -q db)
# 确认容器ID和备份文件均为本实例！
docker exec -i "$DB_CONTAINER" pg_restore \
  -U audit -d audit --clean --if-exists --no-owner --exit-on-error < "$BACKUP/database.dump"
# 确认恢复无错误后再启动旧镜像：
docker compose --project-directory "$PWD" --env-file "$BACKUP/.env" \
  -p "$PROJECT" -f "$BACKUP/rollback.yaml" \
  up -d --no-deps --no-build --pull never --force-recreate app
```

`pg_restore --clean` 只清理归档包含的对象，不保证删除更新后新增的所有对象；重大结构变更应先在隔离数据库演练，再按迁移说明完整恢复。恢复失败时不要启动应用。原 `.env` 与备份中的主密钥必须一致；恢复数据库后不要换密钥。

恢复完成后，按备份 `RELEASE.txt` 核对源码版本、原 `.env` 以及原 `compose.yaml`，在人工确认兼容的干净源码上构建应用、改回由原 `compose.yaml` 管理，再恢复脚本更新。脚本不会自动重置 Git 工作区。也不要在这个阶段盲目运行 `--no-pull` 部署不兼容的旧版本。

## 4. 常见问题与验证

- **已有 `.env` 但没有容器**：脚本不判断它是否可丢弃。已有手动配置请按 [DEPLOY.zh.md](DEPLOY.zh.md) 启动；本脚本生成的配置使用 `--resume`。
- **主密钥、环境或卷不一致**：先核对原实例，不要删除 `.env` / 卷，也不要用重新安装代替更新。
- **提示额外覆盖文件 / 外部配置**：自动更新无法保证所有外部部署设置已备份，改为手动更新。
- **`.deploy.lock` 已存在**：确认没有其他安装或更新进程运行后才能移除。强制终止进程、断电可能留下锁；锁不提供自动超时删除。
- **Docker 不可连接 / 依赖缺失**：先修复 Docker daemon、权限或安装依赖。脚本不自动执行 sudo 或改变系统软件包。

离线回归测试（使用模拟 Docker / Git，覆盖原部署识别、凭据保护、备份与失败处理）：

```bash
bash -n install.sh update.sh auto-update.sh install-auto-update.sh deploy/scripts-common.sh
python3 -B -m unittest discover -s tests -p 'test_*.py' -v
```
