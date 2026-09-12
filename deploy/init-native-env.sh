#!/usr/bin/env bash
# Run on the server. PostgreSQL local peer authentication maps the OS account
# 'audit' to the database role 'audit'. Existing configuration is never replaced.
set -euo pipefail
origin="${1:-http://localhost:8090}"
output="${2:-/etc/deepseek-audit.env}"
if [[ "$origin" != http://localhost:8090 && ! "$origin" =~ ^https://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]]; then
  echo 'Use http://localhost:8090 for an SSH tunnel, or https://your-domain without a trailing slash.' >&2
  exit 1
fi
command -v openssl >/dev/null || { echo 'Install openssl first.' >&2; exit 1; }
master_key="$(openssl rand -base64 32)"
admin_password="$(openssl rand -hex 24)"
umask 077
# noclobber also prevents accidental overwrites when two initializers run.
set -o noclobber
cat > "$output" <<EOF
MASTER_KEY=$master_key
ADMIN_USER=admin
ADMIN_PASSWORD=$admin_password
DATABASE_URL='postgres://audit@/audit?host=/var/run/postgresql&sslmode=disable'
PUBLIC_URL=$origin
LISTEN_ADDR=127.0.0.1:8090
STATIC_DIR=/opt/deepseek-audit/current/frontend/dist
EOF
printf 'Created %s (mode 600). Read ADMIN_PASSWORD there to sign in. Keep MASTER_KEY backed up.\n' "$output"
