#!/usr/bin/env bash
# Export an amd64 Docker image plus the files needed for 1Panel Compose.
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
version="${1:-$(date -u +%Y%m%d-%H%M%S)}"
if [[ ! "$version" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]]; then
  echo 'Version may only contain letters, numbers, dot, underscore and hyphen.' >&2
  exit 1
fi
for dependency in docker tar; do
  command -v "$dependency" >/dev/null || { echo "Missing build tool: $dependency" >&2; exit 1; }
done
name="deepseek-audit-${version}-1panel-amd64"
output="$project_root/dist/$name"
if [[ -e "$output" ]]; then
  echo 'Release directory already exists; choose a new version.' >&2
  exit 1
fi
mkdir -p "$output"
image="deepseek-audit:1panel-amd64-${version}"
docker build --platform linux/amd64 --tag "$image" .
docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}' | \
  grep -qx 'linux/amd64'
docker image save --output "$output/image.tar" "$image"
sed "s/deepseek-audit:1panel-amd64/deepseek-audit:1panel-amd64-${version}/" \
  deploy/compose.1panel.yaml > "$output/compose.yaml"
cp deploy/1panel.env.example "$output/.env.example"
cp DEPLOY_1PANEL.zh.md "$output/DEPLOY_1PANEL.zh.md"
(
  cd "$output"
  if command -v shasum >/dev/null; then
    shasum -a 256 image.tar compose.yaml .env.example DEPLOY_1PANEL.zh.md > SHA256SUMS
  else
    sha256sum image.tar compose.yaml .env.example DEPLOY_1PANEL.zh.md > SHA256SUMS
  fi
)
COPYFILE_DISABLE=1 tar -czf "$output.tar.gz" -C "$project_root/dist" "$name"
echo "Created: $output.tar.gz"
