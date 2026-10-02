#!/usr/bin/env bash
# Shared helpers for the standard Docker Compose deployment only.
set -Eeuo pipefail
umask 077

fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
require_tools() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || fail "缺少 $tool；请先安装后重试"
  done
  docker compose version >/dev/null 2>&1 || fail '需要 Docker Compose v2'
  docker info >/dev/null 2>&1 || fail '无法连接 Docker，请检查服务和当前用户权限'
}
compose() (
  # Never let exported shell variables replace the deployment's credentials.
  unset MASTER_KEY ADMIN_USER ADMIN_PASSWORD POSTGRES_PASSWORD DATABASE_URL \
    PUBLIC_URL LISTEN_ADDR STATIC_DIR AUDIT_SUB2API_ORIGINS \
    AUDIT_MODEL_CONCURRENCY AUDIT_REQUEST_CONCURRENCY AUDIT_REQUEST_BODY_MIB \
    AUDIT_TRIAL_CONCURRENCY AUDIT_MAX_IMAGES AUDIT_INGRESS_RPM \
    AUDIT_INGRESS_IP_RPM AUDIT_TRUSTED_PROXIES COMPOSE_PROJECT_NAME \
    COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_DISABLE_ENV_FILE
  local variable
  while IFS= read -r variable; do
    [[ $variable != AUDIT_* ]] || unset "$variable"
  done < <(compgen -e)
  docker compose --project-directory "$ROOT_DIR" --env-file "$ROOT_DIR/.env" \
    -p "$PROJECT" -f "$ROOT_DIR/compose.yaml" "$@"
)
acquire_lock() {
  LOCK_DIR="$ROOT_DIR/.deploy.lock"
  mkdir "$LOCK_DIR" 2>/dev/null || fail '另一个安装/更新正在运行，或上次异常退出留下 .deploy.lock；确认无进程运行后再移除该目录'
}
wait_healthy() {
  local container=$1 state attempt
  for ((attempt=0; attempt<90; attempt++)); do
    state=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null) || return 1
    [[ $state != healthy ]] || return 0
    [[ $state != unhealthy ]] || return 1
    sleep 2
  done
  return 1
}
