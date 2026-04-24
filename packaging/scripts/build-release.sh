#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty)}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
CHANNEL="${CHANNEL:-dev}"
TARGETS="${TARGETS:-linux/arm64 linux/arm/v7 linux/amd64 darwin/arm64}"

mkdir -p "$OUT_DIR"

ldflags="-X main.Version=$VERSION -X main.Commit=$COMMIT -X main.BuildDate=$BUILD_DATE -X main.Channel=$CHANNEL"

for target in $TARGETS; do
  os="${target%%/*}"
  rest="${target#*/}"
  arch="${rest%%/*}"
  arm=""
  if [[ "$rest" == */* ]]; then
    arm="${rest##*/}"
  fi
  name="expert-amp-server_${VERSION}_${os}_${arch}"
  if [[ -n "$arm" ]]; then
    name="${name}_${arm}"
  fi
  out="$OUT_DIR/$name"
  if [[ "$os" == "windows" ]]; then
    out="$out.exe"
  fi
  echo "building $target -> $out"
  if [[ -n "$arm" ]]; then
    GOOS="$os" GOARCH="$arch" GOARM="$arm" go build -trimpath -ldflags "$ldflags" -o "$out" "$ROOT/cmd/server"
  else
    GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out" "$ROOT/cmd/server"
  fi
done

cp "$ROOT/packaging/systemd/expert-amp-server.service" "$OUT_DIR/"
cp "$ROOT/packaging/config/config.example.json" "$OUT_DIR/"

(
  cd "$OUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum expert-amp-server_* expert-amp-server.service config.example.json > SHA256SUMS
  else
    shasum -a 256 expert-amp-server_* expert-amp-server.service config.example.json > SHA256SUMS
  fi
)

echo "release artifacts written to $OUT_DIR"
