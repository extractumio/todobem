#!/usr/bin/env sh
# todobem deploy helper  build the single binary and run it, from a git clone, in one step.
#
#   git clone <repo> todobem && cd todobem && ./scripts/deploy.sh
#
# Commands:
#   (none) | start   build, then (re)start in the background (nohup), print the URL
#   run              build, then run in the FOREGROUND (for systemd / a terminal)
#   stop             stop the instance on ADDR
#   status           show whether an instance is running on ADDR, and any other todobem server
#
# The instance is whatever listens on ADDR: the pidfile remembers the one started here, and a
# todobem started by hand (or one whose pidfile was lost) is adopted into it, so stop, status
# and a restart always find the server the browser is talking to. `start` replaces it, and the
# open tab follows the new build by itself (the server names its build on every answer).
# Finding the listener needs lsof (macOS, most Linux); without it the pidfile is all there is.
#
# It KEEPS the loopback bind (127.0.0.1) from the security model  nothing is exposed off-host.
# For a remote host, tunnel it:  ssh -L 7788:127.0.0.1:7788 <host>   then open the URL locally.
# Override with env: ADDR=127.0.0.1:9000  RULES=/path/rules.json  AUTH=/path/auth.key
# CODEX=/path/codex and CLAUDE=/path/claude pin this run to exactly those folders (a source not
# named is off; the Settings page is then read-only); unset, the server reads the folders of
# ~/.todobem/settings.json, else ~/.codex and ~/.claude.
# (AUTH=off leaves the UI open)  todobem flags after --
#
# The UI is locked until a one-time token is used. `start` prints a login link (one use, 5 min);
# later: ./todobem token   (or ./todobem token -revoke to end every session).
#
# AGENT=1 runs agent mode instead (docs/AGENT-MODE.md): headless, TLS on ADDR (default :7789,
# every interface), answering a paired hub. `start` then prints a pairing string (one use, 5 min)
# for `todobem hub add` on the hub; later: ./todobem agent pair.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
BINARY=${BINARY:-todobem}
case "$BINARY" in
  /*) BIN=$BINARY ;;
  *) BIN_DIR=$(CDPATH= cd -- "$(dirname -- "$BINARY")" && pwd); BIN=$BIN_DIR/$(basename -- "$BINARY") ;;
esac
AGENT=${AGENT:-}
if [ -n "$AGENT" ]; then ADDR=${ADDR:-:7789}; PIDFILE=${PIDFILE:-$ROOT/todobem-agent.pid}; LOGFILE=${LOGFILE:-$ROOT/todobem-agent.out}; fi
ADDR=${ADDR:-127.0.0.1:7788}
CODEX=${CODEX:-}
CLAUDE=${CLAUDE:-}
RULES=${RULES:-}
AUTH=${AUTH:-}
PIDFILE=${PIDFILE:-$ROOT/todobem.pid}
LOGFILE=${LOGFILE:-$ROOT/todobem.out}
CMD=${1:-start}
[ $# -gt 0 ] && shift || true
[ "${1:-}" = "--" ] && shift || true

need_go() {
  command -v go >/dev/null 2>&1 || { echo "error: Go toolchain not found. Install Go 1.22+ (https://go.dev/dl) and re-run." >&2; exit 1; }
}

port=${ADDR##*:}
cmdline() { ps -o command= -p "$1" 2>/dev/null; }
proc_cwd() { lsof -a -p "$1" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1; }
# is_binary identifies exactly the executable this deployment manages. is_todobem is broader
# for the stray report: it also recognizes the conventional binary names.
is_binary() {
  line=$(cmdline "$1") || return 1
  first=${line%% *}
  case "$first" in
    /*) actual=$first ;;
    *) cwd=$(proc_cwd "$1"); actual=$cwd/${first#./} ;;
  esac
  [ "$actual" = "$BIN" ] && return 0
  # `go run ./cmd/todobem` uses a temporary executable but keeps the repository cwd. Preserve
  # hand-start adoption for the default binary without weakening custom BINARY matching.
  [ "$BIN" = "$ROOT/todobem" ] && [ "${first##*/}" = todobem ] && [ "$(proc_cwd "$1")" = "$ROOT" ]
}
is_todobem() {
  line=$(cmdline "$1") || return 1
  first=${line%% *}
  base=${first##*/}
  if [ "$base" = "${BIN##*/}" ]; then return 0; fi
  case "$base" in todobem|todobem-*) return 0 ;; esac
  return 1
}
# is_instance also checks this invocation's address and mode. A shared or stale pidfile must
# never let a custom ADDR adopt (and later stop) a different todobem process.
is_instance() {
  line=$(cmdline "$1") || return 1
  is_binary "$1" || return 1
  case " $line " in
    *" -addr $ADDR "*|*" -addr=$ADDR "*) : ;;
    *)
      [ -z "$AGENT" ] && [ "$ADDR" = 127.0.0.1:7788 ] || return 1
      case " $line " in *" -addr "*|*" -addr="*) return 1 ;; esac
      ;;
  esac
  if [ -n "$AGENT" ]; then case " $line " in *" -agent "*) : ;; *) return 1 ;; esac
  else case " $line " in *" -agent "*) return 1 ;; esac
  fi
}
listeners() { lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | sort -u; }
# listener prints the matching todobem pid on ADDR's port (none → nothing).
listener() {
  for candidate in $(listeners); do
    if is_instance "$candidate"; then echo "$candidate"; return; fi
  done
}
# running: the pidfile's process when it is still a todobem, else the todobem on the port,
# adopted into the pidfile. Anything else on the port is not ours: `busy` names it.
running() {
  pid=$(cat "$PIDFILE" 2>/dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && is_instance "$pid"; then return 0; fi
  rm -f "$PIDFILE"
  pid=$(listener)
  [ -n "$pid" ] || return 1
  if is_todobem "$pid"; then echo "$pid" >"$PIDFILE"; return 0; fi
  return 1
}
busy() { pid=$(listeners | head -n 1); [ -n "$pid" ] && echo "$pid $(cmdline "$pid")"; }
# strays: every other todobem server listening on this machine — a preview left on another
# port, say. Listed, never stopped: an instance pinned to a test folder is legitimate. Nothing
# started for a task may outlive it; this is where a leftover shows up.
strays() {
  mine=$(cat "$PIDFILE" 2>/dev/null || echo 0)
  lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk -v mine="$mine" 'NR > 1 && $2 != mine { print $2, $9 }' | sort -u | while read -r other_pid addr; do
    if is_todobem "$other_pid"; then echo "  other todobem server: pid $other_pid on $addr  $(cmdline "$other_pid")"; fi
  done
}

stop() {
  if running; then
    pid=$(cat "$PIDFILE")
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do kill -0 "$pid" 2>/dev/null || break; sleep 0.3; done
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
    echo "stopped todobem (pid $pid)"
  else
    echo "todobem is not running"
  fi
  rm -f "$PIDFILE"
}

build() { need_go; echo "building $BIN"; go build -o "$BIN" ./cmd/todobem; }

# launch runs todobem with this run's arguments — the mode and the address, the caller's extra
# flags, then the folders, rules and auth the environment names — in the foreground (fg: exec)
# or the background (bg: nohup, pidfile). `run` and `start` share it.
launch() {
  how=$1; shift
  if [ -n "$AGENT" ]; then set -- -agent -addr "$ADDR" "$@"; else set -- -addr "$ADDR" -open=false "$@"; fi
  [ -n "$CODEX" ] && set -- "$@" -codex "$CODEX"
  [ -n "$CLAUDE" ] && set -- "$@" -claude "$CLAUDE"
  [ -n "$RULES" ] && set -- "$@" -rules "$RULES"
  [ -n "$AUTH" ] && [ -z "$AGENT" ] && set -- "$@" -auth "$AUTH"
  case "$how" in
    fg)
      echo "running in foreground: $BIN $*"
      if [ -n "$AGENT" ]; then echo "pairing string: run  $BIN agent pair  in another terminal"; else echo "login link: run  $BIN token  in another terminal"; fi
      exec "$BIN" "$@" ;;
    bg)
      : >"$LOGFILE"; chmod 600 "$LOGFILE"   # the log names sessions; keep it to this user
      nohup "$BIN" "$@" >>"$LOGFILE" 2>&1 &
      echo $! >"$PIDFILE" ;;
  esac
}

warn_addr() {
  [ -n "$AGENT" ] && return 0   # an agent listens outward by design: TLS, a bearer on every request
  case "$ADDR" in
    127.0.0.1:*|localhost:*|"[::1]:"*) : ;;
    *) echo "WARNING: ADDR=$ADDR is not loopback  this exposes the local session viewer on the network. Prefer an ssh -L tunnel instead." >&2 ;;
  esac
}

url_hint() {
  host=${ADDR%:*}; port=${ADDR##*:}
  if [ -n "$AGENT" ]; then echo "todobem agent: TLS on $ADDR (no UI); the hub pairs with the string below"; return; fi
  echo "todobem: http://$ADDR/"
  case "$host" in 127.0.0.1|localhost|"[::1]") echo "remote host? tunnel it:  ssh -L $port:127.0.0.1:$port <this-host>  then open http://127.0.0.1:$port/" ;; esac
}

# login_hint prints a one-time login link to THIS terminal only — never into the log file.
login_hint() {
  if [ -n "$AGENT" ]; then
    agent_key=
    while [ $# -gt 0 ]; do
      case "$1" in
        -agent-key) shift; [ $# -gt 0 ] && agent_key=$1 ;;
        -agent-key=*) agent_key=${1#-agent-key=} ;;
      esac
      [ $# -gt 0 ] && shift || true
    done
    set -- agent pair -port "${ADDR##*:}"
    [ -n "$agent_key" ] && set -- "$@" -key "$agent_key"
    "$BIN" "$@" || echo "could not mint a pairing string; run: $BIN agent pair" >&2
    return
  fi
  if [ "$AUTH" = off ]; then echo "auth: OFF"; return; fi
  set -- -addr "$ADDR"
  [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
  "$BIN" token "$@" || echo "could not mint a login token; run: $BIN token" >&2
}

case "$CMD" in
  stop) stop ;;
  status)
    if running; then pid=$(cat "$PIDFILE"); echo "running (pid $pid)  $(cmdline "$pid")  log: $LOGFILE"
    elif other=$(busy); then echo "not running; $ADDR is taken by: $other"
    else echo "not running"; fi
    strays ;;
  run)
    build; warn_addr
    launch fg "$@" ;;
  start|"")
    build; warn_addr
    running && stop
    if other=$(busy); then echo "error: $ADDR is taken by another program: $other" >&2; exit 1; fi
    launch bg "$@"
    sleep 1
    if running; then url_hint; login_hint "$@"; echo "logs: $LOGFILE  stop: ./scripts/deploy.sh stop"; strays; else echo "failed to start; see $LOGFILE" >&2; tail -n 20 "$LOGFILE" >&2 || true; exit 1; fi ;;
  *) echo "usage: $0 [start|run|stop|status] [-- extra todobem flags]" >&2; exit 2 ;;
esac
