#!/usr/bin/env bash
set -euo pipefail
# ~/.opencode/bin belongs here too: opencode's installer adds it to ~/.zshrc
# only, so a launch that does not come from an interactive zsh (a desktop
# entry, a script, a CI shell) saw NO opencode binary - and every opencode leg
# reported itself missing while the install was fine (2026-09-22).
export PATH="$HOME/.opencode/bin:$HOME/.bun/bin:$HOME/.local/bin:$PATH"
# The PHYSICAL path: opencode records a session under the resolved folder,
# so a TUI launched through a symlink to a nested checkout found none of its
# sessions with -c and started fresh every time (2026-09-17). Every cwd-keyed
# thing - sessions, the brain's workspace, the sidebar's feed - must agree.
LAUNCH_DIR="$(pwd -P)"
export CAPTAIN_CWD="$LAUNCH_DIR"
# No exit trap: the brain is a daemon shared by every captain-code TUI on the
# machine (its supervisor outlives this launcher on purpose). `captaincode.sh
# down` stops it explicitly.
for envf in "$HOME/.config/captain/env" "$HOME/.config/opencode/env" "$LAUNCH_DIR/.env"; do
  if [ -f "$envf" ]; then
    echo "→ sourcing $envf"
    set -a; . "$envf"; set +a
  fi
done
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRAIN_URL="http://127.0.0.1:14097"
BRAIN_PID_FILE="/tmp/captain-brain.pid"
BRAIN_LOG="/tmp/captain-brain.log"

brain_health() {
  curl -s "$BRAIN_URL/v1/health" 2>/dev/null || true
}

start_brain() {
  echo "→ starting captain brain…"
  # The brain is the GO binary (captain brain) - the routing/scorecard/team
  # engine with the resilience stack. A TS stub (index.ts brain) briefly
  # replaced it here and broke every turn with 404s (2026-07-23): it served
  # /v1/health but not /v1/models or the wrapper. Do not point this at the
  # fork again; the fork is the FRONTEND.
  # One brain serves every captain-code TUI on the machine: each request
  # names the folder its TUI is open in (X-Captain-Cwd, ?cwd=), so the brain
  # is NOT pinned to this launch dir - it is started without CAPTAIN_CWD and
  # never restarted for another folder (that restart killed the other TUI's
  # workers, and the pinned cwd sent them into the wrong repo, 2026-09-12).
  (env -u CAPTAIN_CWD captain brain >>"$BRAIN_LOG" 2>&1) &
  local pid=$!
  disown $pid 2>/dev/null || true  # no job-control obituaries in the TUI when a deploy restarts the brain
  echo $pid > "$BRAIN_PID_FILE"
  for i in $(seq 1 30); do
    if curl -s "$BRAIN_URL/v1/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.3
  done
  return 1
}

SUPERVISOR_PID_FILE="/tmp/captain-supervisor.pid"
# One supervisor per machine, whichever launcher started it: two launchers
# each restarting the brain raced for the port ("address already in use" in
# the brain log, 2026-09-11). The pid file is claimed atomically so a second
# launcher starting at the same instant does not get a supervisor too.
supervise_brain() {
  if [ -f "$SUPERVISOR_PID_FILE" ] && kill -0 "$(cat "$SUPERVISOR_PID_FILE")" 2>/dev/null; then
    return
  fi
  if ! ( set -o noclobber; echo $$ > "$SUPERVISOR_PID_FILE" ) 2>/dev/null; then
    if kill -0 "$(cat "$SUPERVISOR_PID_FILE" 2>/dev/null)" 2>/dev/null; then
      return
    fi
    rm -f "$SUPERVISOR_PID_FILE"
    ( set -o noclobber; echo $$ > "$SUPERVISOR_PID_FILE" ) 2>/dev/null || return
  fi
  (
    while true; do
      if ! curl -s "$BRAIN_URL/v1/health" >/dev/null 2>&1; then
        echo "→ [supervisor] brain down, restarting" >> "$BRAIN_LOG"
        # ALL supervisor output goes to the log - its stdout is the TUI's
        # terminal, and restart chatter corrupts the input field (2026-07-25).
        start_brain >>"$BRAIN_LOG" 2>&1 || true
      fi
      sleep 5
    done
  ) >>"$BRAIN_LOG" 2>&1 </dev/null &
  SUPERVISOR_PID=$!
  disown $SUPERVISOR_PID 2>/dev/null || true
  echo $SUPERVISOR_PID > "$SUPERVISOR_PID_FILE"
}

# busy_count is how many legs /v1/workers currently has in flight. A workflow
# that has started but not yet dispatched a leg still counts as one: killing
# the brain then drops the director mid-plan. Brain down → 0 (nothing to keep).
busy_count() {
  local json n wf
  json=$(curl -s -m 3 "$BRAIN_URL/v1/workers" 2>/dev/null || true)
  n=$(printf '%s' "$json" | sed -n 's/.*"busy":\([0-9][0-9]*\).*/\1/p')
  n=${n:-0}
  wf=$(curl -s -m 3 "$BRAIN_URL/v1/workflow/status" 2>/dev/null || true)
  if printf '%s' "$wf" | grep -q '"active":true' && [ "$n" -eq 0 ]; then
    n=1
  fi
  printf '%s\n' "$n"
}

# confirm_stop asks before killing in-flight work; $1 names the action in the
# prompt ("restart" or "stop"). Empty / anything but Y keeps the brain. No TTY
# → refuse (a script must not kill a live turn).
confirm_stop() {
  local action="${1:-restart}" n ans
  n=$(busy_count)
  case "$n" in
    ''|0) return 0 ;;
  esac
  if [ ! -t 0 ]; then
    echo "✗ $n workers are still active - $action refused (no TTY to confirm)" >&2
    return 1
  fi
  if [ "$n" -eq 1 ]; then
    printf '1 worker is still active are you sure to want to %s? Y/n ' "$action" >&2
  else
    printf '%s workers are still active are you sure to want to %s? Y/n ' "$n" "$action" >&2
  fi
  read -r ans || ans=
  case "$ans" in
    Y|y|yes|YES) return 0 ;;
  esac
  echo "→ $action cancelled" >&2
  return 1
}

# stop_brain takes the shared brain and its supervisor down - every open TUI
# loses its workers, so only `captaincode.sh down` calls it, and only after
# confirm_stop.
# reap_workers ends the CLI workers a dying brain would orphan: claude -p,
# codex exec and cursor-agent are direct children of the brain process, and
# a brain that exits leaves them re-parented to init, still running, their
# answer going nowhere (a 26-minute claude -p in arc after an -rr, 2026-09-19).
# Only children of THIS brain are touched - a claude -p the user runs by hand
# has another parent. Called before the brain itself is killed.
reap_workers() {
  local bpid child cmd
  for bpid in $(pgrep -f "captain brain" 2>/dev/null); do
    for child in $(pgrep -P "$bpid" 2>/dev/null); do
      cmd=$(ps -o command= -p "$child" 2>/dev/null)
      case "$cmd" in
        "claude -p"*|*"/claude -p"*|"codex exec"*|*"/codex exec"*|*"cursor-agent"*)
          echo "  ending worker $child left by the brain: ${cmd:0:60}"
          kill "$child" 2>/dev/null; (sleep 3; kill -9 "$child" 2>/dev/null) &
          ;;
      esac
    done
  done
}

stop_brain() {
  reap_workers
  if [ -f "$SUPERVISOR_PID_FILE" ]; then
    kill "$(cat "$SUPERVISOR_PID_FILE")" 2>/dev/null || true
    rm -f "$SUPERVISOR_PID_FILE"
  fi
  if [ -n "${SUPERVISOR_PID:-}" ]; then
    kill "$SUPERVISOR_PID" 2>/dev/null || true
  fi
  if [ -f "$BRAIN_PID_FILE" ]; then
    kill "$(cat "$BRAIN_PID_FILE")" 2>/dev/null || true
    rm -f "$BRAIN_PID_FILE"
  fi
  # NEVER bare 'pkill -f brain' - it matches any process mentioning "brain".
  pkill -f "captain brain" 2>/dev/null || true
  # The worker serve the brain spawned (opencode serve on 14096) goes with
  # it: a restart that kept it left the workers on a serve that had loaded
  # the config and the plugin two days earlier - no redaction hooks, no
  # proxy routes - with 120 leaked `captain euclid mcp` children under it
  # (live 2026-09-15). Only a process that IS that serve is touched.
  local spid
  for spid in $(lsof -nP -iTCP:14096 -sTCP:LISTEN -t 2>/dev/null); do
    if ps -o command= -p "$spid" 2>/dev/null | grep -q "opencode serve"; then
      kill "$spid" 2>/dev/null || true
    fi
  done
}

doctor() {
  echo "captain doctor"
  echo "env files: captain=$([ -f "$HOME/.config/captain/env" ] && echo present || echo missing) opencode=$([ -f "$HOME/.config/opencode/env" ] && echo present || echo missing) local-env=$([ -f "$LAUNCH_DIR/.env" ] && echo present || echo missing)"
  echo "launch dir: $LAUNCH_DIR"
  local hc
  hc=$(brain_health)
  if [ -n "$hc" ]; then
    echo "brain: up (supervised)"
    echo "$hc"
  else
    echo "brain: down"
  fi
  if command -v captain >/dev/null; then echo "captain: $(command -v captain)"; else echo "captain: not in PATH"; fi
  if command -v opencode >/dev/null; then echo "opencode: $(command -v opencode)"; else echo "opencode: not in PATH"; fi
  if command -v bun >/dev/null; then echo "bun: $(bun --version)"; else echo "bun: missing"; fi
  if command -v opencode >/dev/null; then opencode doctor 2>/dev/null || true; fi
}

up() {
  # A running brain is reused whatever folder it was started from: this TUI
  # tells it its own folder on every request.
  if [ -z "$(brain_health)" ]; then
    if ! start_brain; then
      echo "✗ brain failed to start - see $BRAIN_LOG"; exit 1
    fi
  fi
  supervise_brain
  if curl -s "$BRAIN_URL/v1/health" >/dev/null 2>&1; then
    echo "✓ brain up on :14097 (supervised, shared by every captain-code TUI; this one works in $LAUNCH_DIR)"
  else
    echo "✗ brain not healthy"; exit 1
  fi
}

# rebuild: compile the captain binary from this checkout and install it where
# the launcher finds it. `restart` alone never rebuilds - it restarts whatever
# is on PATH, which is how a brain ran a day-old binary after a restart
# (2026-09-16). Build into a temp file first so a failed build leaves the
# installed binary untouched; install -m 755 replaces the inode, so a running
# brain keeps its old one until it is restarted.
rebuild() {
  command -v go >/dev/null || { echo "✗ go is not on PATH - cannot rebuild captain" >&2; exit 1; }
  local dest="${CAPTAIN_BIN_DEST:-$HOME/.local/bin/captain}"
  local rev; rev=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo "?")
  local dirty; dirty=$(git -C "$SCRIPT_DIR" status --porcelain --untracked-files=no 2>/dev/null | wc -l | tr -d ' ')
  echo "→ building captain from $SCRIPT_DIR ($rev${dirty:+, $dirty modified file(s)})…"
  local tmp; tmp=$(mktemp "${TMPDIR:-/tmp}/captain.build.XXXXXX")
  if ! (cd "$SCRIPT_DIR" && go build -o "$tmp" ./cmd/captaincode); then
    rm -f "$tmp"; echo "✗ build failed - $dest left as it was" >&2; exit 1
  fi
  install -m 755 "$tmp" "$dest" && rm -f "$tmp"
  echo "✓ installed $dest ($rev)"
}

# tuis lists every captain-code TUI on the machine and reaps the ORPHANS: a
# TUI whose terminal is gone (no controlling tty) keeps its render loop at a
# full core and, in one case, grew to 48 GB - eight of them made the machine
# unresponsive (2026-09-19). A TUI with a terminal is never touched.
tuis() {
  local mode="${1:-list}" line pid tty cmd n=0 orphans=0
  while IFS= read -r line; do
    pid=$(printf '%s' "$line" | awk '{print $1}')
    tty=$(printf '%s' "$line" | awk '{print $2}')
    cmd=$(printf '%s' "$line" | cut -d' ' -f3- | sed 's/^ *//')
    case "$cmd" in
      "opencode serve"*|*"opencode run "*) continue ;; # the worker serve and headless runs are not TUIs
    esac
    n=$((n+1))
    if [ "$tty" = "??" ]; then
      orphans=$((orphans+1))
      if [ "$mode" = "reap" ]; then
        kill "$pid" 2>/dev/null; sleep 1; kill -9 "$pid" 2>/dev/null || true
        echo "  reaped orphan TUI $pid (no terminal): ${cmd#opencode }"
      else
        echo "  $pid  (no terminal - orphan)  ${cmd#opencode }"
      fi
    else
      echo "  $pid  $tty  ${cmd#opencode }"
    fi
  done <<EOF2
$(ps -eo pid=,tty=,command= | grep -E '^ *[0-9]+ +\S+ +opencode ' | grep -v grep)
EOF2
  [ "$n" -eq 0 ] && echo "  no captain-code TUI is open"
  if [ "$mode" = "list" ] && [ "$orphans" -gt 0 ]; then
    echo "  $orphans orphan(s): \`captaincode.sh tuis reap\` ends them (a restart does it too)"
  fi
}

case "${1:-}" in
  doctor) doctor; exit 0 ;;
  tuis) tuis "${2:-list}"; exit 0 ;;
  brain) shift; exec captain brain "$@" ;;
  up) up; exit 0 ;;
  down) confirm_stop stop || exit 1; stop_brain; tuis reap; echo "✓ brain stopped"; exit 0 ;;
  restart)
    confirm_stop restart || exit 1
    stop_brain; tuis reap; echo "✓ brain stopped"; up; exit 0 ;;
  rebuild) rebuild; exit 0 ;;
  -rr|rebuild-restart)
    rebuild
    confirm_stop restart || exit 1
    stop_brain; tuis reap; echo "✓ brain stopped"; up; exit 0 ;;
esac

up

# A brain older than the captain binary serves a sidebar loaded from source:
# the sidebar calls endpoints the brain never had and the click fails with a
# 404 (the ⇡ retarget, 2026-09-13). Say so at launch; the restart is the
# user's call - and it asks first if any worker is still running.
brain_built=$(brain_health | sed -n 's/.*"built":\([0-9]*\).*/\1/p')
if command -v captain >/dev/null; then
  bin_built=$(stat -f %m "$(command -v captain)" 2>/dev/null || stat -c %Y "$(command -v captain)" 2>/dev/null || echo 0)
  if [ -z "$brain_built" ] || [ "${bin_built:-0}" -gt "${brain_built:-0}" ]; then
    echo "! the running brain predates the captain binary on PATH - \`$(basename "$0") restart\` picks the new one up (ends the workers of every open TUI)"
  fi
fi

# The launch check: is the main brain (~/.euclid) filled, does this project have
# a local brain that inherited the doctrine, what waits for a distill. Two
# lines, filesystem only, never blocks (see `captain euclid check`).
captain euclid check 2>/dev/null || true
# …and the launch ensure, in the background so the TUI never waits for it:
# the main brain and this repo's brain exist (scaffolded and bootstrapped
# from the repo docs on the first launch here) and both indexes are rebuilt,
# so the memory is searchable and the dashboards current on every launch
# (CAPTAIN_EUCLID=0 turns the layer off). Output goes to the brain log.
(captain euclid ensure >>"$BRAIN_LOG" 2>&1 </dev/null &)
echo "→ launching captain-code in $LAUNCH_DIR - every prompt routes through the director"
echo "  watch:  tail -f /tmp/captain-route.log   |   route timing in /tmp/captain-brain.log"

# STOCK opencode. Routing, the sidebar, the wordmark and the director tag are
# plugins (registered by `captain init` into ~/.config/opencode/opencode.jsonc
# and ~/.config/opencode/tui.json) rather than patches, so this repository
# carries no opencode source and there is nothing to build but the Go brain.
# Forward extra flags to the TUI: -c/--continue resumes the last session,
# -s <id> a specific one, --fork branches off it (see opencode tui --help).
# Each folder's TUI gets its own state directory (prompt history behind the
# arrow keys, last model, preferences): stock opencode keeps one for the whole
# machine, so the arrow keys in one project scrolled through another's prompts.
# Prepared on first use, seeded from the machine-wide state and from this
# folder's own past sessions (`captain state`). Plugin metadata stays machine-wide.
if state_home=$(captain state "$LAUNCH_DIR" 2>/dev/null) && [ -n "$state_home" ]; then
  export XDG_STATE_HOME="$state_home"
  export OPENCODE_PLUGIN_META_FILE="${OPENCODE_PLUGIN_META_FILE:-$HOME/.local/state/opencode/plugin-meta.json}"
fi
if ! command -v opencode >/dev/null; then
  echo "opencode is not on PATH - install it (curl -fsSL https://opencode.ai/install | bash)" >&2
  exit 1
fi
if ! grep -q "plugin/captain.ts" "$HOME/.config/opencode/opencode.jsonc" 2>/dev/null; then
  echo "→ registering the captain plugins (first run on stock opencode)"
  captain init >/dev/null || true
fi
# The captain provider must name this TUI's folder on every call (X-Captain-Cwd),
# or the shared brain runs its workers in whatever folder it was started from.
if ! grep -q "X-Captain-Cwd" "$HOME/.config/opencode/opencode.jsonc" 2>/dev/null; then
  echo "→ adding the workspace header to the captain provider (one brain, many TUIs)"
  captain init >/dev/null || true
fi
# A leg the registry knows and the TUI does not (no /grok-max command, no
# captain/grok-max model, 2026-09-15): `captain init --check` exits 3 on such
# drift, and init is idempotent - apply it, say so.
# (set -e: the non-zero exit must be caught in the test itself, or the
# launcher dies here without a word - live 2026-09-15, a blank terminal.)
drift=0
captain init --check >/dev/null 2>&1 || drift=$?
if [ "$drift" -eq 3 ]; then
  echo "→ the TUI's commands and models lag the leg registry - captain init"
  captain init 2>/dev/null | sed -n 's/^  • /  /p' || true
fi
# CAPTAIN_ROUTE_PLUGIN is exported HERE, on the TUI process only. The brain was
# started above without it, so the `opencode serve` it spawns for workers does
# not route - otherwise every worker turn would bounce back into the brain.
# -c/--continue resolved HERE, to this folder's own newest session: stock
# opencode's --continue takes the newest session of the whole project (the
# git root), and a probe once run in a since-deleted subfolder was resumed
# in the repo two days later - every prompt then died on realPath ENOENT
# before reaching the brain (2026-09-13). No session in this exact folder:
# start fresh, and say so.
tui_args=()
for a in "$@"; do
  case "$a" in
    -c|--continue)
      if last=$(captain state --last "$LAUNCH_DIR" 2>/dev/null) && [ -n "$last" ]; then
        tui_args+=(-s "$last")
      else
        echo "  (no earlier session in $LAUNCH_DIR itself - starting a fresh one)"
      fi ;;
    *) tui_args+=("$a") ;;
  esac
done
# ${tui_args[@]+"${tui_args[@]}"}: an EMPTY array is "unbound" to macOS's
# bash 3.2 under set -u (live 2026-09-13, the first -c launch with no session).
CAPTAIN_ROUTE_PLUGIN=1 exec opencode "$LAUNCH_DIR" ${tui_args[@]+"${tui_args[@]}"}
