#!/usr/bin/env bash
# Register the hourly check with the Debian host's systemd, not a container.
set -Eeuo pipefail
umask 077
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd "$ROOT_DIR"
fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
MODE=install
RUN_USER=''
while (( $# )); do
  case $1 in
    --help|-h)
      printf '用法：sudo ./install-auto-update.sh [--user 用户]\n      ./install-auto-update.sh --print [--user 用户]\n      sudo ./install-auto-update.sh --uninstall\n默认以部署目录所有者运行，每小时检查一次；--print 仅预览 unit。\n'
      exit 0 ;;
    --user) [[ $# -ge 2 && -n $2 ]] || fail '--user 需要用户名'; RUN_USER=$2; shift 2 ;;
    --print|--uninstall) [[ $MODE == install ]] || fail '不能组合 --print 与 --uninstall'; MODE=${1#--}; shift ;;
    *) fail "未知参数：$1" ;;
  esac
done
command -v python3 >/dev/null 2>&1 || fail '需要 Python 3'
[[ -x auto-update.sh && -x update.sh ]] || fail '找不到可执行的 auto-update.sh / update.sh'
UNIT_DIR=/etc/systemd/system
UNIT_NAME=deepseek-audit-auto-update
MARKER='# Managed by deepseek-moderation-api install-auto-update.sh'
temp_dir=$(mktemp -d)
trap 'rm -rf -- "$temp_dir"' EXIT
python3 - "$ROOT_DIR" "$RUN_USER" "$temp_dir" <<'PY'
import os
import pathlib
import pwd
import re
import sys

root, user, output = sys.argv[1:]
user = user or pwd.getpwuid(os.stat(root).st_uid).pw_name
if not re.fullmatch(r"[a-zA-Z_][a-zA-Z0-9_.-]*\$?", user):
    sys.exit("运行用户名格式无效")
pwd.getpwnam(user)
if any(ord(c) < 32 or ord(c) == 127 for c in root) or root != root.rstrip():
    sys.exit("部署目录不能包含控制字符或以空白结尾")

def quote(value, command=False):
    value = value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%")
    if command:
        value = value.replace("$", "$$")
    return '"' + value + '"'

marker = "# Managed by deepseek-moderation-api install-auto-update.sh\n"
service = marker + f"""[Unit]
Description=Check and upgrade DeepSeek audit Docker deployment
Wants=network-online.target docker.service
After=network-online.target docker.service

[Service]
Type=oneshot
User={user}
# WorkingDirectory takes a literal path, not an ExecStart-style quoted word.
WorkingDirectory={root.replace('%', '%%')}
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment=GIT_TERMINAL_PROMPT=0
ExecStart=/bin/bash {quote(root + '/auto-update.sh', command=True)}
TimeoutStartSec=infinity
UMask=0077
StandardOutput=journal
StandardError=journal
"""
timer = marker + """[Unit]
Description=Hourly check for DeepSeek audit source updates

[Timer]
OnCalendar=hourly
Persistent=true
RandomizedDelaySec=60
AccuracySec=1s
Unit=deepseek-audit-auto-update.service

[Install]
WantedBy=timers.target
"""
for suffix, text in (("service", service), ("timer", timer)):
    pathlib.Path(output, f"deepseek-audit-auto-update.{suffix}").write_text(text)
PY
if [[ $MODE == print ]]; then
  cat "$temp_dir/$UNIT_NAME.service" "$temp_dir/$UNIT_NAME.timer"
  exit 0
fi
[[ $(uname -s) == Linux && -d /run/systemd/system ]] || fail '需要运行 systemd 的 Debian / Linux 宿主机'
[[ $EUID == 0 ]] || fail '安装或卸载定时任务需要 root；请使用 sudo'
for tool in systemctl install git flock timeout runuser; do
  command -v "$tool" >/dev/null 2>&1 || fail "缺少 $tool"
done
# Refuse to overwrite an unrelated service or silently switch its checkout.
for suffix in service timer; do
  unit="$UNIT_DIR/$UNIT_NAME.$suffix"
  [[ ! -L $unit ]] || fail "不覆盖符号链接：$unit"
  if [[ -e $unit ]]; then
    grep -Fxq "$MARKER" "$unit" || fail "已有其他程序管理 $unit"
  fi
done
if [[ -f $UNIT_DIR/$UNIT_NAME.service ]]; then
  directory_line=$(grep '^WorkingDirectory=' "$temp_dir/$UNIT_NAME.service")
  grep -Fxq "$directory_line" "$UNIT_DIR/$UNIT_NAME.service" || fail '定时任务已绑定其他部署目录，请先在原目录卸载'
fi
if [[ $MODE == uninstall ]]; then
  systemctl disable --now "$UNIT_NAME.timer"
  # Do not interrupt an update that is already backing up or deploying.
  rm -f -- "$UNIT_DIR/$UNIT_NAME.service" "$UNIT_DIR/$UNIT_NAME.timer"
  systemctl daemon-reload
  printf '每小时自动检查已卸载；已经开始的升级会继续完成。\n'
  exit 0
fi
[[ -f .env && ! -L .env && -f compose.yaml ]] || fail '请在已有 Docker Compose 部署目录安装'
service_user=$(sed -n 's/^User=//p' "$temp_dir/$UNIT_NAME.service")
# Expand in the target user's shell; pass the directory as a positional argument.
# shellcheck disable=SC2016
runuser -u "$service_user" -- env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin /bin/bash -c '
  set -e
  cd -- "$1"
  [[ $(git rev-parse --show-toplevel) == "$PWD" && $(git branch --show-current) == main ]]
  [[ -z $(git status --porcelain) && -r .env && -w . && -w $(git rev-parse --absolute-git-dir) ]]
  docker compose version >/dev/null
  docker info >/dev/null
' bash "$ROOT_DIR" || fail '运行用户必须能读写原部署目录、访问 Docker，并使用干净的 main 分支'
install -m 644 "$temp_dir/$UNIT_NAME.service" "$UNIT_DIR/$UNIT_NAME.service"
install -m 644 "$temp_dir/$UNIT_NAME.timer" "$UNIT_DIR/$UNIT_NAME.timer"
systemctl daemon-reload
systemctl enable --now "$UNIT_NAME.timer"
printf '已启用每小时自动检查 origin/main。\n查看计划：systemctl list-timers %s.timer\n查看日志：journalctl -u %s.service\n立即检查：sudo systemctl start %s.service\n' \
  "$UNIT_NAME" "$UNIT_NAME" "$UNIT_NAME"
