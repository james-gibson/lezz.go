#!/bin/bash
#
# Install the seed-addressed invite plane as a supervised service. It resolves
# invites by isotope name and enforces signed rotational expectations before a
# call reaches the WAN ingress. The wan-ingress binary itself stays unaware of
# DNS, mDNS and seed semantics.
#
#   BINARY=./dist/seed-invite \
#   CONFIG=./configs/seed-invite.json \
#   ./scripts/install-seed-invite.sh
#
# CONFIG is deliberately required. Copy configs/seed-invite.example.json, fill
# in the operator-owned token, announcement key, seed and DNS/mDNS source, then
# install that file. A missing config cannot leave the plane permissive.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="${BINARY:-$HERE/../dist/seed-invite}"
LOGDIR="${LOGDIR:-$HOME/Library/Logs}"

[ -n "${CONFIG:-}" ] || { echo "CONFIG is required; copy configs/seed-invite.example.json and fill it in" >&2; exit 1; }
[ -f "$CONFIG" ] || { echo "config not found: $CONFIG" >&2; exit 1; }
[ -x "$BINARY" ] || { echo "binary not found or not executable: $BINARY" >&2; exit 1; }
BINARY="$(cd "$(dirname "$BINARY")" && pwd)/$(basename "$BINARY")"
CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
mkdir -p "$LOGDIR"

case "$(uname -s)" in
  Darwin)
    DEST="${HOME}/Library/LaunchAgents/co.james-gibson.lezz.seed-invite.plist"
    sed -e "s|__BINARY__|$BINARY|g" -e "s|__CONFIG__|$CONFIG|g" \
        -e "s|__LOGDIR__|$LOGDIR|g" "$HERE/seed-invite.plist" > "$DEST"
    launchctl unload "$DEST" 2>/dev/null || true
    launchctl load "$DEST"
    echo "installed $DEST (KeepAlive: crash restarts the invite plane)"
    ;;
  Linux)
    DEST="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/seed-invite.service"
    mkdir -p "$(dirname "$DEST")"
    sed -e "s|__BINARY__|$BINARY|g" -e "s|__CONFIG__|$CONFIG|g" \
        -e "s|__LOGDIR__|$LOGDIR|g" "$HERE/seed-invite.service" > "$DEST"
    systemctl --user daemon-reload
    systemctl --user enable --now seed-invite.service
    echo "installed $DEST (Restart=always: crash restarts the invite plane)"
    ;;
  *)
    echo "unsupported host: $(uname -s); install the unit manually from $HERE" >&2
    exit 1
    ;;
esac
