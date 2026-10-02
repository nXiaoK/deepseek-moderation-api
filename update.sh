#!/usr/bin/env bash
# Preserve the original Compose identity, credentials and PostgreSQL volume.
set -Eeuo pipefail
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd "$ROOT_DIR"
source "$ROOT_DIR/deploy/scripts-common.sh"
PULL=1
case ${1:-} in
  --help|-h) printf '用法：./update.sh [--no-pull]\n默认快进 origin/main；--no-pull 只构建当前已检出的源码。\n'; exit 0 ;;
  --no-pull) [[ $# == 1 ]] || fail '用法：./update.sh [--no-pull]'; PULL=0 ;;
  '') [[ $# == 0 ]] || fail '用法：./update.sh [--no-pull]' ;;
  *) fail '用法：./update.sh [--no-pull]' ;;
esac
require_tools docker git python3 mktemp
[[ -f .env && ! -L .env && -f compose.yaml ]] || fail '需要原部署目录的 .env 和 compose.yaml，不要在新克隆目录更新'
[[ $(git rev-parse --show-toplevel) == "$ROOT_DIR" ]] || fail '请在项目 Git 根目录运行'
[[ -z $(git status --porcelain) ]] || fail 'Git 工作区有未提交改动，请先处理；脚本不会丢弃或暂存你的改动'
if (( PULL )); then
  [[ $(git branch --show-current) == main ]] || fail '默认更新仅支持 main 分支；其他已检出版本可使用 --no-pull'
fi
acquire_lock
BACKUP_DIR=''
APP_STOPPED=0
DEPLOY_STARTED=0
UPDATE_OK=0
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  if (( ! UPDATE_OK && APP_STOPPED )); then
    if (( ! DEPLOY_STARTED )); then
      printf '更新未切换镜像，正在重新启动原应用。\n' >&2
      docker start "$APP_CONTAINER" >/dev/null || printf '原应用重启失败，请人工处理。\n' >&2
    else
      # SQL migrations cannot safely be reversed by just restoring an image.
      printf '新应用启动失败，尝试切回备份镜像；不会自动覆盖数据库。\n' >&2
      if docker compose --project-directory "$ROOT_DIR" --env-file "$BACKUP_DIR/.env" \
        -p "$PROJECT" -f "$BACKUP_DIR/rollback.yaml" up -d --no-deps --no-build --pull never --force-recreate app; then
        local restored
        restored=$(docker compose -p "$PROJECT" -f "$BACKUP_DIR/rollback.yaml" ps -q app) || restored=''
        if [[ -n $restored ]] && wait_healthy "$restored"; then
          printf '已恢复旧镜像并通过健康检查，但数据库迁移仍保留；核对兼容性后再开放服务。\n' >&2
        else
          printf '旧镜像也未通过健康检查，可能需要按 UPDATING.md 恢复数据库备份。\n' >&2
        fi
      else
        printf '镜像回退失败，请按 UPDATING.md 人工恢复。\n' >&2
      fi
    fi
  fi
  [[ -z $BACKUP_DIR ]] || printf '备份目录：%s（包含密钥，请妥善保管）\n' "$BACKUP_DIR"
  rmdir "$LOCK_DIR" || true
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Read identities from existing containers, never guess the old project name.
identity=$(python3 "$ROOT_DIR/deploy/compose_guard.py" discover "$ROOT_DIR")
IFS=$'\n' read -r -d '' PROJECT APP_CONTAINER DB_CONTAINER DATA_VOLUME <<< "$identity" || true
[[ -n $PROJECT && -n $APP_CONTAINER && -n $DB_CONTAINER && -n $DATA_VOLUME ]] || fail '无法识别原部署'
[[ $(compose ps --all -q app) == "$APP_CONTAINER" && $(compose ps --all -q db) == "$DB_CONTAINER" ]] || fail '当前 Compose 没有指向原部署'

backup_root=${AUDIT_BACKUP_ROOT:-"$ROOT_DIR/backups"}
python3 "$ROOT_DIR/deploy/compose_guard.py" backup-path "$ROOT_DIR" "$backup_root" "$DB_CONTAINER"
mkdir -p -- "$backup_root"
backup_root=$(cd -- "$backup_root" && pwd -P)
[[ $backup_root != "$ROOT_DIR" ]] || fail '备份目录不能是源码根目录'
# Custom in-repository backup paths must be ignored, or future updates would fail.
git check-ignore -q "$backup_root/backup-probe" 2>/dev/null || \
  python3 -c 'import os,sys; sys.exit(os.path.commonpath(sys.argv[1:]) == sys.argv[1])' "$ROOT_DIR" "$backup_root" || \
  fail '仓库内的自定义备份目录必须被 Git 忽略'
BACKUP_DIR=$(mktemp -d "$backup_root/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")
chmod 700 "$BACKUP_DIR"
cp .env "$BACKUP_DIR/.env"
cp compose.yaml "$BACKUP_DIR/compose.yaml"
cp "$ROOT_DIR/deploy/compose_guard.py" "$BACKUP_DIR/compose_guard.py"
GUARD="$BACKUP_DIR/compose_guard.py"
compose config --no-env-resolution --format json > "$BACKUP_DIR/compose.json"
python3 "$GUARD" current "$BACKUP_DIR/compose.json" "$APP_CONTAINER" "$DB_CONTAINER" "$DATA_VOLUME"
old_revision=$(git rev-parse HEAD)
old_image=$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")
backup_tag="deepseek-audit-backup:$(date -u +%Y%m%dT%H%M%SZ)-$$"
docker image tag "$old_image" "$backup_tag"
python3 "$GUARD" rollback "$BACKUP_DIR/compose.json" "$backup_tag" > "$BACKUP_DIR/rollback.yaml"
printf 'revision=%s\nproject=%s\napp_image=%s\ndatabase_volume=%s\n' \
  "$old_revision" "$PROJECT" "$backup_tag" "$DATA_VOLUME" > "$BACKUP_DIR/RELEASE.txt"
if (( PULL )); then
  git fetch origin main || fail '获取源码失败，原应用未停止'
  git merge --ff-only FETCH_HEAD || fail '无法快进源码；请手动处理分支，原应用未停止'
fi
# Refuse deployment changes that could switch credentials, ports or the database.
compose config --no-env-resolution --format json > "$BACKUP_DIR/target-compose.json"
python3 "$GUARD" target "$BACKUP_DIR/compose.json" "$BACKUP_DIR/target-compose.json"
compose build app || fail '镜像构建失败，原应用未停止；源码可能已快进，排查后重新运行即可'
# Quiesce the only supported writer before taking a consistent logical backup.
APP_STOPPED=1
compose stop -t 30 app || fail '无法停止原应用，未切换镜像'
docker exec "$DB_CONTAINER" pg_dump -U audit -d audit -Fc > "$BACKUP_DIR/database.dump.tmp" || fail '数据库备份失败，未切换镜像'
[[ -s $BACKUP_DIR/database.dump.tmp ]] || fail '数据库备份为空，未切换镜像'
# Verify that the archive has a readable table of contents before deploying.
docker exec -i "$DB_CONTAINER" pg_restore --list < "$BACKUP_DIR/database.dump.tmp" > /dev/null || fail '数据库备份格式验证失败，未切换镜像'
mv "$BACKUP_DIR/database.dump.tmp" "$BACKUP_DIR/database.dump"
DEPLOY_STARTED=1
compose up -d --no-deps --no-build --pull never --force-recreate app || fail '应用重建失败'
container=$(compose ps -q app)
[[ -n $container ]] && wait_healthy "$container" || fail '新应用未通过健康检查'
UPDATE_OK=1
printf '\n更新完成，应用与数据库已通过健康检查。\n源码：%s → %s\n原主密钥、管理员配置、Compose 项目和数据库卷均保留；数据库容器未重建。\n' \
  "$old_revision" "$(git rev-parse HEAD)"
