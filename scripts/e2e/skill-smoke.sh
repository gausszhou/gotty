#!/usr/bin/env bash
# Skill smoke test: prove that the skill's harness template actually runs.
#
#   1. `gotty skill` prints the embedded SKILL.md and `--install` writes it;
#   2. the harness extracted from SKILL.md passes against fixture-tui;
#   3. the same harness fails (non-zero exit + evidence) when the assertion
#      is wrong — a test that cannot fail is not a test.
#
# Usage: bash scripts/e2e/skill-smoke.sh [path-to-gotty]
# Requires: jq, go, and a built gotty binary (default ./build/gotty).
set -euo pipefail

GOTTY_BIN="${1:-./build/gotty}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cd "$root"

command -v jq >/dev/null || { echo "jq is required (the skill's contract)" >&2; exit 1; }
command -v go >/dev/null || { echo "go is required to build the fixture" >&2; exit 1; }
[[ -x "$GOTTY_BIN" ]] || { echo "gotty binary not executable: $GOTTY_BIN" >&2; exit 1; }

echo "== build fixture =="
go build -o "$tmp/tui" ./scripts/e2e/fixture-tui

echo "== gotty skill prints and installs the embedded file =="
"$GOTTY_BIN" skill >"$tmp/printed.md"
cmp "$tmp/printed.md" "$root/skills/gotty-tui-test/SKILL.md"
"$GOTTY_BIN" skill --install "$tmp/installed" >/dev/null
cmp "$tmp/installed/SKILL.md" "$root/skills/gotty-tui-test/SKILL.md"

echo "== extract the harness from SKILL.md =="
awk '/<!-- harness:start -->/{f=1;next} /<!-- harness:end -->/{f=0} f' \
  "$root/skills/gotty-tui-test/SKILL.md" >"$tmp/harness.sh"
[[ -s "$tmp/harness.sh" ]] || { echo "harness markers produced an empty script" >&2; exit 1; }

echo "== happy path =="
GOTTY="$GOTTY_BIN" TUI_CMD="$tmp/tui" bash "$tmp/harness.sh"

echo "== failure path (wrong ready marker must fail and dump evidence) =="
set +e
GOTTY="$GOTTY_BIN" TUI_CMD="$tmp/tui" READY_REGEX="NeverAppears" \
  bash "$tmp/harness.sh" >"$tmp/fail.out" 2>"$tmp/fail.err"
code=$?
set -e
if [[ $code -eq 0 ]]; then
  echo "expected the harness to fail with READY_REGEX=NeverAppears" >&2
  exit 1
fi
grep -q 'FAIL: TUI did not reach' "$tmp/fail.err" || {
  echo "missing the failure message" >&2; cat "$tmp/fail.err" >&2; exit 1;
}
grep -q -- '---- screen ----' "$tmp/fail.err" || {
  echo "missing screen evidence" >&2; cat "$tmp/fail.err" >&2; exit 1;
}
grep -q -- '---- server log' "$tmp/fail.err" || {
  echo "missing server log evidence" >&2; cat "$tmp/fail.err" >&2; exit 1;
}

echo "PASS: skill smoke"
