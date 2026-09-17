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
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
BINARY=${BINARY:-todobem}
ADDR=${ADDR:-127.0.0.1:7788}
CODEX=${CODEX:-}
CLAUDE=${CLAUDE:-}
RULES=${RULES:-}
AUTH=${AUTH:-}
PIDFILE=${PIDFILE:-$ROOT/todobem.pid}
LOGFILE=${LOGFILE:-$ROOT/todobem.out}
CMD=${1:-start}
[ $# -gt 0 ] && shift || true

need_go() {
  command -v go >/dev/null 2>&1 || { echo "error: Go toolchain not found. Install Go 1.22+ (https://go.dev/dl) and re-run." >&2; exit 1; }
}

port=${ADDR##*:}
cmdline() { ps -o command= -p "$1" 2>/dev/null; }
# listener prints the pid listening on ADDR's port (lsof: macOS and most Linux; none → nothing)
listener() { lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -n 1; }
# running: the pidfile's process when it is still a todobem, else the todobem on the port,
# adopted into the pidfile. Anything else on the port is not ours: `busy` names it.
running() {
  pid=$(cat "$PIDFILE" 2>/dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    case "$(cmdline "$pid")" in *todobem*) return 0 ;; esac
  fi
  rm -f "$PIDFILE"
  pid=$(listener)
  [ -n "$pid" ] || return 1
  case "$(cmdline "$pid")" in *todobem*) echo "$pid" >"$PIDFILE"; return 0 ;; esac
  return 1
}
busy() { pid=$(listener); [ -n "$pid" ] && echo "$pid $(cmdline "$pid")"; }
# strays: every other todobem server on this machine — a preview left on another port, say.
# Nothing started for a task may outlive it; this is where a leftover shows up.
strays() {
  mine=$(cat "$PIDFILE" 2>/dev/null || echo 0)
  for pid in $(pgrep -f '^[^ ]*todobem[^ /]* .*-addr' 2>/dev/null); do
    [ "$pid" = "$mine" ] && continue
    echo "  other todobem server: pid $pid  $(cmdline "$pid")"
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

build() { need_go; echo "building $BINARY"; go build -o "$BINARY" ./cmd/todobem; }

warn_addr() {
  case "$ADDR" in
    127.0.0.1:*|localhost:*|"[::1]:"*) : ;;
    *) echo "WARNING: ADDR=$ADDR is not loopback  this exposes the local session viewer on the network. Prefer an ssh -L tunnel instead." >&2 ;;
  esac
}

url_hint() {
  host=${ADDR%:*}; port=${ADDR##*:}
  echo "todobem: http://$ADDR/"
  case "$host" in 127.0.0.1|localhost|"[::1]") echo "remote host? tunnel it:  ssh -L $port:127.0.0.1:$port <this-host>  then open http://127.0.0.1:$port/" ;; esac
}

# login_hint prints a one-time login link to THIS terminal only — never into the log file.
login_hint() {
  if [ "$AUTH" = off ]; then echo "auth: OFF"; return; fi
  set -- -addr "$ADDR"
  [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
  "./$BINARY" token "$@" || echo "could not mint a login token; run: ./$BINARY token" >&2
}

case "$CMD" in
  stop) stop ;;
  status)
    if running; then echo "running (pid $(cat "$PIDFILE"))  log: $LOGFILE"
    elif other=$(busy); then echo "not running; $ADDR is taken by: $other"
    else echo "not running"; fi
    strays ;;
  run)
    build; warn_addr
    set -- -addr "$ADDR" -open=false "$@"
    [ -n "$CODEX" ] && set -- "$@" -codex "$CODEX"
    [ -n "$CLAUDE" ] && set -- "$@" -claude "$CLAUDE"
    [ -n "$RULES" ] && set -- "$@" -rules "$RULES"
    [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
    echo "running in foreground: ./$BINARY $*"
    echo "login link: run  ./$BINARY token  in another terminal"
    exec "./$BINARY" "$@" ;;
  start|"")
    build; warn_addr
    running && stop
    if other=$(busy); then echo "error: $ADDR is taken by another program: $other" >&2; exit 1; fi
    set -- -addr "$ADDR" -open=false "$@"
    [ -n "$CODEX" ] && set -- "$@" -codex "$CODEX"
    [ -n "$CLAUDE" ] && set -- "$@" -claude "$CLAUDE"
    [ -n "$RULES" ] && set -- "$@" -rules "$RULES"
    [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
    : >"$LOGFILE"; chmod 600 "$LOGFILE"   # the log names sessions; keep it to this user
    nohup "./$BINARY" "$@" >>"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"
    sleep 1
    if running; then url_hint; login_hint; echo "logs: $LOGFILE  stop: ./scripts/deploy.sh stop"; strays; else echo "failed to start; see $LOGFILE" >&2; tail -n 20 "$LOGFILE" >&2 || true; exit 1; fi ;;
  *) echo "usage: $0 [start|run|stop|status] [-- extra todobem flags]" >&2; exit 2 ;;
esac
