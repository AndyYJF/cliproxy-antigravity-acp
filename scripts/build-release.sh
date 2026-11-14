#!/usr/bin/env bash
set -euo pipefail

tag="${1:?Usage: bash scripts/build-release.sh vX.Y.Z}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "Expected tag vX.Y.Z or vX.Y.Z-prerelease" >&2
  exit 1
fi
version="${tag#v}"
export GOOS=linux GOARCH=amd64
go test ./cmd/bridge ./cmd/plugin
mkdir -p build/release
CGO_ENABLED=1 go build -buildvcs=false -trimpath -buildmode=c-shared \
  -ldflags="-s -w -X main.pluginVersion=$version" \
  -o "build/release/antigravity-acp-v$version.so" ./cmd/plugin
CGO_ENABLED=0 go build -buildvcs=false -trimpath \
  -ldflags="-s -w -X main.bridgeVersion=$version" \
  -o build/release/acp-bridge ./cmd/bridge
