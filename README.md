# lezz.go — Lab Orchestrator

The orchestration layer for the prototype lab. Installs and manages the other lab tools, handles self-updating binaries, and provides `lezz demo` — a subcommand that spins up a complete local cluster for experimentation.

---

## Trust after connection

**A caller can connect to an MCP service; whether it is trusted after connecting is
a separate question, and one most transport specifications do not answer.** This lab
measures it. A caller carries a **42i distance** — the extent to which it has
departed from **nominal** (the service level a call promises) against **linear time**
(the shared tick) — and the distance maps to a **rung** that limits what the caller
may read. A caller that keeps departing is removed from the routeable set.

```
 nominal ●────────────  the level a call promises
          ╲
           ╲ distance     +8 / +16 / +32 …
            ●───────────  where it landed (linear time →)
```

One measure, regardless of source — a stale credential, a clock skew, a tampered
receipt, a honeypot contact. Model: `kb/scripts/lab/PLANE.md`. Figures:
`kb/scripts/lab/MATH.md`. Full page with references: `kb/scripts/lab/SPLASH.md`.

## Commands

```sh
# Demo cluster
lezz demo                              # start a self-contained cluster (foreground)
lezz demo start|stop|status|restart    # detached lifecycle; stable ports
lezz auto                              # start a base cluster — empty and awaiting, auto-installs tools, reuses an existing registry

# Tool lifecycle
lezz install <tool>                    # download and install a managed tool
lezz tools upgrade [tool...]           # upgrade managed tools to their latest releases
lezz update                            # update lezz and all managed tools
lezz version                           # print tool versions and daemon status

# Process control
lezz run <tool> [args...]              # exec into the tool (lezz disappears from the process tree)
lezz start <tool> [args...]            # spawn the tool as a child process and wait

# LaunchAgent management (macOS)
lezz service install <tool> [profile] # install a launchd plist and load it
lezz service remove <tool> [profile]  # unload and remove the plist
lezz service list                      # show all managed LaunchAgents and their status
lezz service purge                     # stop and remove all managed services

# macOS application bundle (TCC permissions / passkey identity)
lezz app install [--dock]              # build and sign ~/Applications/Lezz.app
lezz app open [-- args...]             # launch via LaunchServices (default: auto)
lezz app info                          # show bundle identity and signature
lezz app uninstall [--force]           # remove Lezz.app

# lezz.sea dashboard (signed dev release)
lezz sea                               # launch it, installing it if missing
lezz sea --check                       # show the installed build and whether it verifies
lezz sea --install                     # install or update without launching

# GitHub device-flow auth (quick path; token in keychain)
lezz auth github                       # sign in with a GitHub OAuth App
lezz auth status                       # verify the stored GitHub identity
lezz auth logout                       # remove the token and metadata

# Editor wiring
lezz agent setup [editor]              # wire the workspace agent into Zed's settings
lezz agent remove [editor]             # remove it (requires --yes)
lezz agent show [editor]               # show what is wired

# Full reset
lezz purge                             # stop services, remove binaries, clear Go caches
```

`lezz auth github` uses the OAuth **device flow**: the client ID is public and is
embedded at build time (`-X main.buildGitHubClientID=<id>`), and no client secret
is used. See [docs/github-auth.md](docs/github-auth.md) for creating the OAuth
App and the storage/security model.

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

Installed tools are **not** left to rot: `lezz tools upgrade` compares each binary's
`-v` output with its latest GitHub release and reinstalls it when it is older.
`lezz update` does the same for every managed tool after self-updating lezz.

```sh
lezz tools upgrade                      # every managed tool except lezz
lezz tools upgrade ocd-smoke-alarm      # one tool
```

---

## Daemon Profiles

Each tool can be run as a macOS LaunchAgent under the `co.james-gibson.lab.*` namespace. Profiles describe the intended role.

| Tool | Profile | Args | Description |
|---|---|---|---|
| `lezz` | `demo` | `demo` | self-contained demo cluster |
| `adhd` | `idle` | `--headless --mcp-addr 127.0.0.1:9090` | headless MCP server on loopback `127.0.0.1:9090`, no smoke-alarm connected |
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

A profile that names an MCP address is **probed before its plist is written**.
`adhd` logs a failed bind as a warning and keeps running, so a service installed
over a taken port would be green in `lezz service list` while exposing no MCP
server; the install therefore refuses, and names the owner and the fix:

```sh
lezz service install adhd idle
# service install failed: refusing to install a service whose MCP port is in use:
#   127.0.0.1:9090 is held by PID 1202 (adhd); stop it or choose another --mcp-addr,
#   or pass --force to install anyway

lezz service install --force adhd idle   # deliberate shared-port override
```

The probe is fail-closed only on a proven conflict: when the address cannot be
probed (not local to this host, unparseable, no permission), the install goes
ahead with a warning on stderr. A port held by the currently installed instance
of the same service is expected — the install reloads it.

---

## lezz demo

`lezz demo` starts a self-contained cluster without requiring any pre-existing configuration:

- Two smoke-alarm links running **in-process** (`github.com/james-gibson/smoke-alarm/pkg/smokealarm`), each watching the other — no generated alarm YAML and no `ocd-smoke-alarm` subprocess
- One `adhd` instance in headless mode, registered as an isotope with the primary smoke-alarm
- A fixed-port discovery registry at `:19100/cluster`
- mDNS advertisement of `_lezz-demo._tcp`

The links honour `health.self_check`, `tuner.advertise`, `alerts.sinks.log` and
per-link known-state baselines (`state-a` / `state-b`), and they shut down when
lezz exits. `adhd` still runs as a subprocess: it is a TUI/MCP server with its
own lifecycle, and the facade is smoke-alarm only.

On startup it prints a cluster summary including isotope trust levels from `/isotope/list`.

The installed `adhd` is **probed before it is started**: a build that does not
accept the demo's command line is reported at once, with adhd's own message and
the upgrade that fixes it, instead of surfacing as a readiness timeout later. A
build that predates the advertise-guard override (`--force`, adhd#15) is started
without it and says so — that release has no guard to override.

A child that dies during startup fails the demo immediately rather than after
the timeout, and the error carries the tail of its log. The full log is copied
to `~/.lezz/logs/demo-<service>.log` before the demo's temp root is removed, so
the cause outlives the process that produced it.

### Lifecycle

`lezz demo` runs in the foreground. To leave the cluster running:

```sh
lezz demo start                     # detached; returns once the cluster is ready
lezz demo status                    # pid, endpoints, and a live probe of each
lezz demo stop                      # SIGTERM, waits for the process to exit
lezz demo restart                   # stop + start, same ports when free
```

`start` writes `~/.lezz/demo-state.json` (pid, endpoints, log directory) and sends
the child's output to `~/.lezz/logs/demo.log`. `status` and `stop` read that file,
so they also reach a foreground `lezz demo` started in another terminal. The state
file is removed when the cluster exits; a state file whose process is gone is
cleaned up on the next `status`.

Ports are stable across restarts: the last run's ports are saved in
`~/.lezz/demo-ports.json` and reused when they are still free. Pin them when the
endpoints must be known up front:

```sh
lezz demo start --alarm-a-port 52100 --alarm-b-port 52101 --adhd-port 52102
```

A pinned port that is already in use fails the start instead of quietly moving the
endpoint.

Connect the dashboard to a running demo cluster:

```sh
adhd --demo
# or, using the stable config written by lezz demo:
adhd --config ~/.lezz/demo-adhd.yaml
```

Inspect cluster state:

```sh
lezz demo status                     # endpoints, pid, and probe results
curl http://localhost:19100/cluster | jq .
# alarm ports come from the status output:
curl http://localhost:<alarm-a-port>/status | jq .
```

---

## lezz auto

`lezz auto` launches a base cluster in auto mode — empty and awaiting, designed
to be populated at runtime instead of hardcoding the config at launch:

- Installs any missing managed tools (`adhd`, `ocd-smoke-alarm`) on demand
- Reuses a live discovery registry instead of starting a competing second cluster
- Two `ocd-smoke-alarm` instances with **no hardcoded targets**: they start empty
  and are populated by the server's auto-refresh (discovery) and remote-update
  (`remote_agent`) paths
- The instances **chain themselves into a federation mesh**: the first to bind
  `base_port` becomes the introducer and each later instance claims the next
  free port and introduces itself — no manual peering (this is what sets `auto`
  apart from `demo`, which wires instances by hand)
- One `adhd` instance in headless mode started with `--demo`, so it builds its
  `smoke_alarm` endpoints from the discovery registry rather than a config file
- A fixed-port discovery registry at `:19100/cluster` and mDNS advertisement

```sh
lezz auto
# connect a dashboard:
adhd --demo
```

Because the base cluster carries no targets until they appear, a second
`lezz auto` run on the same host reuses the running registry, and further
`ocd-smoke-alarm` instances started with the same federation range chain on as
followers automatically.

**Why `auto` does not embed its links.** `pkg/smokealarm` exposes the monitor,
health, alerts and known-state subsystems but not federation — an embedded
instance reports `federation.enabled` as unsupported. Since federation *is* how
`auto` chains its links, adopting the facade here would start two links that
never join the mesh. `auto` therefore keeps spawning
`ocd-smoke-alarm serve -config …`; `demo` (hand-peered) is the path that runs
in-process. Revisit when the facade exposes federation.

---

## lezz sea

Launch the [lezz.sea](https://github.com/james-gibson/lezz.sea) dashboard. When
the app is already installed, this is just `open -a`: no network, no update.

```sh
lezz sea                 # launch; install from the dev release if missing
lezz sea --install       # install or update, do not launch
lezz sea --check         # what is installed, and does it verify
lezz sea --force         # reinstall over a verified copy
```

The dashboard is published as a signed macOS bundle on the `dev` prerelease of
`james-gibson/lezz.sea`. An install goes through every check below, in this
order, and nothing is unpacked or installed until the previous one passes:

1. **HTTPS to an allowlist.** `github.com` and GitHub's asset hosts only, and
   every redirect is checked too — a redirect cannot move the download to a new
   origin or downgrade it to HTTP.
2. **SHA-256 against the published manifest** (`dev.json`). This rejects a
   truncated or substituted body before it is ever read as an archive.
3. **Apple's signature on the unpacked bundle**: `codesign --verify --deep
   --strict`, authority `Developer ID Application: James Gibson (288ZTJGXX9)`,
   team `288ZTJGXX9`, hardened runtime, secure timestamp. The signature — not
   the checksum — is the trust anchor, because the checksum comes from the same
   origin as the archive while the signature comes from Apple's chain.
4. **Notarization**: a stapled ticket plus a Gatekeeper assessment of
   `accepted / Notarized Developer ID`.

A signed build with no notarization ticket is refused, because macOS would block
its first launch anyway. `--allow-unnotarized` installs one deliberately (it
warns); it exists for the window before a notarized build is published and
should not be scripted against.

Installing is a staged copy plus a rename into `~/Applications`, never an
in-place overwrite, and it refuses to replace a running copy. The loopback
dashboard shows exactly the cluster it is told about — `lezz sea` only decides
which bundle runs.

## lezz agent

`lezz agent setup` writes the workspace agent into an editor's settings so the
virtual tree is reachable from the Agent Panel. Zed is supported; its settings
file is `~/.config/zed/settings.json` (or `$XDG_CONFIG_HOME/zed/settings.json`).

```sh
lezz agent setup zed --command /path/to/workspace-agent
lezz agent build --src ~/projects/personal/lab-safety
lezz agent show zed
lezz agent remove zed --yes
```

The command is resolved in this order: `--command`, `$LEZZ_WORKSPACE_AGENT`,
`~/.lezz/bin/workspace-agent`, then `workspace-agent` on `PATH`. If none resolve,
`lezz agent setup` fetches and builds the agent into `~/.lezz/bin` (unless
`--no-build`). `lezz agent build` does the same explicitly:

```sh
lezz agent build [--src DIR] [--out PATH] [--no-fetch]
```

It looks for a local `lab-safety` checkout first (`$LEZZ_WORKSPACE_AGENT_SRC`,
the current directory and its parents, then common `~/projects/...` locations).
If none exists, it clones `https://github.com/james-gibson/lab-safety.git` into
`~/.lezz/src/lab-safety` and builds `lab/agent`. The clone path requires
`lab/agent` to be committed and pushed; a local checkout works immediately.

This is the interim path. Long term `workspace-agent` should be a registered
managed tool with release assets, so `lezz install workspace-agent` and
`lezz auto` can install it like adhd and ocd-smoke-alarm.

The settings file is JWCC (JSON with comments and trailing commas), so the edit
is a **splice**: only `agent_servers.<name>` is written, and every other byte —
key order, comments, unrelated agents — is left alone. The result is re-parsed
and compared against the original before the write, and a
`settings.json.bak-YYYYMMDD-HHMMSS` copy is made first. `--dry-run` prints the
entry without touching the file. Zed detects the change without a restart.

---

## lezz update

`lezz update` updates lezz itself **and every managed tool**. lezz is checked and
atomically replaced from its own releases; each tool (`adhd`, `ocd-smoke-alarm`,
`tuner`) is asked for its version with `-v`, compared with its latest GitHub
release, and reinstalled only when it is older.

```sh
lezz v0.5.1 — checking for updates...
new lezz version available: v0.5.2 → applying...
updated lezz to v0.5.2 — restart lezz to use the new version

managed tools:
  adhd             0.8.6 → 0.11.1
  ocd-smoke-alarm  up to date (0.11.0)
  tuner            up to date (0.1.0)
```

A tool whose version cannot be determined (a source build that reports only a
commit hash) is treated as stale and replaced with the released build, the same
rule lezz applies to itself. `lezz tools upgrade [tool...]` runs only that half —
useful when a released tool fix is the thing you are waiting for, e.g.

```sh
lezz tools upgrade ocd-smoke-alarm
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
│   ├── appbundle/         — macOS .app bundle, codesign, LaunchServices launch
│   ├── credential/        — Keychain / 0600-file secret store seam
│   ├── githubauth/        — GitHub device flow, token metadata
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
