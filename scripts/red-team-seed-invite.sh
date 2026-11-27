#!/usr/bin/env sh
# Red-team gate for the seed/rotation address plane.
#
# The attacker can write DNS/mDNS TXT records, guess or replay nonces, choose
# headers and stand up a lying peer; it does not hold the seed announcement key.
# Every case must fail closed before the signed border is reached.
set -eu
cd "$(dirname "$0")/.."

go test ./internal/announce -v
go test ./cmd/seed-invite -run RedTeam -v
