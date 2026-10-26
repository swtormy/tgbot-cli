#!/usr/bin/env bash
# Refreshes internal/spec/api.json from the daily-updated Bot API description
# by PaulSonOfLars/telegram-bot-api-spec, keeping only what tgbot uses.
set -euo pipefail

cd "$(dirname "$0")/.."
src="https://raw.githubusercontent.com/PaulSonOfLars/telegram-bot-api-spec/main/api.json"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

curl -fsSL "$src" -o "$tmp"
jq -c '{version, release_date, methods}' "$tmp" > internal/spec/api.json
jq -r '"Updated spec: \(.version) (\(.release_date)), \(.methods | length) methods"' internal/spec/api.json
