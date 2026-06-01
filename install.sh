#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="${APP_NAME:-video-site-91}"
GITHUB_REPO="${GITHUB_REPO:-nianzhibai/91}"
INSTALL_PATH="${INSTALL_PATH:-/opt/video-site-91}"
SERVICE_NAME="${SERVICE_NAME:-video-site-91}"
FRONTEND_PORT_WAS_SET="${FRONTEND_PORT+x}"
FRONTEND_PORT="${FRONTEND_PORT:-9191}"
VERSION="${VERSION:-latest}"
GH_PROXY="${GH_PROXY:-}"
CONFIGURE_UFW="${CONFIGURE_UFW:-1}"
INSTALL_DEPS="${INSTALL_DEPS:-1}"
SELF_UPDATE="${SELF_UPDATE:-1}"
FORCE_UPDATE="${FORCE_UPDATE:-0}"
INSTALL_SCRIPT_REF="${INSTALL_SCRIPT_REF:-main}"
INSTALL_SCRIPT_URL="${INSTALL_SCRIPT_URL:-${GH_PROXY}https://raw.githubusercontent.com/${GITHUB_REPO}/${INSTALL_SCRIPT_REF}/install.sh}"
VIDEO_SITE_SKIP_SELF_UPDATE="${VIDEO_SITE_SKIP_SELF_UPDATE:-0}"
SERVICE_READY_TIMEOUT="${SERVICE_READY_TIMEOUT:-90}"
VERSION_FILE="$INSTALL_PATH/.version"
MANAGER_PATH="/usr/local/sbin/${APP_NAME}-manager"
COMMAND_LINK="/usr/local/bin/91"
APP_COMMAND_LINK="/usr/local/bin/${APP_NAME}"

RED='\033[1;31m'
GREEN='\033[1;32m'
YELLOW='\033[1;33m'
BLUE='\033[1;34m'
RESET='\033[0m'

log() {
  printf "${BLUE}[install]${RESET} %s\n" "$*"
}

warn() {
  printf "${YELLOW}[install]${RESET} %s\n" "$*" >&2
}

die() {
  printf "${RED}[install]${RESET} %s\n" "$*" >&2
  exit 1
}

usage() {
  cat <<EOF
Usage:
  sudo bash install.sh [install]
  91 [update|restart|stop|status|logs|uninstall]

Default action:
  install.sh with no args downloads the prebuilt release package and starts the service.
  91 with no args opens the management menu.

Actions:
  install    Install to $INSTALL_PATH
  update     Refresh manager script, download latest release, and keep config/data
  restart    Restart service
  stop       Stop service
  status     Show service status
  logs       Follow service logs
  uninstall  Remove service and optionally delete installed files

Options via environment:
  GITHUB_REPO=$GITHUB_REPO
  VERSION=$VERSION              latest or a release tag such as v0.1.0
  INSTALL_PATH=$INSTALL_PATH
  FRONTEND_PORT=$FRONTEND_PORT
  GH_PROXY=$GH_PROXY
  INSTALL_DEPS=$INSTALL_DEPS
  CONFIGURE_UFW=$CONFIGURE_UFW
  SELF_UPDATE=$SELF_UPDATE
  FORCE_UPDATE=$FORCE_UPDATE
  UNINSTALL_DELETE_FILES=0      Set to 1 for non-interactive uninstall to delete $INSTALL_PATH
  INSTALL_SCRIPT_REF=$INSTALL_SCRIPT_REF
  INSTALL_SCRIPT_URL=$INSTALL_SCRIPT_URL
  SERVICE_READY_TIMEOUT=$SERVICE_READY_TIMEOUT

Examples:
  sudo bash install.sh
  FRONTEND_PORT=8080 sudo -E bash install.sh
  91
  91 update
  91 logs
EOF
}

is_manager_invocation() {
  local name
  name="$(basename "$0")"
  [[ "$name" == "91" || "$name" == "$APP_NAME" || "$name" == "$(basename "$MANAGER_PATH")" ]]
}

need_root() {
  if [[ "$(id -u)" == "0" ]]; then
    return
  fi
  if command -v sudo >/dev/null 2>&1; then
    exec sudo -E bash "$0" "$@"
  fi
  die "please run as root"
}

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "unsupported architecture: $machine" ;;
  esac
}

download_base_url() {
  if [[ "$VERSION" == "latest" ]]; then
    printf '%shttps://github.com/%s/releases/latest/download' "$GH_PROXY" "$GITHUB_REPO"
  else
    printf '%shttps://github.com/%s/releases/download/%s' "$GH_PROXY" "$GITHUB_REPO" "$VERSION"
  fi
}

asset_name() {
  printf '%s-linux-%s.tar.gz' "$APP_NAME" "$ARCH"
}

install_deps() {
  if [[ "$INSTALL_DEPS" != "1" ]]; then
    return
  fi
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    log "installing runtime dependencies"
    apt-get update
    apt-get install -y ca-certificates curl tar ffmpeg openssl iproute2 python3 python3-requests python3-bs4 python3-lxml python3-socks
    return
  fi

  for cmd in curl tar ffmpeg ffprobe openssl; do
    command -v "$cmd" >/dev/null 2>&1 || die "missing command: $cmd"
  done
}

check_system() {
  [[ "$(uname -s)" == "Linux" ]] || die "Linux is required"
  command -v systemctl >/dev/null 2>&1 || die "systemd is required"
  detect_arch
}

check_disk_space() {
  local parent avail
  parent="$(dirname "$INSTALL_PATH")"
  mkdir -p "$parent"
  avail="$(df -Pm "$parent" | awk 'NR==2 {print $4}')"
  if [[ "$avail" =~ ^[0-9]+$ ]] && (( avail < 512 )); then
    die "not enough free space under $parent, need at least 512 MB"
  fi
}

download_file() {
  local url="$1"
  local output="$2"
  local retry=0
  while (( retry < 3 )); do
    if curl -fL --connect-timeout 15 --retry 2 --retry-delay 2 "$url" -o "$output"; then
      [[ -s "$output" ]] && return 0
    fi
    retry=$((retry + 1))
    warn "download failed, retry $retry/3"
    sleep $((retry * 2))
  done
  return 1
}

backup_install_files() {
  local backup="$1"
  mkdir -p "$backup"
  cp -a "$INSTALL_PATH/server" "$backup/server"
  for item in dist config.example.yaml 91VideoSpider config.yaml .version; do
    if [[ -e "$INSTALL_PATH/$item" ]]; then
      cp -a "$INSTALL_PATH/$item" "$backup/$item"
    fi
  done
}

restore_install_files() {
  local backup="$1"
  mkdir -p "$INSTALL_PATH"
  cp -a "$backup/server" "$INSTALL_PATH/server"
  for item in dist config.example.yaml 91VideoSpider config.yaml .version; do
    rm -rf "${INSTALL_PATH:?}/$item"
    if [[ -e "$backup/$item" ]]; then
      cp -a "$backup/$item" "$INSTALL_PATH/$item"
    fi
  done
  chmod +x "$INSTALL_PATH/server"
}

prepare_config() {
  local cfg="$INSTALL_PATH/config.yaml"
  local example="$INSTALL_PATH/config.example.yaml"
  mkdir -p "$INSTALL_PATH/data"

  if [[ ! -f "$cfg" ]]; then
    cp "$example" "$cfg"
    sed -i -E "s#listen: \".*\"#listen: \"0.0.0.0:${FRONTEND_PORT}\"#" "$cfg"
    chmod 600 "$cfg"
    log "created $cfg"
  else
    log "keeping existing $cfg"
    if [[ -n "$FRONTEND_PORT_WAS_SET" ]]; then
      sed -i -E "s#listen: \".*\"#listen: \"0.0.0.0:${FRONTEND_PORT}\"#" "$cfg"
      log "updated listen port to ${FRONTEND_PORT}"
    fi
  fi

  if grep -q 'session_secret: "change-me-to-a-random-string"' "$cfg"; then
    local secret
    secret="$(openssl rand -hex 32)"
    sed -i -E "s#session_secret: \".*\"#session_secret: \"$secret\"#" "$cfg"
    log "generated random session_secret"
  fi
}

write_service() {
  cat >"/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Video Site 91
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_PATH}
ExecStart=${INSTALL_PATH}/server
Restart=on-failure
RestartSec=5
TimeoutStopSec=20
Environment=VIDEO_CONFIG=${INSTALL_PATH}/config.yaml
Environment=VIDEO_FRONTEND_DIR=${INSTALL_PATH}/dist
Environment=VIDEO_VERSION_FILE=${VERSION_FILE}
Environment=VIDEO_GITHUB_REPO=${GITHUB_REPO}
Environment=HOME=/root
Environment=PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${SERVICE_NAME}

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}.service" >/dev/null
}

install_cli() {
  local src
  src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  install_cli_from_file "$src"
}

install_cli_from_file() {
  local src="$1"
  local tmp
  [[ -f "$src" ]] || return 0
  mkdir -p "$(dirname "$MANAGER_PATH")" "$(dirname "$COMMAND_LINK")" "$(dirname "$APP_COMMAND_LINK")"
  tmp="${MANAGER_PATH}.tmp.$$"
  cp "$src" "$tmp"
  chmod 755 "$tmp"
  mv "$tmp" "$MANAGER_PATH"
  ln -sfn "$MANAGER_PATH" "$COMMAND_LINK"
  ln -sfn "$MANAGER_PATH" "$APP_COMMAND_LINK"
}

self_update_manager() {
  [[ "$SELF_UPDATE" == "1" ]] || return 1
  [[ "$VIDEO_SITE_SKIP_SELF_UPDATE" != "1" ]] || return 1
  [[ -n "$INSTALL_SCRIPT_URL" ]] || return 1

  local tmp
  tmp="$(mktemp)"
  log "checking latest manager script"
  if ! download_file "$INSTALL_SCRIPT_URL" "$tmp"; then
    warn "manager self-update skipped: cannot download $INSTALL_SCRIPT_URL"
    rm -f "$tmp"
    return 1
  fi
  if ! bash -n "$tmp"; then
    warn "manager self-update skipped: downloaded script has syntax errors"
    rm -f "$tmp"
    return 1
  fi
  if [[ -f "$MANAGER_PATH" ]] && cmp -s "$tmp" "$MANAGER_PATH"; then
    rm -f "$tmp"
    return 1
  fi

  install_cli_from_file "$tmp"
  rm -f "$tmp"
  log "manager script updated"
  return 0
}

exec_latest_manager_update() {
  local env_args=(
    "VIDEO_SITE_SKIP_SELF_UPDATE=1"
    "APP_NAME=$APP_NAME"
    "GITHUB_REPO=$GITHUB_REPO"
    "INSTALL_PATH=$INSTALL_PATH"
    "SERVICE_NAME=$SERVICE_NAME"
    "VERSION=$VERSION"
    "GH_PROXY=$GH_PROXY"
    "CONFIGURE_UFW=$CONFIGURE_UFW"
    "INSTALL_DEPS=$INSTALL_DEPS"
    "SELF_UPDATE=$SELF_UPDATE"
    "FORCE_UPDATE=$FORCE_UPDATE"
    "INSTALL_SCRIPT_REF=$INSTALL_SCRIPT_REF"
    "INSTALL_SCRIPT_URL=$INSTALL_SCRIPT_URL"
    "SERVICE_READY_TIMEOUT=$SERVICE_READY_TIMEOUT"
  )
  if [[ -n "$FRONTEND_PORT_WAS_SET" ]]; then
    env_args+=("FRONTEND_PORT=$FRONTEND_PORT")
  fi
  exec env "${env_args[@]}" bash "$MANAGER_PATH" update
}

open_firewall_port() {
  [[ "$CONFIGURE_UFW" == "1" ]] || return 0
  command -v ufw >/dev/null 2>&1 || return 0
  if ufw status 2>/dev/null | grep -qi "Status: active"; then
    log "allowing ${FRONTEND_PORT}/tcp in UFW"
    ufw allow "${FRONTEND_PORT}/tcp"
  fi
}

listen_port_from_config() {
  local cfg="$INSTALL_PATH/config.yaml"
  local listen="" port
  if [[ -f "$cfg" ]]; then
    listen="$(sed -nE 's/^[[:space:]]*listen:[[:space:]]*"?([^" #]+)"?.*/\1/p' "$cfg" | head -n1)"
  fi
  port="${listen##*:}"
  if [[ "$port" =~ ^[0-9]+$ ]]; then
    printf '%s' "$port"
    return
  fi
  printf '%s' "$FRONTEND_PORT"
}

append_unique() {
  local value="$1"
  shift
  for existing in "$@"; do
    [[ "$existing" == "$value" ]] && return 1
  done
  printf '%s' "$value"
}

app_service_names() {
  local names=()
  local name
  for name in "$SERVICE_NAME" "$APP_NAME" video-site-91 video-site-backend video-site-frontend; do
    [[ -n "$name" ]] || continue
    if append_unique "$name" "${names[@]}" >/dev/null; then
      names+=("$name")
    fi
  done
  printf '%s\n' "${names[@]}"
}

stop_app_services() {
  local name unit
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    unit="${name}.service"
    systemctl disable --now "$unit" 2>/dev/null || systemctl stop "$unit" 2>/dev/null || true
    rm -f "/etc/systemd/system/$unit"
  done < <(app_service_names)
  systemctl daemon-reload
}

remove_app_containers() {
  command -v docker >/dev/null 2>&1 || return 0

  local names=()
  local name
  for name in "$SERVICE_NAME" "$APP_NAME" video-site-91; do
    [[ -n "$name" ]] || continue
    if append_unique "$name" "${names[@]}" >/dev/null; then
      names+=("$name")
    fi
  done

  for name in "${names[@]}"; do
    if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -Fxq "$name"; then
      log "removing docker container $name"
      docker rm -f "$name" >/dev/null 2>&1 || true
    fi
  done
}

pids_listening_on_port() {
  local port="$1"
  [[ "$port" =~ ^[0-9]+$ ]] || return 0
  command -v ss >/dev/null 2>&1 || return 0

  ss -ltnp 2>/dev/null \
    | awk -v port="$port" '$4 ~ ":" port "$" {print}' \
    | grep -oE 'pid=[0-9]+' \
    | cut -d= -f2 \
    | sort -u || true
}

process_looks_like_app() {
  local pid="$1"
  local exe="" cmd=""
  exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
  cmd="$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null || true)"

  [[ "$exe" == "$INSTALL_PATH/server" ]] && return 0
  [[ "$cmd" == *"$INSTALL_PATH"* ]] && return 0
  [[ "$cmd" == *"VIDEO_FRONTEND_DIR=$INSTALL_PATH/dist"* ]] && return 0
  [[ "$cmd" == *"VIDEO_CONFIG=$INSTALL_PATH/config.yaml"* ]] && return 0
  [[ "$cmd" == *"video-site-91"* ]] && return 0
  [[ "$cmd" == *"91VideoSpider"* ]] && return 0
  return 1
}

stop_lingering_app_processes() {
  local ports=("$@")
  local port pid pids=()

  for port in "${ports[@]}"; do
    [[ "$port" =~ ^[0-9]+$ ]] || continue
    while IFS= read -r pid; do
      [[ -n "$pid" ]] || continue
      process_looks_like_app "$pid" || continue
      if append_unique "$pid" "${pids[@]}" >/dev/null; then
        pids+=("$pid")
      fi
    done < <(pids_listening_on_port "$port")
  done

  if (( ${#pids[@]} == 0 )); then
    return
  fi

  warn "stopping lingering app process(es): ${pids[*]}"
  kill "${pids[@]}" 2>/dev/null || true
  sleep 1

  local alive=()
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      alive+=("$pid")
    fi
  done
  if (( ${#alive[@]} > 0 )); then
    warn "force killing lingering app process(es): ${alive[*]}"
    kill -9 "${alive[@]}" 2>/dev/null || true
  fi
}

warn_remaining_listeners() {
  local ports=("$@")
  local port pid cmd
  for port in "${ports[@]}"; do
    [[ "$port" =~ ^[0-9]+$ ]] || continue
    while IFS= read -r pid; do
      [[ -n "$pid" ]] || continue
      cmd="$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null || true)"
      warn "port $port is still listening after uninstall: pid=$pid ${cmd:-unknown}"
    done < <(pids_listening_on_port "$port")
  done
}

has_interactive_tty() {
  [[ -t 0 ]]
}

confirm_uninstall_app() {
  if ! has_interactive_tty; then
    return 0
  fi

  local confirm=""
  printf '确认卸载 91 吗？这会停止服务、移除管理命令，并可选择是否删除项目文件。[y/N]: ' >/dev/tty
  IFS= read -r confirm </dev/tty || confirm=""
  case "$confirm" in
    [yY]) return 0 ;;
    *)
      log "uninstall cancelled"
      return 1
      ;;
  esac
}

delete_install_path_requested() {
  if [[ "${UNINSTALL_DELETE_FILES:-0}" == "1" ]]; then
    return 0
  fi
  if ! has_interactive_tty; then
    return 1
  fi

  local confirm=""
  printf '删除 %s 里的程序、配置和数据吗？[y/N]: ' "$INSTALL_PATH" >/dev/tty
  IFS= read -r confirm </dev/tty || confirm=""
  case "$confirm" in
    [yY]) return 0 ;;
    *) return 1 ;;
  esac
}

service_health_url() {
  printf 'http://127.0.0.1:%s/admin/api/setup' "$(listen_port_from_config)"
}

wait_for_service_ready() {
  local url deadline
  url="$(service_health_url)"
  deadline=$((SECONDS + SERVICE_READY_TIMEOUT))
  log "waiting for service at $url"
  while (( SECONDS < deadline )); do
    if curl -fsS --connect-timeout 2 --max-time 5 "$url" >/dev/null 2>&1; then
      log "service is ready"
      return 0
    fi
    sleep 2
  done
  return 1
}

restart_service_ready() {
  if systemctl restart "${SERVICE_NAME}.service" && wait_for_service_ready; then
    return 0
  fi

  warn "service did not become ready; retrying restart"
  if systemctl restart "${SERVICE_NAME}.service" && wait_for_service_ready; then
    return 0
  fi

  warn "service failed to become ready"
  systemctl --no-pager --full status "${SERVICE_NAME}.service" || true
  journalctl -u "${SERVICE_NAME}.service" -n 80 --no-pager || true
  return 1
}

fetch_and_unpack() {
  local tmp archive url root
  tmp="$(mktemp -d)"
  archive="$tmp/$(asset_name)"
  url="$(download_base_url)/$(asset_name)"
  log "downloading $url"
  if ! download_file "$url" "$archive"; then
    warn "download failed: $url"
    rm -rf "$tmp"
    return 1
  fi

  if ! tar -xzf "$archive" -C "$tmp"; then
    warn "extract failed"
    rm -rf "$tmp"
    return 1
  fi
  root="$tmp/${APP_NAME}-linux-${ARCH}"
  if [[ ! -f "$root/server" || ! -d "$root/dist" || ! -f "$root/config.example.yaml" ]]; then
    warn "release package layout is invalid"
    rm -rf "$tmp"
    return 1
  fi

  mkdir -p "$INSTALL_PATH"
  cp "$root/server" "$INSTALL_PATH/server"
  rm -rf "$INSTALL_PATH/dist"
  cp -R "$root/dist" "$INSTALL_PATH/dist"
  cp "$root/config.example.yaml" "$INSTALL_PATH/config.example.yaml"
  if [[ -d "$root/91VideoSpider" ]]; then
    rm -rf "$INSTALL_PATH/91VideoSpider"
    cp -R "$root/91VideoSpider" "$INSTALL_PATH/91VideoSpider"
  fi
  chmod +x "$INSTALL_PATH/server"
  rm -rf "$tmp"
}

installed_version() {
  if [[ -f "$VERSION_FILE" ]]; then
    head -n1 "$VERSION_FILE" 2>/dev/null | tr -d '\r'
  fi
}

target_version() {
  if [[ "$VERSION" != "latest" ]]; then
    printf '%s' "$VERSION"
    return
  fi

  local body version effective_url
  body="$(curl -fsSL \
    -H "Accept: application/vnd.github+json" \
    -H "User-Agent: video-site-91-installer" \
    "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null || true)"
  version="$(printf '%s\n' "$body" \
    | sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' \
    | head -n1)"
  if [[ -n "$version" ]]; then
    printf '%s' "$version"
    return
  fi

  effective_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$(download_base_url)/$(asset_name)" 2>/dev/null || true)"
  printf '%s\n' "$effective_url" \
    | sed -nE 's#.*/releases/download/([^/]+)/.*#\1#p' \
    | head -n1
}

should_skip_update() {
  [[ "$FORCE_UPDATE" != "1" ]] || return 1

  local current target
  current="$(installed_version)"
  target="$(target_version || true)"

  if [[ -z "$target" ]]; then
    warn "cannot determine target version; continuing update"
    return 1
  fi

  if [[ -z "$current" ]]; then
    log "installed version: unknown"
    log "target version: $target"
    return 1
  fi

  log "installed version: $current"
  log "target version: $target"
  [[ "$current" == "$target" ]]
}

record_version() {
  local version
  version="$(target_version || true)"
  [[ -n "$version" ]] || version="$VERSION"
  {
    echo "$version"
    date '+%Y-%m-%d %H:%M:%S'
  } >"$VERSION_FILE"
}

show_success() {
  local local_ip public_ip version
  local_ip="$(ip addr show 2>/dev/null | awk '/inet / && $2 !~ /^127/ {sub(/\/.*/, "", $2); print $2; exit}')"
  public_ip="$(curl -s4 --connect-timeout 5 ip.sb 2>/dev/null || true)"
  version="$(head -n1 "$VERSION_FILE" 2>/dev/null || echo unknown)"

  echo
  printf '%b安装完成%b\n' "$GREEN" "$RESET"
  echo "版本：$version"
  [[ -n "$local_ip" ]] && echo "局域网：http://${local_ip}:${FRONTEND_PORT}/"
  [[ -n "$public_ip" ]] && echo "公网：  http://${public_ip}:${FRONTEND_PORT}/"
  echo "后台：  http://服务器IP:${FRONTEND_PORT}/admin"
  echo "数据：  $INSTALL_PATH/data"
  echo
  echo "首次访问后台时会要求设置管理员用户名和密码。"
  echo "管理命令：91 或 91 status | logs | update | restart | stop"
}

install_app() {
  check_system
  check_disk_space
  install_deps
  systemctl stop "${SERVICE_NAME}.service" 2>/dev/null || true
  fetch_and_unpack || die "install failed"
  prepare_config
  write_service
  install_cli
  open_firewall_port
  restart_service_ready || die "service failed to start"
  record_version
  show_success
}

update_app() {
  check_system
  [[ -f "$INSTALL_PATH/server" ]] || die "not installed at $INSTALL_PATH"

  if self_update_manager; then
    log "re-running update with latest manager script"
    exec_latest_manager_update
  fi

  install_deps

  if should_skip_update; then
    log "already up to date; skipped app update"
    return 0
  fi

  check_disk_space

  local backup
  backup="$(mktemp -d)"
  backup_install_files "$backup"

  systemctl stop "${SERVICE_NAME}.service" 2>/dev/null || true
  if ! (fetch_and_unpack && prepare_config && write_service && install_cli); then
    warn "update failed; restoring previous files"
    restore_install_files "$backup"
    systemctl start "${SERVICE_NAME}.service" 2>/dev/null || true
    rm -rf "$backup"
    exit 1
  fi

  if ! restart_service_ready; then
    warn "new version failed to start; restoring previous files"
    restore_install_files "$backup"
    restart_service_ready 2>/dev/null || true
    rm -rf "$backup"
    exit 1
  fi
  record_version
  rm -rf "$backup"
  log "updated"
}

uninstall_app() {
  local listen_port port ports=()
  confirm_uninstall_app || return 1

  listen_port="$(listen_port_from_config)"
  for port in "$listen_port" "$FRONTEND_PORT" 9191 9192; do
    [[ "$port" =~ ^[0-9]+$ ]] || continue
    if append_unique "$port" "${ports[@]}" >/dev/null; then
      ports+=("$port")
    fi
  done

  stop_app_services
  remove_app_containers
  stop_lingering_app_processes "${ports[@]}"
  rm -f "$COMMAND_LINK" "$APP_COMMAND_LINK" "$MANAGER_PATH"

  if delete_install_path_requested; then
    rm -rf "$INSTALL_PATH"
    log "removed $INSTALL_PATH"
  else
    log "kept $INSTALL_PATH"
  fi

  warn_remaining_listeners "${ports[@]}"
}

show_menu() {
  if [[ ! -t 0 ]]; then
    usage
    return 0
  fi

  while true; do
    clear
    echo "欢迎使用 91 管理脚本"
    echo
    echo "基础功能："
    echo "1、查看状态"
    echo "2、查看日志"
    echo "3、更新 91"
    echo "4、重启 91"
    echo "5、停止 91"
    echo "6、卸载 91"
    echo "0、退出"
    echo
    read -r -p "请输入选项 [0-6]: " choice

    case "$choice" in
      1) main status ;;
      2) main logs ;;
      3) main update ;;
      4) main restart ;;
      5) main stop ;;
      6)
        if main uninstall; then
          exit 0
        fi
        ;;
      0) exit 0 ;;
      *) echo "无效的选项" ;;
    esac

    echo
    read -r -n1 -s -p "按任意键继续 ..."
  done
}

main() {
  local action="${1:-}"
  if [[ -z "$action" ]]; then
    if is_manager_invocation; then
      show_menu
      return
    fi
    action="install"
  fi

  case "$action" in
    install)
      need_root "$@"
      install_app
      ;;
    update)
      need_root "$@"
      update_app
      ;;
    restart)
      need_root "$@"
      restart_service_ready || die "service failed to start"
      ;;
    stop)
      need_root "$@"
      systemctl stop "${SERVICE_NAME}.service"
      ;;
    status)
      systemctl --no-pager --full status "${SERVICE_NAME}.service" || true
      ;;
    logs)
      journalctl -u "${SERVICE_NAME}.service" -f
      ;;
    menu)
      show_menu
      ;;
    uninstall)
      need_root "$@"
      uninstall_app
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
