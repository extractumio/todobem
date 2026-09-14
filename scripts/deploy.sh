#!/usr/bin/env sh
# todobem deploy helper  build the single binary and run it, from a git clone, in one step.
#
#   git clone <repo> todobem && cd todobem && ./scripts/deploy.sh
#
# Commands:
#   (none) | start   build, then (re)start in the background (nohup), print the URL
#   run              build, then run in the FOREGROUND (for systemd / a terminal)
#   stop             stop a background instance started here
#   status           show whether a background instance is running
#
# It KEEPS the loopback bind (127.0.0.1) from the security model  nothing is exposed off-host.
# For a remote host, tunnel it:  ssh -L 7788:127.0.0.1:7788 <host>   then open the URL locally.
# Override with env: ADDR=127.0.0.1:9000  CODEX=~/.codex  RULES=/path/rules.json  AUTH=/path/auth.key
# (AUTH=off leaves the UI open)  todobem flags after --
#
# The UI is locked until a one-time token is used. `start` prints a login link (one use, 5 min);
# later: ./todobem token   (or ./todobem token -revoke to end every session).
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
BINARY=${BINARY:-todobem}
ADDR=${ADDR:-127.0.0.1:7788}
CODEX=${CODEX:-$HOME/.codex}
RULES=${RULES:-}
AUTH=${AUTH:-}
PIDFILE=${PIDFILE:-$ROOT/todobem.pid}
LOGFILE=${LOGFILE:-$ROOT/todobem.out}
CMD=${1:-start}
[ $# -gt 0 ] && shift || true

need_go() {
  command -v go >/dev/null 2>&1 || { echo "error: Go toolchain not found. Install Go 1.22+ (https://go.dev/dl) and re-run." >&2; exit 1; }
}

running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

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
  status) if running; then echo "running (pid $(cat "$PIDFILE"))  log: $LOGFILE"; else echo "not running"; fi ;;
  run)
    build; warn_addr
    set -- -addr "$ADDR" -codex "$CODEX" -open=false "$@"
    [ -n "$RULES" ] && set -- "$@" -rules "$RULES"
    [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
    echo "running in foreground: ./$BINARY $*"
    echo "login link: run  ./$BINARY token  in another terminal"
    exec "./$BINARY" "$@" ;;
  start|"")
    build; warn_addr
    running && stop
    set -- -addr "$ADDR" -codex "$CODEX" -open=false "$@"
    [ -n "$RULES" ] && set -- "$@" -rules "$RULES"
    [ -n "$AUTH" ] && set -- "$@" -auth "$AUTH"
    : >"$LOGFILE"; chmod 600 "$LOGFILE"   # the log names sessions; keep it to this user
    nohup "./$BINARY" "$@" >>"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"
    sleep 1
    if running; then url_hint; login_hint; echo "logs: $LOGFILE  stop: ./scripts/deploy.sh stop"; else echo "failed to start; see $LOGFILE" >&2; tail -n 20 "$LOGFILE" >&2 || true; exit 1; fi ;;
  *) echo "usage: $0 [start|run|stop|status] [-- extra todobem flags]" >&2; exit 2 ;;
esac
