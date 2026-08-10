#!/usr/bin/env sh
# VAS Sentinel - format and build script (Linux/Debian)
set -e

cd "$(dirname "$0")"

VERSION="0.1.0"
if [ -f release.yml ]; then
    VERSION="$(sed -n 's/^version:[[:space:]]*//p' release.yml | head -n1 | tr -d '"' | tr -d '\r')"
    [ -n "$VERSION" ] || VERSION="0.1.0"
fi
VERSION="${SENTINEL_VERSION:-$VERSION}"

echo "=== gofmt ==="
gofmt -w .
if [ $? -ne 0 ]; then exit 1; fi

echo "=== go vet ==="
go vet ./...
if [ $? -ne 0 ]; then exit 1; fi

echo "=== build ==="
mkdir -p "bin/$VERSION"
go build -ldflags="-s -w -X main.version=$VERSION" -o "bin/$VERSION/sentinel" ./cmd/sentinel
if [ $? -ne 0 ]; then exit 1; fi

echo
echo "OK: bin/$VERSION/sentinel generated"
