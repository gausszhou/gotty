#!/usr/bin/env bash
# Agent API e2e: the hermetic parts of 0004 §4 + 0005 §4.
#
# Every case drives a real `gotty serve` over HTTP with the CLI (no browser):
#
#   1. scrollback is tail-first and the visible screen is not history;
#   2. a full-screen (alternate-screen) program reports alternate_screen:true
#      with an empty history instead of erroring;
#   3. the key-name layer closes the loop: `press a b c` -> `wait abc`;
#   4. a program that never enabled mouse reporting answers 409, and --force
#      sends SGR bytes anyway;
#   5. conditional bracketed paste: a mode-2004 fixture receives ONE wrapped
#      ESC[200~…ESC[201~ block, not one line at a time;
#   6. `gotty usage` stays in cheat-sheet size (< 6000 bytes).
#
# Usage: bash scripts/e2e/agent-api.sh [path-to-gotty]
# Requires: jq, curl, go, and a built gotty binary (default ./build/gotty).
# Build it with: go build -o build/gotty .
set -euo pipefail

GOTTY_BIN="${1:-./build/gotty}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
tmp="$(mktemp -d)"
port=19049
server="http://127.0.0.1:$port"
active_sessions=()
server_pid=""

cd "$root"
if [[ "$GOTTY_BIN" != /* ]]; then
  GOTTY_BIN="$root/$GOTTY_BIN"
fi

if [[ ! -x "$GOTTY_BIN" ]]; then
  echo "gotty binary not executable: $GOTTY_BIN" >&2
  echo "build it with: go build -o build/gotty ." >&2
  exit 1
fi
for tool in jq curl go; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

# ---- helpers --------------------------------------------------------------

# dump_evidence prints the server log and the screen/history of every live
# session. It is called on the first assertion failure, before cleanup.
dump_evidence() {
  echo "---- evidence: server log (tail) ----" >&2
  if [[ -f "$tmp/server.log" ]]; then
    tail -n 50 "$tmp/server.log" >&2 || true
  else
    echo "(no server log)" >&2
  fi
  if [[ ${#active_sessions[@]} -eq 0 ]]; then
    echo "---- evidence: no live sessions ----" >&2
    return 0
  fi
  local id
  for id in "${active_sessions[@]}"; do
    echo "---- evidence: session $id status ----" >&2
    "$GOTTY_BIN" status --server "$server" --session "$id" >&2 2>&1 || true
    echo "---- evidence: session $id screen ----" >&2
    "$GOTTY_BIN" screen --server "$server" --session "$id" >&2 2>&1 || true
    echo "---- evidence: session $id scrollback (tail 20) ----" >&2
    "$GOTTY_BIN" scrollback --server "$server" --session "$id" --lines 20 >&2 2>&1 || true
  done
}

fail() {
  echo "FAIL: $*" >&2
  dump_evidence
  exit 1
}

on_error() {
  local code=$?
  echo "FAIL: unexpected command failure (exit $code) at line $1" >&2
  dump_evidence
  exit 1
}

cleanup() {
  local status=$?
  trap - EXIT ERR
  local id
  if [[ ${#active_sessions[@]} -gt 0 ]]; then
    for id in "${active_sessions[@]}"; do
      "$GOTTY_BIN" kill --server "$server" --session "$id" >/dev/null 2>&1 || true
    done
  fi
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill "$server_pid" 2>/dev/null || true
    local i
    for i in $(seq 1 100); do
      kill -0 "$server_pid" 2>/dev/null || break
      sleep 0.02
    done
    kill -9 "$server_pid" 2>/dev/null || true
  fi
  if [[ -n "$server_pid" ]]; then
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'on_error "$LINENO"' ERR

require_eq() { # got want what
  if [[ "$1" != "$2" ]]; then
    fail "$3: got '$1', want '$2'"
  fi
}

require_jq() { # json expr what
  if ! jq -e "$2" >/dev/null 2>&1 <<<"$1"; then
    fail "$3: jq '$2' rejected: $1"
  fi
}

require_contains() { # haystack needle what
  if [[ "$1" != *"$2"* ]]; then
    fail "$3: '$2' not found in: $1"
  fi
}

require_line() { # text line what
  if ! grep -qxF -- "$2" <<<"$1"; then
    fail "$3: expected line '$2' in: $1"
  fi
}

require_absent_line() { # text line what
  if grep -qxF -- "$2" <<<"$1"; then
    fail "$3: line '$2' must not be present in: $1"
  fi
}

# create_session creates a session and stores its id in SESSION_ID. A global
# is used instead of command substitution so the array update is not lost in
# a subshell.
create_session() {
  local out
  out=$("$GOTTY_BIN" run --server "$server" "$@") || fail "gotty run failed: $*"
  if [[ -z "$out" ]]; then
    fail "gotty run returned an empty session id"
  fi
  SESSION_ID="$out"
  active_sessions+=("$SESSION_ID")
}

kill_session() { # id
  local id=$1
  "$GOTTY_BIN" kill --server "$server" --session "$id" >/dev/null 2>&1 || true
  local kept=()
  local x
  for x in "${active_sessions[@]}"; do
    [[ "$x" == "$id" ]] || kept+=("$x")
  done
  if [[ ${#kept[@]} -gt 0 ]]; then
    active_sessions=("${kept[@]}")
  else
    active_sessions=()
  fi
}

wait_regex() { # id regex
  local id=$1 re=$2 out
  out=$("$GOTTY_BIN" wait --server "$server" --session "$id" \
    --regex "$re" --timeout-ms 5000 --format json) ||
    fail "gotty wait --regex '$re' failed for session $id"
  require_jq "$out" '.matched == true' "screen never matched '$re'"
}

wait_quiet() { # id
  local id=$1 out
  out=$("$GOTTY_BIN" wait --server "$server" --session "$id" \
    --quiet-ms 200 --timeout-ms 5000 --format json) ||
    fail "gotty wait --quiet-ms failed for session $id"
  require_jq "$out" '.quiet == true' "output never went quiet"
}

wait_bracketed_paste() { # id
  local id=$1 i out=""
  for i in $(seq 1 100); do
    out=$("$GOTTY_BIN" cursor --server "$server" --session "$id" 2>/dev/null) || true
    if jq -e '.bracketed_paste == true' >/dev/null 2>&1 <<<"$out"; then
      return 0
    fi
    sleep 0.05
  done
  fail "session $id never reported bracketed_paste=true (last: ${out:-none})"
}

wait_alternate_screen() { # id
  local id=$1 i out=""
  for i in $(seq 1 100); do
    out=$("$GOTTY_BIN" scrollback --server "$server" --session "$id" --format json 2>/dev/null) || true
    if jq -e '.alternate_screen == true' >/dev/null 2>&1 <<<"$out"; then
      ALT_SCREEN_JSON="$out"
      return 0
    fi
    sleep 0.05
  done
  fail "session $id never entered the alternate screen (last: ${out:-none})"
}

start_server() {
  echo "== start gotty serve on 127.0.0.1:$port =="
  if curl -fsS -o /dev/null --max-time 0.5 "$server/" 2>/dev/null; then
    fail "port $port is already serving something"
  fi
  mkdir -p "$tmp/home"
  HOME="$tmp/home" GOTTY_CONFIG="$tmp/home/.gotty/config.json" \
    "$GOTTY_BIN" serve \
    --address 127.0.0.1 --port "$port" \
    --permit-write=true --mirror=true \
    --session-file "" --title-file "" --log-file "" \
    >"$tmp/server.log" 2>&1 &
  server_pid=$!

  local i
  for i in $(seq 1 100); do
    if ! kill -0 "$server_pid" 2>/dev/null; then
      fail "gotty serve exited during startup"
    fi
    if curl -fsS -o /dev/null --max-time 0.5 "$server/"; then
      return 0
    fi
    sleep 0.05
  done
  fail "gotty serve did not become ready on $server"
}

# ---- setup ----------------------------------------------------------------

echo "== build paste fixture =="
go build -o "$tmp/paste-fixture" ./scripts/e2e/fixture-paste ||
  fail "go build ./scripts/e2e/fixture-paste failed"

start_server

# ---- case 1: scrollback tail-first ---------------------------------------

echo "== case 1: scrollback --lines 20 is tail-first, screen is not history =="
# seq writes 500 lines; the 19 extra newlines scroll the whole visible page
# (height 20) into history, so the 20 most recent history lines are exactly
# 481…500. Without that scroll, 482…500 would still be visible and scrollback
# would (correctly) end at 481. SB_READY marks the end of output without
# adding another newline of its own.
create_session --width 80 --height 20 -- sh -c \
  'seq 1 500; n=0; while [ "$n" -lt 19 ]; do printf "\n"; n=$((n+1)); done; printf "SB_READY"; sleep 600'
id=$SESSION_ID
wait_regex "$id" 'SB_READY'
wait_quiet "$id"

sb_json=$("$GOTTY_BIN" scrollback --server "$server" --session "$id" --lines 20 --format json) ||
  fail "gotty scrollback --format json failed"
sb_text=$("$GOTTY_BIN" scrollback --server "$server" --session "$id" --lines 20) ||
  fail "gotty scrollback failed"
screen_text=$("$GOTTY_BIN" screen --server "$server" --session "$id") ||
  fail "gotty screen failed"

sb_count=$(printf '%s\n' "$sb_text" | wc -l | tr -d ' ')
sb_first=$(head -n 1 <<<"$sb_text")
sb_last=$(tail -n 1 <<<"$sb_text")
sb_total=$(jq -r '.total' <<<"$sb_json")
sb_lines=$(jq -r '.lines' <<<"$sb_json")
alternate=$(jq -r '.alternate_screen' <<<"$sb_json")
echo "scrollback: first=$sb_first last=$sb_last count=$sb_count total=$sb_total lines=$sb_lines"
echo "screen:     contains SB_READY=$([[ "$screen_text" == *SB_READY* ]] && echo yes || echo no)"

# The 20 most recent lines are 481…500 (chronological order in the payload;
# reading from the end gives 500…481). The visible screen has no 481 because
# the page was scrolled into history, which is exactly 0005 §4's acceptance.
require_eq "$alternate" "false" "scrollback must be on the main screen"
require_eq "$sb_lines" "20" "scrollback --lines 20 must return 20 lines"
require_eq "$sb_count" "20" "scrollback text must contain 20 lines"
require_eq "$sb_total" "500" "history must retain all 500 lines after the page scrolls"
require_eq "$sb_first" "481" "the 20 returned lines must be the tail (481…500), not the head"
require_eq "$sb_last" "500" "the newest history line must be seq's last line"
require_contains "$screen_text" "SB_READY" "the session must still be alive after the output"
require_absent_line "$screen_text" "481" "481 is outside the visible screen"
require_absent_line "$screen_text" "500" "500 is outside the visible screen"
require_absent_line "$sb_text" "1" "scrollback must not start at the head of seq"
kill_session "$id"

# ---- case 2: alternate screen ---------------------------------------------

echo "== case 2: alternate screen reports alternate_screen:true with empty history =="
create_session --width 80 --height 20 -- sh -c 'printf "\033[?1049h"; printf "ALT_READY\r\n"; sleep 600'
id=$SESSION_ID
wait_regex "$id" 'ALT_READY'
wait_alternate_screen "$id"
alt_json=$ALT_SCREEN_JSON
require_jq "$alt_json" '.alternate_screen == true' "alternate_screen must be true"
require_jq "$alt_json" '((.text // "") == "") and ((.cells // []) | length == 0)' \
  "the alternate screen must have an empty history"
require_jq "$alt_json" '.total == 0' "the alternate screen must report total=0"
kill_session "$id"

# ---- case 3: key-name layer closes the loop -------------------------------

echo "== case 3: gotty press a b c -> gotty wait --regex abc =="
create_session --width 80 --height 20 -- cat
id=$SESSION_ID
press_json=$("$GOTTY_BIN" press --server "$server" --session "$id" a b c) ||
  fail "gotty press failed"
require_jq "$press_json" '.mode == "keys" and .written == 3' \
  "press must encode three named keys"
wait_regex "$id" 'abc'
screen_text=$("$GOTTY_BIN" screen --server "$server" --session "$id") ||
  fail "gotty screen failed"
require_line "$screen_text" "abc" "cat must echo the pressed keys"
kill_session "$id"

# ---- case 4: mouse 409 and --force ----------------------------------------

echo "== case 4: mouse click on a non-mouse program is 409, --force succeeds =="
create_session --width 80 --height 20 -- sh -c 'sleep 600'
id=$SESSION_ID
cursor_json=$("$GOTTY_BIN" cursor --server "$server" --session "$id") ||
  fail "gotty cursor failed"
require_jq "$cursor_json" '.supported == true and .enabled == false' \
  "the sleep program must not have enabled mouse reporting"

if mouse_err=$("$GOTTY_BIN" mouse click --server "$server" --session "$id" --col 1 --row 1 2>&1); then
  fail "mouse click without --force must fail with HTTP 409"
else
  mouse_code=$?
fi
if [[ "$mouse_code" -eq 0 ]]; then
  fail "mouse click without --force exited 0"
fi
require_contains "$mouse_err" "409" "the error must carry HTTP 409"
require_contains "$mouse_err" "did not enable mouse reporting" \
  "the 409 must explain that the program never enabled mouse reporting"

force_json=$("$GOTTY_BIN" mouse click --server "$server" --session "$id" --col 1 --row 1 --force) ||
  fail "mouse click --force failed"
require_jq "$force_json" '.written > 0 and .resolved_from == "coordinates"' \
  "--force must send SGR bytes at the requested coordinates"
kill_session "$id"

# ---- case 5: conditional bracketed paste ----------------------------------

echo "== case 5: bracketed paste is ONE wrapped block, not line-by-line =="
create_session --width 120 --height 24 -- "$tmp/paste-fixture"
id=$SESSION_ID
wait_regex "$id" 'PASTE_READY'
wait_bracketed_paste "$id"

paste_text=$'one\ntwo\nthree'
paste_json=$("$GOTTY_BIN" paste --server "$server" --session "$id" "$paste_text") ||
  fail "gotty paste failed"
# 13 bytes of text + ESC[200~ (6) + ESC[201~ (6) = 25 bytes written once.
require_jq "$paste_json" '.mode == "paste" and .bracketed == true and .written == 25' \
  "paste must be wrapped (13 text bytes + 12 marker bytes)"
wait_regex "$id" 'PASTE_REPORT wrapped=yes len=13 nl=2 start=1 end=1 head=one tail=three'
screen_text=$("$GOTTY_BIN" screen --server "$server" --session "$id") ||
  fail "gotty screen failed"
require_contains "$screen_text" \
  "PASTE_REPORT wrapped=yes len=13 nl=2 start=1 end=1 head=one tail=three" \
  "the fixture must report exactly one wrapped block with both newlines"
if grep -q 'PASTE_REPORT wrapped=no' <<<"$screen_text"; then
  fail "the paste was delivered without bracketed-paste markers"
fi
kill_session "$id"

# ---- case 6: usage size ---------------------------------------------------

echo "== case 6: gotty usage stays a cheat sheet (< 6000 bytes) =="
"$GOTTY_BIN" usage >"$tmp/usage.txt" || fail "gotty usage failed"
usage_bytes=$(wc -c <"$tmp/usage.txt" | tr -d ' ')
echo "usage: $usage_bytes bytes"
if [[ "$usage_bytes" -ge 6000 ]]; then
  fail "gotty usage is $usage_bytes bytes, want < 6000"
fi
if [[ "$usage_bytes" -le 1000 ]]; then
  fail "gotty usage is only $usage_bytes bytes; the cheat sheet looks truncated"
fi

echo "PASS: agent API e2e (6 cases)"
