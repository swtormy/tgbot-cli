#!/usr/bin/env bash
# Cross-compiles tgbot for every supported platform into ./dist and writes checksums.txt.
# Usage: scripts/build-release.sh [version]   (default: git describe or "dev")
set -euo pipefail

cd "$(dirname "$0")/.."
version="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
version="${version#v}"
rm -rf dist
mkdir -p dist

targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
for t in $targets; do
  os="${t%/*}"
  arch="${t#*/}"
  name="tgbot_${os}_${arch}"
  stage="dist/${name}"
  mkdir -p "$stage"
  bin="tgbot"
  [ "$os" = "windows" ] && bin="tgbot.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X github.com/swtormy/tgbot-cli/internal/cli.Version=${version}" \
    -o "${stage}/${bin}" ./cmd/tgbot
  cp LICENSE NOTICE README.md "$stage/"
  cp -r docs "$stage/docs"
  if [ "$os" = "windows" ]; then
    if command -v zip >/dev/null; then
      (cd dist && zip -qr "${name}.zip" "$name")
    else
      (cd dist && python3 -m zipfile -c "${name}.zip" "$name")
    fi
  else
    tar -C dist -czf "dist/${name}.tar.gz" "$name"
  fi
  rm -rf "$stage"
done

cd dist
if command -v sha256sum >/dev/null; then
  sha256sum -- *.tar.gz *.zip > checksums.txt
else
  shasum -a 256 -- *.tar.gz *.zip > checksums.txt
fi
ls -la
