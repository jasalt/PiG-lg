#!/usr/bin/env bash
# Runs the kmet-compat fixtures with Babashka against a Kmet checkout's create-nullable-api.
# Usage: run-kmet.sh <path to a kmet-agent checkout>   (or set KMET_DIR)
# Kmet is not vendored and this script is not part of CI. Compare the output with
# the let-go test: go test ./coding/extension/host/letgo -run KmetCompat.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
kmet=${1:-${KMET_DIR:-}}
if [[ -z "$kmet" || ! -f "$kmet/src/kmet/extension.clj" ]]; then
  echo "usage: run-kmet.sh <kmet-agent checkout>   (KMET_DIR also works)" >&2
  exit 2
fi
kmet=$(cd "$kmet" && pwd)
echo "kmet checkout: $kmet @ $(git -C "$kmet" rev-parse --short HEAD 2>/dev/null || echo unknown)"
cd "$here"
exec bb -cp "$kmet/src:$here" run-kmet.clj
