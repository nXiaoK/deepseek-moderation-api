#!/usr/bin/env bash
# Check origin/main without changing HEAD; delegate deployment to update.sh.
set -Eeuo pipefail
umask 077
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd "$ROOT_DIR"
log() { printf '[自动升级] %s\n' "$*"; }
fail() { log "错误：$*" >&2; exit 1; }
case ${1:-} in
  --help|-h) printf '用法：./auto-update.sh\n检查 origin/main；有新提交或上次升级失败时执行 ./update.sh。\n'; exit 0 ;;
  '') [[ $# == 0 ]] || fail '用法：./auto-update.sh' ;;
  *) fail '用法：./auto-update.sh' ;;
esac
for tool in git flock timeout; do
  command -v "$tool" >/dev/null 2>&1 || fail "缺少 $tool；Debian 请安装 git、util-linux 和 coreutils"
done
[[ $(git rev-parse --show-toplevel) == "$ROOT_DIR" ]] || fail '必须位于项目 Git 根目录'
[[ -f .env && ! -L .env && -f compose.yaml && -x update.sh ]] || fail '需要已有 Docker Compose 部署的 .env、compose.yaml 和可执行 update.sh'
git_dir=$(git rev-parse --absolute-git-dir)
# The file remains in .git so neither state nor the lock dirties the checkout.
exec 8>"$git_dir/audit-auto-update.lock"
if ! flock -n 8; then
  log '另一个自动检查正在运行，本次跳过。'
  exit 0
fi
check_checkout() {
  [[ $(git branch --show-current) == main ]] || fail '自动升级仅支持 main 分支'
  [[ -z $(git status --porcelain) ]] || fail 'Git 工作区有未提交改动，请先人工处理'
}
check_checkout
if [[ -e .deploy.lock ]]; then
  log '安装或更新锁已存在，本次跳过；异常遗留的锁需要人工处理。'
  exit 0
fi
export GIT_TERMINAL_PROMPT=0
# Use a private ref so a concurrent manual update's FETCH_HEAD is untouched.
timeout 120 git fetch --no-tags --no-write-fetch-head origin \
  '+refs/heads/main:refs/auto-update/main' || fail '获取 origin/main 失败或超过 120 秒，下次定时检查会重试'
check_checkout
local_revision=$(git rev-parse HEAD)
remote_revision=$(git rev-parse refs/auto-update/main)
git merge-base --is-ancestor "$local_revision" "$remote_revision" || \
  fail '本地 main 领先远端或历史已分叉，不能自动快进，请人工处理'
pending="$git_dir/audit-auto-update.pending"
[[ ! -L $pending ]] || fail '升级状态文件不能是符号链接'
if [[ $local_revision == "$remote_revision" && ! -e $pending ]]; then
  log "源码没有新提交（${local_revision}），无需升级。"
  exit 0
fi
if [[ -e .deploy.lock ]]; then
  log '安装或更新已开始，本次跳过。'
  exit 0
fi
if [[ -e $pending ]]; then
  log '上次升级未完成，重新执行 ./update.sh。'
else
  log "发现新提交：${local_revision} → ${remote_revision}，执行 ./update.sh。"
fi
# update.sh can fast-forward HEAD before a failed build. Remember the attempt
# until deployment succeeds so equal Git revisions never hide that failure.
printf '%s\n' "$remote_revision" > "$pending"
if ./update.sh; then
  rm -- "$pending"
  log "升级成功，当前源码：$(git rev-parse HEAD)"
else
  result=$?
  log '升级失败，保留重试标记；查看更新日志，下次检查会重试。' >&2
  exit "$result"
fi
