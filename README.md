# lezz.go — Lab Orchestrator

The orchestration layer for the prototype lab. Installs and manages the other lab tools as macOS LaunchAgents, handles self-updating binaries, and provides `lezz demo` — a subcommand that spins up a complete local cluster for experimentation.

---

## Commands

```sh
# Manage background services
lezz service list          # show all managed daemons and their status
lezz service purge         # stop and unload all managed services

# Demo cluster
lezz demo                  # start a self-contained cluster

# Binaries
lezz version               # show installed tool versions alongside daemon status
lezz update                # self-update all managed binaries
```

---

## lezz demo

`lezz demo` starts a self-contained cluster without requiring any pre-existing configuration:

- Two `ocd-smoke-alarm` instances, each watching the other
- One `adhd` instance in headless mode, registered as an isotope with the primary smoke-alarm
- A fixed-port discovery registry at `:19100/cluster`
- mDNS advertisement of `_lezz-demo._tcp`

On startup it prints a cluster summary including the isotope trust levels reported by `/isotope/list`.

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

## Managed Services

lezz manages services under the `co.james-gibson.lab.*` LaunchAgent namespace. Each tool gets a plist installed in `~/Library/LaunchAgents/` and is started at login.

```sh
lezz service list
# co.james-gibson.lab.smoke-alarm-a   running  pid=12345
# co.james-gibson.lab.smoke-alarm-b   running  pid=12346
# co.james-gibson.lab.adhd            running  pid=12347
```

---

## Directory Structure

```
lezz.go/
├── cmd/lezz/              — CLI entry point
├── internal/
│   ├── service/           — LaunchAgent install/unload/list
│   ├── demo/              — Demo cluster bring-up and summary
│   ├── tools/             — Binary download and version checking
│   └── selfupdate/        — lezz self-update logic
└── features/              — Gherkin acceptance tests
```

---

## In the Lab

lezz.go is the host tool that brings up and manages all other lab participants. See [lab-safety](https://github.com/james-gibson/lab-safety) for a full map of how all tools connect.

Peer tools:
- **ocd-smoke-alarm** — health monitoring, managed as a LaunchAgent by lezz
- **adhd** — dashboard, started in headless mode by `lezz demo`
- **isotope** — shared library used by lezz demo to query trust status from `/isotope/list`
- **lab-safety** — pre-flight and teardown validation (planned integration)

---

## See Also

- [lab-safety — full ecosystem overview](https://github.com/james-gibson/lab-safety)
- [ocd-smoke-alarm](https://github.com/james-gibson/smoke-alarm)
- [adhd dashboard](https://github.com/james-gibson/adhd)
- [isotope protocol library](https://github.com/james-gibson/isotope)
