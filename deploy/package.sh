#!/usr/bin/env bash
# Build on macOS/Linux; only compiled binaries and an explicit file allowlist
# enter the archive. Never copy .env, database files, or local credentials.
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
arch="${1:-amd64}"
version="${2:-$(date -u +%Y%m%d-%H%M%S)}"
case "$arch" in amd64|arm64|all) ;; *) echo 'Usage: bash deploy/package.sh [amd64|arm64|all] [version]' >&2; exit 1;; esac
if [[ ! "$version" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]]; then
  echo 'Version may only contain letters, numbers, dot, underscore and hyphen.' >&2
  exit 1
fi
for dependency in go pnpm tar; do command -v "$dependency" >/dev/null || { echo "Missing build tool: $dependency" >&2; exit 1; }; done
mkdir -p dist
archs=("$arch")
if [[ "$arch" == all ]]; then archs=(amd64 arm64); fi
for target in "${archs[@]}"; do
  if [[ -e "dist/deepseek-audit-${version}-linux-${target}.tar.gz" ]]; then
    echo "Release already exists for ${target}; choose a new version." >&2; exit 1
  fi
done
stage="$(mktemp -d "${TMPDIR:-/tmp}/deepseek-audit-release.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

(
  cd frontend
  pnpm install --frozen-lockfile
  pnpm build
)
go test ./...
for target in "${archs[@]}"; do
  name="deepseek-audit-${version}-linux-${target}"
  release="$stage/$name"
  mkdir -p "$release/frontend/dist" "$release/deploy"
  CGO_ENABLED=0 GOOS=linux GOARCH="$target" go build -trimpath -ldflags='-s -w' -o "$release/audit-server" ./cmd/server
  cp -R frontend/dist/. "$release/frontend/dist/"
  cp deploy/deepseek-audit.service deploy/init-native-env.sh "$release/deploy/"
  cp DEPLOY_NATIVE.zh.md README.md COSTS_AND_OPTIMIZATION.md "$release/"
  printf 'version=%s\nos=linux\narch=%s\n' "$version" "$target" > "$release/RELEASE.txt"
  chmod 755 "$release/audit-server"
  archive="$project_root/dist/$name.tar.gz"
  COPYFILE_DISABLE=1 tar -czf "$archive" -C "$stage" "$name"
  (
    cd dist
    if command -v shasum >/dev/null; then
      shasum -a 256 "$name.tar.gz" > "$name.tar.gz.sha256"
    else
      sha256sum "$name.tar.gz" > "$name.tar.gz.sha256"
    fi
  )
  echo "Created: $archive"
done
