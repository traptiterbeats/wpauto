#!/usr/bin/env bash
# wpauto installer: bash <(curl -Ls https://raw.githubusercontent.com/traptiterbeats/wpauto/main/install.sh)
set -euo pipefail

REPO="traptiterbeats/wpauto"
BIN="/usr/local/bin/wpauto"
UNIT="/etc/systemd/system/wpauto.service"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

[ "$(id -u)" -eq 0 ] || { red "Please run as root (sudo -i)."; exit 1; }
command -v systemctl >/dev/null || { red "systemd is required."; exit 1; }
command -v curl >/dev/null || { apt-get update -y && apt-get install -y curl; }

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) red "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

VERSION="${WPAUTO_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
ASSET="wpauto_linux_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $ASSET ..."
curl -fsSL "$BASE/$ASSET" -o "$TMP/$ASSET"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt"
( cd "$TMP" && grep " ${ASSET}\$" checksums.txt | sha256sum -c - >/dev/null ) || { red "Checksum verification failed."; exit 1; }
tar -xzf "$TMP/$ASSET" -C "$TMP"

systemctl stop wpauto 2>/dev/null || true
install -m 0755 "$TMP/wpauto" "$BIN"

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
  if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
    ufw allow "${PORT}/tcp" >/dev/null || true
  fi
fi

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
sleep 1
green "Installation finished. Run 'wpauto' to open the management menu."
"$BIN" info || true
