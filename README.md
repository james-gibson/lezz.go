# lezz.go — Lab Orchestrator

The orchestration layer for the prototype lab. Installs and manages the other lab tools, handles self-updating binaries, and provides `lezz demo` — a subcommand that spins up a complete local cluster for experimentation.

---

## Commands

```sh
# Demo cluster
lezz demo                              # start a self-contained cluster

# Tool lifecycle
lezz install <tool>                    # download and install a managed tool
lezz update                            # self-update lezz to the latest release
lezz version                           # print tool versions and daemon status

# Process control
lezz run <tool> [args...]              # exec into the tool (lezz disappears from the process tree)
lezz start <tool> [args...]            # spawn the tool as a child process and wait

# LaunchAgent management (macOS)
lezz service install <tool> [profile] # install a launchd plist and load it
lezz service remove <tool> [profile]  # unload and remove the plist
lezz service list                      # show all managed LaunchAgents and their status
lezz service purge                     # stop and remove all managed services

# Full reset
lezz purge                             # stop services, remove binaries, clear Go caches
```

---

## Managed Tools

| Tool | GitHub | Notes |
|---|---|---|
| `lezz` | james-gibson/lezz.go | self-managing |
| `adhd` | james-gibson/adhd | dashboard / MCP skill runner |
| `ocd-smoke-alarm` | james-gibson/smoke-alarm | health monitoring |
| `tuner` | james-gibson/tuner | |

Install any tool from its latest GitHub release:

```sh
lezz install adhd
lezz install ocd-smoke-alarm
lezz install tuner
```

Binaries land in `~/.lezz/bin/`. If that directory isn't on your `PATH`, `lezz install` will print the one-liner to add it.

---

## Daemon Profiles

Each tool can be run as a macOS LaunchAgent under the `co.james-gibson.lab.*` namespace. Profiles describe the intended role.

| Tool | Profile | Args | Description |
|---|---|---|---|
| `lezz` | `demo` | `demo` | self-contained demo cluster |
| `adhd` | `idle` | `--headless` | headless MCP server, no smoke-alarm connected |
| `ocd-smoke-alarm` | `idle` | _(none)_ | running, no targets configured |
| `tuner` | `idle` | _(none)_ | running, no configuration |

```sh
# Install adhd in headless mode as a LaunchAgent
lezz service install adhd idle

# Check status
lezz service list
# LABEL                                   STATUS   PLIST
# co.james-gibson.lab.adhd.idle           running  ~/Library/LaunchAgents/...

# Logs land in ~/.lezz/logs/co.james-gibson.lab.<tool>.<profile>.{log,err}
```

---

## lezz demo

`lezz demo` starts a self-contained cluster without requiring any pre-existing configuration:

- Two `ocd-smoke-alarm` instances, each watching the other
- One `adhd` instance in headless mode, registered as an isotope with the primary smoke-alarm
- A fixed-port discovery registry at `:19100/cluster`
- mDNS advertisement of `_lezz-demo._tcp`

On startup it prints a cluster summary including isotope trust levels from `/isotope/list`.

Connect the dashboard to a running demo cluster:

```sh
adhd --demo
# or, using the stable config written by lezz demo:
adhd --config ~/.lezz/demo-adhd.yaml
```

Inspect cluster state:

```sh
curl http://localhost:8088/status | jq .
curl http://localhost:8088/isotope/list | jq .
curl http://localhost:19100/cluster | jq .
```

---

## lezz update

`lezz update` checks GitHub for a newer release and atomically replaces the running binary.

```sh
lezz v0.1.25 — checking for updates...
new version available: v0.1.26 → applying...
updated to v0.1.26 — restart lezz to use the new version
```

**Pseudo-version and dirty-build detection:** dev builds (`v0.1.25-0.20260404234801-2a30d6a85e23`) and dirty local builds (`+dirty`) are treated as stale regardless of the version number — `lezz update` will always apply the latest release when run from such a build. Clean tagged releases (`v1.2.3`, `v1.2.3-alpha.1`) are compared normally.

---

## lezz run vs lezz start

`lezz run <tool>` uses `syscall.Exec` — lezz replaces itself with the tool, so lezz does not appear as the parent process. Use this when you want a clean process tree.

`lezz start <tool>` spawns the tool as a child and waits for it to exit. lezz stays alive as the parent. Useful when scripting clusters or when you need the exit code.

---

## lezz purge

Full reset — removes everything lezz owns:

1. Stops and unloads all managed LaunchAgents
2. Deletes managed binaries from `~/.lezz/bin/`
3. Clears Go build-cache entries for managed module packages
4. Removes module download cache entries for managed modules

```sh
lezz purge
# removing 2 service(s)...
# removing managed binaries...
# clearing Go build cache for managed modules...
# clearing Go module cache for managed modules...
# done — run `lezz install <tool>` to reinstall
```

---

## ADHD MCP Tools

When `adhd` is running in headless mode it exposes an MCP server. Tool list as of current registry:

| Tool | Description |
|---|---|
| `adhd.status` | Dashboard light count summary by status |
| `adhd.lights.list` | All lights with current status |
| `adhd.lights.get` | Single light by name |
| `adhd.isotope.status` | This isotope's topology role |
| `adhd.isotope.peers` | Discovered peer ADHD isotopes |
| `adhd.isotope.instance` | This instance's public isotope identifier |
| `adhd.rung.respond` | Compute a rung validation receipt for a challenge |
| `adhd.rung.verify` | Verify a receipt against this instance's secret |
| `adhd.rung.challenge` | Issue a two-step rung validation challenge to a peer |

### Rung validation

Each ADHD instance generates a random secret at startup and derives a public isotope identifier from it. External clients can challenge the instance to prove it has a real, running implementation:

```sh
# Get this instance's public identifier
curl -s -XPOST http://localhost:9001/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.isotope.instance"}' | jq .

# Issue a challenge to a peer
curl -s -XPOST http://localhost:9001/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.rung.challenge","params":{
    "target_url": "http://localhost:9002",
    "feature_id": "adhd/lights-status",
    "nonce": "fresh-random-nonce"
  }}' | jq .
# → { "verified": true, "receipt": "...", "peer_isotope": "..." }
```

The receipt is `base64url(SHA256(secret + ":" + feature_id + ":" + nonce))`. Without knowing the instance secret, forging a valid receipt requires ~2^256 guesses. Each nonce binds the receipt to a specific challenge, preventing replay.

---

## Directory Structure

```
lezz.go/
├── cmd/lezz/              — CLI entry point and command handlers
├── internal/
│   ├── service/           — LaunchAgent install / unload / list (macOS launchd)
│   ├── demo/              — Demo cluster bring-up and cluster summary
│   ├── tools/             — Binary download, version checking, registry, purge
│   └── selfupdate/        — lezz self-update logic (pseudo-version detection)
└── features/              — Gherkin acceptance tests
```

---

## In the Lab

lezz.go is the host tool that brings up and manages all other lab participants. See [lab-safety](https://github.com/james-gibson/lab-safety) for a full map of how all tools connect.

Peer tools:
- **ocd-smoke-alarm** — health monitoring, managed as a LaunchAgent by lezz
- **adhd** — dashboard and MCP skill runner, started in headless mode by `lezz demo`
- **isotope** — shared library used by lezz demo to query trust status from `/isotope/list`
- **lab-safety** — pre-flight and teardown validation (planned integration)

---

## See Also

- [lab-safety — full ecosystem overview](https://github.com/james-gibson/lab-safety)
- [ocd-smoke-alarm](https://github.com/james-gibson/smoke-alarm)
- [adhd dashboard](https://github.com/james-gibson/adhd)
- [isotope protocol library](https://github.com/james-gibson/isotope)
