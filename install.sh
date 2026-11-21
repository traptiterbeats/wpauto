#!/usr/bin/env bash
# wpauto installer: bash <(curl -Ls https://raw.githubusercontent.com/traptiterbeats/wpauto/main/install.sh)
#   latest stable release (default)
#   ... install.sh --beta          newest release, pre-releases (betas) included
#   ... install.sh v0.15.0-beta.1  one exact release (same as WPAUTO_VERSION=v0.15.0-beta.1)
set -euo pipefail

REPO="traptiterbeats/wpauto"
BIN="/usr/local/bin/wpauto"
UNIT="/etc/systemd/system/wpauto.service"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

VERSION="${WPAUTO_VERSION:-latest}"
while [ $# -gt 0 ]; do
  case "$1" in
    --beta|beta) VERSION="beta" ;;
    --stable|stable) VERSION="latest" ;;
    --version) shift; VERSION="${1:-latest}" ;;
    v[0-9]*) VERSION="$1" ;;
    -h|--help) echo "usage: install.sh [--beta | --stable | vX.Y.Z[-pre]]"; exit 0 ;;
    *) printf 'unknown option: %s\n' "$1"; exit 1 ;;
  esac
  shift
done

[ "$(id -u)" -eq 0 ] || { red "Please run as root (sudo -i)."; exit 1; }
command -v systemctl >/dev/null || { red "systemd is required."; exit 1; }
command -v curl >/dev/null || { apt-get update -y && apt-get install -y curl; }

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) red "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

STEPS=6
T0=$SECONDS
step() { printf '\n\033[1;36m[%s/%s] %s\033[0m\n' "$1" "$STEPS" "$2"; }
ok()   { printf '\033[32m  ✔ %s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
# --speed-limit/--speed-time: abort (and retry) if the download stalls below 1 KB/s for 30 s,
# instead of hanging silently.
CURL=(curl -fL --connect-timeout 15 --retry 3 --retry-delay 3 --speed-limit 1024 --speed-time 30)

step 1 "Checking this server"
info "architecture: $ARCH · $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")"
if [ -x "$BIN" ]; then info "currently installed: $("$BIN" version 2>/dev/null || echo unknown)"; else info "currently installed: none (fresh install)"; fi

step 2 "Finding the release"
if [ "$VERSION" = "beta" ]; then
  # GitHub lists releases newest first; drafts are not visible without a token
  VERSION=$("${CURL[@]}" -s -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/releases?per_page=1" 2>/dev/null | grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
  [ -n "$VERSION" ] || { red "Could not find the newest release on GitHub."; exit 1; }
  BASE="https://github.com/$REPO/releases/download/$VERSION"
  ok "newest release (beta channel): $VERSION"
  TAG="$VERSION"
elif [ "$VERSION" = "latest" ]; then
  TAG=$("${CURL[@]}" -s -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
  BASE="https://github.com/$REPO/releases/latest/download"
  ok "latest release: ${TAG:-latest}"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
  ok "release: $VERSION"
  TAG="$VERSION"
fi
ASSET="wpauto_linux_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

step 3 "Downloading $ASSET"
S=$SECONDS
if ! "${CURL[@]}" --progress-bar "$BASE/$ASSET" -o "$TMP/$ASSET"; then
  red "Download failed or stalled (no data for 30 s, 3 retries). Check the server's connection to github.com and run the installer again."
  exit 1
fi
SIZE=$(du -k "$TMP/$ASSET" | cut -f1)
ok "downloaded $(awk "BEGIN{printf \"%.1f MB\", $SIZE/1024}") in $((SECONDS-S)) s"

step 4 "Verifying checksum"
"${CURL[@]}" -s "$BASE/checksums.txt" -o "$TMP/checksums.txt" || { red "Could not download checksums.txt."; exit 1; }
( cd "$TMP" && grep " ${ASSET}\$" checksums.txt | sha256sum -c - >/dev/null ) || { red "Checksum verification failed."; exit 1; }
ok "sha256 matches"
tar -xzf "$TMP/$ASSET" -C "$TMP"
ok "extracted: $(cd "$TMP" && ls -1 | grep -v -e "$ASSET" -e checksums.txt | tr '\n' ' ')"

step 5 "Installing the program"
systemctl stop wpauto 2>/dev/null || true
install -m 0755 "$TMP/wpauto" "$BIN"
ok "installed $BIN ($("$BIN" version 2>/dev/null || echo "${TAG:-}"))"

if [ ! -f /etc/wpauto/config.json ]; then
  DEFAULT_PORT=$(( (RANDOM % 40000) + 20000 ))
  PORT=""
  while true; do
    read -r -p "Panel port [${DEFAULT_PORT}]: " PORT </dev/tty || true
    PORT="${PORT:-$DEFAULT_PORT}"
    if [[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ]; then
      if ss -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${PORT}\$"; then
        red "Port $PORT is in use, choose another."
      else
        break
      fi
    else
      red "Invalid port."
    fi
  done
  "$BIN" init --port "$PORT"
  ok "created /etc/wpauto/config.json (panel port $PORT)"
  if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
    ufw allow "${PORT}/tcp" >/dev/null || true
    ok "opened port $PORT in ufw"
  fi
fi

step 6 "Starting the panel service"
cat > "$UNIT" <<'EOF'
[Unit]
Description=wpauto panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/wpauto serve
Restart=on-failure
RestartSec=3
User=root
StateDirectory=wpauto

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now wpauto >/dev/null 2>&1
ok "wrote $UNIT"
sleep 1
if systemctl is-active --quiet wpauto; then ok "service wpauto is running"; else red "service wpauto did not start: journalctl -u wpauto -n 50"; fi
echo
green "Installation finished in $((SECONDS-T0)) s."
"$BIN" info || true
echo
info "Opening the management menu (run 'wpauto' anytime to open it again) ..."
sleep 2
if [ -e /dev/tty ]; then "$BIN" </dev/tty >/dev/tty 2>&1 || true; fi
