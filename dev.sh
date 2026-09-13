#!/usr/bin/env bash
# Local development: PostgreSQL + Go API + Vite hot reload (macOS/Linux).
set -euo pipefail

usage() {
  cat <<'EOF'
用法: ./dev.sh [--no-db]

默认启动/复用 deepseek-audit-dev-db，后端监听 127.0.0.1:8090，
Vite 监听 127.0.0.1:5173，浏览器访问 http://localhost:5173。
--no-db  不管理 Docker 数据库，使用 .env 中 DATABASE_URL 对应的现有数据库。
--help   显示帮助。

前端支持热更新；修改 Go 代码后请 Ctrl+C 并重新运行。
退出时停止前后端，保留数据库容器及数据。
EOF
}

manage_db=true
case "${1:-}" in
  '') ;;
  --no-db) manage_db=false ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 1 ;;
esac
[[ $# -le 1 ]] || { usage >&2; exit 1; }

fail() { echo "错误: $*" >&2; exit 1; }
for dependency in go node pnpm curl; do
  command -v "$dependency" >/dev/null || fail "缺少 $dependency；需要 Go 1.26+、Node 20.19+ 和 pnpm 10。"
done
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$project_root"

# Fail before starting services if either fixed development port is occupied.
node --input-type=module <<'JS'
import net from 'node:net';
for (const port of [8090, 5173]) {
  await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => server.close(resolve));
  }).catch(error => {
    console.error(`无法使用开发端口 127.0.0.1:${port}: ${error.code}`);
    process.exit(1);
  });
}
JS

if [[ ! -e .env ]]; then
  go run ./cmd/setup
fi
# .env is the project's trusted shell configuration, as in the manual workflow.
set -a
source ./.env
set +a
[[ -n "${MASTER_KEY:-}" ]] || fail ".env 中缺少 MASTER_KEY。"
[[ -n "${ADMIN_PASSWORD:-}" ]] || fail ".env 中缺少 ADMIN_PASSWORD。"
[[ -n "${DATABASE_URL:-}" ]] || fail ".env 中缺少 DATABASE_URL。"

# Keep the browser origin, Vite proxy, and API listener in sync without editing .env.
export PUBLIC_URL=http://localhost:5173
export LISTEN_ADDR=127.0.0.1:8090

default_database_url='postgres://audit@127.0.0.1:55439/audit?sslmode=disable'
if [[ "$manage_db" == true && "$DATABASE_URL" == "$default_database_url" ]]; then
  command -v docker >/dev/null || fail "缺少 Docker；使用现有 PostgreSQL 时请加 --no-db。"
  docker info >/dev/null 2>&1 || fail "无法连接 Docker，请先启动 Docker Desktop / OrbStack。"
  db_container=deepseek-audit-dev-db
  if docker container inspect "$db_container" >/dev/null 2>&1; then
    # Reuse the container documented in README; never replace or delete its data.
    db_binding="$(docker inspect --format '{{range (index .HostConfig.PortBindings "5432/tcp")}}{{.HostIp}}:{{.HostPort}}{{end}}' "$db_container")"
    [[ "$db_binding" == '127.0.0.1:55439' ]] || fail "$db_container 端口配置不匹配；请检查容器，或使用 --no-db。"
    docker start "$db_container" >/dev/null
  else
    docker run -d --name "$db_container" \
      -e POSTGRES_USER=audit -e POSTGRES_DB=audit \
      -e POSTGRES_HOST_AUTH_METHOD=trust \
      -p 127.0.0.1:55439:5432 postgres:17-alpine >/dev/null
  fi
  echo '等待本地 PostgreSQL 就绪…'
  db_ready=false
  for ((attempt = 0; attempt < 60; attempt++)); do
    if docker exec "$db_container" pg_isready -U audit -d audit >/dev/null 2>&1; then
      db_ready=true
      break
    fi
    sleep 1
  done
  [[ "$db_ready" == true ]] || fail "PostgreSQL 未在 60 秒内就绪，请查看 docker logs $db_container。"
else
  echo '使用 .env 中配置的 PostgreSQL（不管理 Docker 容器）。'
fi

echo '安装前端依赖…'
pnpm --dir frontend install --frozen-lockfile

stage="$(mktemp -d "${TMPDIR:-/tmp}/deepseek-audit-dev.XXXXXX")"
backend_pid=''
frontend_pid=''
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  for pid in "$frontend_pid" "$backend_pid"; do
    if [[ -n "$pid" ]]; then kill -TERM "$pid" 2>/dev/null || true; fi
  done
  for pid in "$frontend_pid" "$backend_pid"; do
    if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
  done
  rm -rf "$stage"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo '编译并启动 Go 后端…'
go build -o "$stage/audit-server" ./cmd/server
"$stage/audit-server" &
backend_pid=$!
backend_ready=false
for ((attempt = 0; attempt < 60; attempt++)); do
  kill -0 "$backend_pid" 2>/dev/null || fail '后端启动失败，请查看上方日志。'
  if curl --noproxy '*' --fail --silent --max-time 1 http://127.0.0.1:8090/readyz >/dev/null; then
    backend_ready=true
    break
  fi
  sleep 1
done
[[ "$backend_ready" == true ]] || fail '后端未在等待期限内就绪，请检查数据库配置和上方日志。'

# Launch Node directly so the tracked PID is Vite itself, not a package-manager wrapper.
(
  cd frontend
  exec node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5173 --strictPort
) &
frontend_pid=$!

echo '管理后台: http://localhost:5173（Vite 就绪后可访问）'
echo '审核 API: http://127.0.0.1:8090'
echo '登录信息见 .env 的 ADMIN_USER / ADMIN_PASSWORD（已有账号以数据库中的密码为准）。'
echo 'Ctrl+C 停止前后端；数据库保留运行。修改 Go 代码后需重新运行本脚本。'
while true; do
  kill -0 "$backend_pid" 2>/dev/null || fail '后端已退出。'
  kill -0 "$frontend_pid" 2>/dev/null || fail '前端已退出。'
  sleep 1
done
