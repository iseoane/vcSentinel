#!/usr/bin/env sh
# VAS Sentinel - release script (Linux/Debian)
# Genera los assets multiplataforma y publica la release en GitHub.
# La versión se lee de release.yml (fuente de verdad); se puede forzar con SENTINEL_VERSION.
set -e

cd "$(dirname "$0")/.."

VERSION="0.1.0"
if [ -f release.yml ]; then
    VERSION="$(sed -n 's/^version:[[:space:]]*//p' release.yml | head -n1 | tr -d '"' | tr -d '\r')"
    [ -n "$VERSION" ] || VERSION="0.1.0"
fi
VERSION="${SENTINEL_VERSION:-$VERSION}"

echo "=== go vet ==="
go vet ./...

echo "=== generate multiplatform assets ==="
go run ./tools/release

echo "=== publish GitHub release v$VERSION ==="
gh release create "v$VERSION" \
  "bin/$VERSION/sentinel-windows-amd64.exe" \
  "bin/$VERSION/sentinel-linux-amd64" \
  "bin/$VERSION/sentinel-linux-arm64" \
  --title "VAS Sentinel v$VERSION" \
  --generate-notes

echo
echo "OK: release v$VERSION published"
