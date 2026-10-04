#!/usr/bin/env bash
# Proves portable/core.cljc is shared source: Babashka must print the same
# report that coding/extension/host/letgo/cljc_test.go asserts for let-go.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
got=$(bb -cp "$here" -e "(require 'portable.core) (println (portable.core/report \"  the quick brown   fox \"))")
want=$(cat "$here/report.golden")
if [[ "$got" != "$want" ]]; then
  printf 'bb report differs\n got: %s\nwant: %s\n' "$got" "$want" >&2
  exit 1
fi
echo "bb report matches report.golden"
