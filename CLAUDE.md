# CLAUDE.md

## What This Is

`lezz.go` is a self-updating host application for the adhd/ocd-smoke-alarm/tuner tool suite. It:

- Downloads and installs managed tools from their GitHub releases
- Keeps itself and its managed tools up to date in the background
- Configures systemd units or cron jobs on behalf of the user
- Manages runtime permissions with explicit prompting and manifest logging

Managed tools: **adhd**, **ocd-smoke-alarm**, **tuner** (all sibling Go binaries in `../`).

## Build & Test

```sh
go build ./cmd/lezz
go test ./...
go vet ./...
```

## Project Structure

```
cmd/lezz/          CLI entrypoint
internal/
  selfupdate/      go-selfupdate integration, version checking, atomic binary swap
  tools/           managed tool registry, install/run lifecycle
  daemon/          systemd unit and cron job generation
  permissions/     system user provisioning, permission manifest, revocation
features/lezz/     Gherkin requirements
```

## Sibling Binaries

lezz, adhd, ocd-smoke-alarm, and tuner are all sibling Go modules under `../`. They can interoperate during tests via subprocess spawning — same pattern as adhd's `tests/scenario/interop_test.go`.

## Key Constraint

lezz must not require root for a user-scoped install. Daemon configuration (systemd/cron) may require privilege escalation, but lezz prompts explicitly and documents everything it creates in a permissions manifest.
