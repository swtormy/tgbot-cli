#!/usr/bin/env bash
# Generates THIRD_PARTY_NOTICES and third_party_licenses/ for release archives.
# Uses go-licenses v1.6.0 (pinned). Union of dependencies across linux, darwin, and windows.
set -euo pipefail

cd "$(dirname "$0")/.."

export PATH="$(go env GOPATH)/bin:${PATH}"

license_ignores=(--ignore=internal/ --ignore=github.com/segmentio/asm)
while IFS= read -r pkg; do
  license_ignores+=(--ignore="$pkg")
done < <(go list std)

pkgs="./..."

for goos in linux darwin windows; do
  GOOS="$goos" go-licenses check "$pkgs" \
    --disallowed_types=forbidden,restricted \
    "${license_ignores[@]}"
done

rm -rf third_party_licenses
GOOS=linux go-licenses save "$pkgs" \
  --save_path=third_party_licenses \
  --force \
  "${license_ignores[@]}"

merge_save() {
  local goos=$1
  local tmp
  tmp="$(mktemp -d)"
  GOOS="$goos" go-licenses save "$pkgs" \
    --save_path="$tmp" \
    --force \
    "${license_ignores[@]}"
  while IFS= read -r f; do
    rel="${f#"$tmp"/}"
    mkdir -p "third_party_licenses/$(dirname "$rel")"
    cp -f "$f" "third_party_licenses/$rel"
  done < <(find "$tmp" -type f)
  rm -rf "$tmp"
}

merge_save darwin
merge_save windows

asm_dir="$(go list -m -f '{{.Dir}}' github.com/segmentio/asm)"
asm_dest="third_party_licenses/github.com/segmentio/asm"
mkdir -p "$asm_dest"
cp -a "$asm_dir/LICENSE" "$asm_dest/"

{
  echo "THIRD-PARTY NOTICES"
  echo
  echo "This file lists third-party software included in or used to build tgbot."
  echo "License texts are in the third_party_licenses/ directory."
  echo "The dependency set is the union across linux, darwin, and windows builds."
  echo
  echo "module,source,license"
} > THIRD_PARTY_NOTICES

for goos in linux darwin windows; do
  GOOS="$goos" go-licenses report "$pkgs" "${license_ignores[@]}"
done | sort -u >> THIRD_PARTY_NOTICES

go list -m -f 'github.com/segmentio/asm,https://github.com/segmentio/asm/blob/{{.Version}}/LICENSE,MIT-0' github.com/segmentio/asm >> THIRD_PARTY_NOTICES
