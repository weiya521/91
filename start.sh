#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export HOME="${HOME:-/root}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$HOME/.cache}"
export GOCACHE="${GOCACHE:-/tmp/video-site-91/go-build}"

FRONTEND_HOST="${FRONTEND_HOST:-0.0.0.0}"
FRONTEND_PORT="${FRONTEND_PORT:-9191}"
FRONTEND_MODE="${FRONTEND_MODE:-preview}"
BACKEND_PORT="${BACKEND_PORT:-9192}"
LOG_DIR="${LOG_DIR:-/tmp/video-site-91}"

FRONTEND_LOG="$LOG_DIR/frontend.log"
BACKEND_LOG="$LOG_DIR/backend.log"

usage() {
  cat <<EOF
Usage: ./start.sh [--restart|--stop|--status]

Environment overrides:
  FRONTEND_HOST=$FRONTEND_HOST
  FRONTEND_PORT=$FRONTEND_PORT
  FRONTEND_MODE=$FRONTEND_MODE  # preview (default, no HMR) or dev
  BACKEND_PORT=$BACKEND_PORT
  LOG_DIR=$LOG_DIR

Logs:
  frontend: $FRONTEND_LOG
  backend:  $BACKEND_LOG
EOF
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

pids_on_port() {
  local port="$1"
  ss -ltnp 2>/dev/null \
    | awk -v needle=":$port" '$4 ~ needle {print $0}' \
    | sed -nE 's/.*pid=([0-9]+).*/\1/p' \
    | sort -u
}

print_port_status() {
  local name="$1"
  local port="$2"
  local pids
  pids="$(pids_on_port "$port" | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  if [[ -n "$pids" ]]; then
    echo "$name listening on port $port (pid: $pids)"
  else
    echo "$name not listening on port $port"
  fi
}

stop_port() {
  local name="$1"
  local port="$2"
  local pids
  pids="$(pids_on_port "$port" | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  if [[ -z "$pids" ]]; then
    echo "$name is not running on port $port"
    return
  fi

  echo "stopping $name on port $port (pid: $pids)"
  kill $pids 2>/dev/null || true

  for _ in $(seq 1 20); do
    if [[ -z "$(pids_on_port "$port")" ]]; then
      return
    fi
    sleep 0.2
  done

  echo "$name did not stop gracefully; sending SIGKILL"
  kill -9 $pids 2>/dev/null || true
}

wait_for_port() {
  local name="$1"
  local port="$2"
  for _ in $(seq 1 60); do
    if [[ -n "$(pids_on_port "$port")" ]]; then
      print_port_status "$name" "$port"
      return 0
    fi
    sleep 0.5
  done
  echo "$name did not start on port $port. Check logs in $LOG_DIR" >&2
  return 1
}

start_backend() {
  if [[ -n "$(pids_on_port "$BACKEND_PORT")" ]]; then
    print_port_status "backend" "$BACKEND_PORT"
    return
  fi

  need_cmd go
  mkdir -p "$LOG_DIR" "$GOCACHE"
  echo "starting backend on 127.0.0.1:$BACKEND_PORT"
  (
    cd "$ROOT_DIR/backend"
    setsid nohup go run ./cmd/server >>"$BACKEND_LOG" 2>&1 </dev/null &
  )
  wait_for_port "backend" "$BACKEND_PORT"
}

start_frontend() {
  if [[ -n "$(pids_on_port "$FRONTEND_PORT")" ]]; then
    print_port_status "frontend" "$FRONTEND_PORT"
    return
  fi

  need_cmd npm
  mkdir -p "$LOG_DIR"
  if [[ "$FRONTEND_MODE" == "dev" ]]; then
    echo "starting frontend dev server on $FRONTEND_HOST:$FRONTEND_PORT"
    (
      cd "$ROOT_DIR"
      setsid nohup npm run dev -- --host "$FRONTEND_HOST" --port "$FRONTEND_PORT" >>"$FRONTEND_LOG" 2>&1 </dev/null &
    )
  else
    echo "building frontend for preview mode"
    (
      cd "$ROOT_DIR"
      npm run build >>"$FRONTEND_LOG" 2>&1
    )
    echo "starting frontend preview server on $FRONTEND_HOST:$FRONTEND_PORT"
    (
      cd "$ROOT_DIR"
      setsid nohup npm run preview -- --host "$FRONTEND_HOST" --port "$FRONTEND_PORT" >>"$FRONTEND_LOG" 2>&1 </dev/null &
    )
  fi
  wait_for_port "frontend" "$FRONTEND_PORT"
}

main() {
  local action="${1:-start}"

  case "$action" in
    start)
      need_cmd ss
      start_backend
      start_frontend
      echo
      echo "ready:"
      echo "  frontend: http://127.0.0.1:$FRONTEND_PORT/"
      echo "  backend:  http://127.0.0.1:$BACKEND_PORT/"
      ;;
    --restart|restart)
      need_cmd ss
      stop_port "frontend" "$FRONTEND_PORT"
      stop_port "backend" "$BACKEND_PORT"
      start_backend
      start_frontend
      echo
      echo "restarted:"
      echo "  frontend: http://127.0.0.1:$FRONTEND_PORT/"
      echo "  backend:  http://127.0.0.1:$BACKEND_PORT/"
      ;;
    --stop|stop)
      need_cmd ss
      stop_port "frontend" "$FRONTEND_PORT"
      stop_port "backend" "$BACKEND_PORT"
      ;;
    --status|status)
      need_cmd ss
      print_port_status "frontend" "$FRONTEND_PORT"
      print_port_status "backend" "$BACKEND_PORT"
      ;;
    -h|--help|help)
      usage
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
