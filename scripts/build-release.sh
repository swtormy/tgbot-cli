#!/usr/bin/env bash
# Deprecated: use GoReleaser for release builds.
#   goreleaser release --snapshot --clean -p 1
set -euo pipefail
echo "scripts/build-release.sh is deprecated; run: goreleaser release --snapshot --clean -p 1" >&2
exec goreleaser release --snapshot --clean -p 1 "$@"
