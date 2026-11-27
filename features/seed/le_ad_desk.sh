#!/usr/bin/env sh
# Origin commands for features/seed/le-ad-desk.feature.
#
# The desk is the le.ad empty desk: a connection point that holds no content.
# This script is the behavioural origin; the feature is its Gherkin inversion.
set -eu

DESK_URL="${DESK_URL:-http://127.0.0.1:8080}"
ANNOUNCE_KEY="${ANNOUNCE_KEY:?set the seed announcement key}"
SEED="${SEED:-seed-42}"
ROTATION="${ROTATION:-rot-desk}"
NONCE="${NONCE:-nonce-desk}"
TICK_SECONDS="${TICK_SECONDS:-5}"
DNS_DOMAIN="${DNS_DOMAIN:-_rot.seed-42.zone.example.}"

# An unarmed desk is empty and holds no content.
curl -fsS "$DESK_URL/desk" | jq -e '.empty == true and .content == 0'

# A signed rotation occupies the lease, but adds no content.
seed-announce -key "$ANNOUNCE_KEY" -seed "$SEED" \
  -name wan-ingress -slot wan-ingress \
  -rotation "$ROTATION" -nonce "$NONCE" \
  -dns-domain "$DNS_DOMAIN" >/dev/null
curl -fsS "$DESK_URL/desk" | jq -e \
  --arg rotation "$ROTATION" \
  '.empty == false and .content == 0 and .rotation == $rotation'

# Two ticks is the lease. After it, the desk returns to empty.
sleep "$((2 * TICK_SECONDS + 1))"
curl -fsS "$DESK_URL/desk" | jq -e '.empty == true and .content == 0'
