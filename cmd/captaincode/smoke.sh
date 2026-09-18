#!/usr/bin/env bash
# Cheapest end-to-end captain smoke: the director plans (class + leg) + assesses,
# a worker leg does the work. Verifies:
#   1) director never does the work itself
#   2) director-returned class is recorded on the event
#
# Each task runs as a fresh captain process so earlier decisions don't affect
# the director's routing for later tasks.
#
#   ./cmd/captaincode/smoke.sh
#
# Cost: ~2 small director calls (plan + assess) + 1 worker call per task.
set -euo pipefail

# Each task may take 30–50 s; the high-quality claude task can take 2+ min.
if [ -z "${CAPTAIN_SMOKE_TIMEOUT:-}" ]; then
  export CAPTAIN_SMOKE_TIMEOUT=300
fi

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="${CAPTAIN_BIN:-}"
if [ -z "$BIN" ]; then
  BIN="$(mktemp -t captain.XXXXXX)"
  trap 'rm -f "$BIN"' EXIT
  echo "→ building captain → $BIN"
  go build -o "$BIN" "$ROOT/cmd/captaincode"
fi

# Isolated home so prior smoke runs don't affect decisions.
CAPTAIN_HOME="$(mktemp -d -t captain-home.XXXXXX)"
trap 'rm -rf "$CAPTAIN_HOME"' EXIT
export HOME="$CAPTAIN_HOME"

echo "→ director: ${CAPTAIN_DIRECTOR:-grok (default)}"

# Six tasks spanning all three complexity classes. Trivial/medium use
# --prefer save (cheapest leg); high uses --prefer quality to encourage
# director to escalate. Every task must have a non-empty class on the
# event and the director must never do the work itself.
run_task() {
  local prefer="$1" task="$2"
  echo "── task ($prefer): $task"
  "$BIN" --prefer "$prefer" "$task" || true
  echo "--- last decision ---"
  "$BIN" why
  class=$("$BIN" why | grep -oE 'class=[a-z]+' | head -1 | cut -d= -f2 || true)
  leg=$("$BIN" why | grep -oE 'leg=[a-z]+' | head -1 | cut -d= -f2 || true)
  director="${CAPTAIN_DIRECTOR:-grok}"
  if [ -z "$leg" ]; then
    echo "FAIL: no dispatch recorded"; exit 1
  fi
  if [ "$leg" = "$director" ]; then
    echo "FAIL: the director ($director) did the work itself (worker=$leg)"; exit 1
  fi
  if [ -z "$class" ] || [ "$class" = "-" ]; then
    echo "FAIL: director class not recorded on event"; exit 1
  fi
  echo "PASS: director=$director class=$class worker=$leg"
}

run_task save "In one word, what colour is a clear daytime sky?"
run_task save "List three Go standard-library packages used for HTTP handlers."
run_task save "Explain in two sentences how a race condition can appear in a shared map without a mutex."
run_task save "Explain two strategies for handling structured concurrency in Go: errgroup vs a custom fan-out with context cancellation. Compare cancellation propagation, error collection, and goroutine lifecycle."
run_task save "Compare the trade-offs between a microservice architecture and a monorepo with well-defined packages: developer velocity, deployment coupling, testing, and operational overhead. Name one concrete scenario where each wins."
# High-complexity quality task: the director should classify this as "high"
# (contains "architect" and "design") and route to a stronger worker.
run_task quality "Architect and design how idempotency keys work across service boundaries in a distributed payment system. Cover key generation, storage, expiry with cleanup, retry with backoff, and exactly-once guarantees."
