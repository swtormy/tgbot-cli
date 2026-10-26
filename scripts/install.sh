#!/usr/bin/env bash
# Installs the latest tgbot release for macOS or Linux.
#   curl -fsSL https://raw.githubusercontent.com/swtormy/tgbot-cli/main/scripts/install.sh | bash
# Environment:
#   TGBOT_VERSION       release tag to install (default: latest)
#   TGBOT_INSTALL_DIR   target directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -euo pipefail

repo="swtormy/tgbot-cli"
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "Unsupported OS: $(uname -s). Download a release manually: https://github.com/${repo}/releases" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${TGBOT_BASE_URL:-}" ]; then
  base="$TGBOT_BASE_URL"
elif [ -n "${TGBOT_VERSION:-}" ]; then
  base="https://github.com/${repo}/releases/download/${TGBOT_VERSION}"
else
  base="https://github.com/${repo}/releases/latest/download"
fi

if [ -n "${TGBOT_INSTALL_DIR:-}" ]; then
  dir="$TGBOT_INSTALL_DIR"
elif [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

asset="tgbot_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${asset}"
curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}"
curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"

want="$(grep " ${asset}\$" "${tmp}/checksums.txt" | cut -d' ' -f1)"
if [ -z "$want" ]; then echo "No checksum for ${asset}" >&2; exit 1; fi
if command -v sha256sum >/dev/null; then
  got="$(sha256sum "${tmp}/${asset}" | cut -d' ' -f1)"
else
  got="$(shasum -a 256 "${tmp}/${asset}" | cut -d' ' -f1)"
fi
if [ "$want" != "$got" ]; then echo "Checksum mismatch for ${asset}" >&2; exit 1; fi

tar -xzf "${tmp}/${asset}" -C "$tmp"
mkdir -p "$dir"
install -m 0755 "${tmp}/tgbot_${os}_${arch}/tgbot" "${dir}/tgbot"
echo "Installed ${dir}/tgbot"
case ":$PATH:" in *":${dir}:"*) ;; *) echo "Add ${dir} to your PATH." ;; esac
"${dir}/tgbot" --version
