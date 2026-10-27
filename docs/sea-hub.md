# sea: one adhd session hub, with child clusters as prime-plus proxies

**Status:** preparation for [#66](https://github.com/james-gibson/lezz.go/issues/66). No code yet.
**Basis:** lezz.go `4bd091e` (#65), adhd `c483fb3`, lezz.sea `8be7973` (#12), read on 2026-10-26.
**Companion:** james-gibson/lezz.sea#12 (the app side), james-gibson/adhd#49 (the self-join
that doubles lights).

This document answers the three questions the issue leaves open before the hub becomes the
only path, records the two places where the proposal as written does not match the code, and
sequences the work so each phase is independently testable. The acceptance criteria are in
`features/lezz/sea-hub.feature`.

## What #65 left in place

`lezz sea` starts lezz.sea as a supervised child over a private stdin/stdout pipe. The app
asks for isotopes by writing a request; `internal/sea` writes the derived config under
`~/.lezz/sea/Clusters/<slug>/` and builds the argv itself (`supervisor.go:249` `start`,
`supervisor.go:317` `startADHD`). There is no session-level adhd, so the app's Lights panel
falls back to the `127.0.0.1:9090` guess (`LezzSea/AppModel.swift:119`) and a per-cluster
endpoint from `cluster.json`.

## 1. Does a prime answer `adhd.lights.list` / `adhd.status` for its prime-plus children?

**Partly — and not through the prime-plus path.** Three separate mechanisms are in play, and
only one of them adds a child light to the parent.

| mechanism | what crosses | where |
|---|---|---|
| `--prime-plus --prime-addr` | MCP **traffic logs** only | `adhd/internal/headless/message_queue.go:87` → `smoke-alarm.isotope.push-logs` |
| the hub receiving those logs | a line in the hub's log file, not a light | `adhd/internal/headless/server.go:634` `handleIncomingPushLogs` |
| `adhd.cluster.join` with `alarm_a`/`alarm_b` | a watched alarm endpoint, hence a light | `adhd/internal/headless/server.go:370` |

- `handleLightsList` iterates the hub's own cluster (`adhd/internal/mcpserver/server.go:1231`),
  and the only join-time addition to that cluster is `smokeWatcher.WatchEndpoint(name+"/alarm-a")`
  (`headless/server.go:373`). So after a join the hub shows the child's **alarm** lights under
  a cluster prefix, and `adhd.status` counts them. The child's gherkin/feature lights,
  `mcp-direct` lights and its own instance light stay on the child.
- `handleClusterJoin` also stores `adhd_mcp` as a cluster peer
  (`mcpserver/server.go:1542`), which is what `adhd.isotope.peers` and path negotiation use.
- adhd accepts `smoke-alarm.isotope.push-logs` on its own MCP surface
  (`mcpserver/server.go:471`), so a hub can be the `--prime-addr`. The flag help calls it
  "address of prime smoke-alarm"; it works because the method name overlaps, and it needs the
  full MCP URL (`http://127.0.0.1:<port>/mcp`), not a host:port.

**Conclusion.** The hub is the right place for the Lights panel, and a stopped child does
disappear from it (the watcher stops reporting) while the app stays connected — that part of
the acceptance holds. The **Scenarios row must keep the child endpoint**, or reach the child
through the hub's existing `adhd.proxy` (`mcpserver/server.go:2909`, `target_endpoint` +
`call`), to read the child's own `adhd.lights.list`. `adhd.proxy` already is the "parent query
route" the issue offers to add; no new adhd route is needed for it.

## 2. Two corrections to the proposal

**a) Light updates do not flow through prime-plus.** Only logs do (§1). The issue's item 3
says "so light updates and logs flow to the parent" — the light path is the join, and it
carries only the alarm endpoints a cluster names.

**b) An "empty" hub cannot also show an app/channel light.** Lights enter an adhd instance only
through the smoke watcher (`headless/server.go:111`, `:349`), and `adhd.lights.*` are read-only
(`mcpserver/server.go:447`–`456`). There is no inbound light-update tool. So "one empty adhd
with no `smoke_alarm` entries" and "give lezz.sea/channel a light on the hub" cannot both hold
as written. The two ways out:

1. **The hub carries exactly one `smoke_alarm` entry** pointing at a channel-status endpoint
   that lezz serves on loopback (it already knows app version, build, connected state, last
   refusal and last UI action — every one of those arrives on the channel). The light then
   appears as `smoke:<source>/<target>` with no adhd change.
2. adhd grows a light-ingest write tool. That is a new write path on a control surface, and it
   is a larger change than the issue implies.

Recommendation: (1). It costs one config stanza and a ~40-line loopback handler on the side
that already owns the facts.

**c) Naming.** `--prime-addr` reads as a smoke-alarm address and is fed to an adhd. Keeping the
flag but documenting what it accepts is the low-risk option; renaming is a public-flag change
and belongs in its own PR.

## 3. The comment: announce an mDNS isotope; collapse by seed then isotope

**Today the hub is undiscoverable.** adhd registers and advertises only when it has a smoke
target: `resolveSmokeAlarm` (`adhd/cmd/adhd/main.go:174`) gates `RegisterAsIsotope`
(`headless/server.go:705`), and the mDNS record is published inside it (`headless/server.go:735`).
No `smoke_alarm` entries means no registry entry and no `_adhd-isotope._tcp` record. A child
spawned outside `lezz sea` therefore has nothing to announce to and nothing to look for;
`AutoDiscoverPrime` (`headless/isotope_discovery.go:149`) reads a smoke-alarm isotope list,
which an empty hub does not have.

What "announce an isotope" requires, and its limit:

- adhd needs to advertise **without** a registry (a flag or config key). The advertise guard
  (`headless/advertise_guard.go`) still applies: it permits a loopback bind and refuses a
  non-loopback one without `--force`. A loopback-bound hub's record names an address that only
  means anything on this host, so this buys **same-host** discovery — which is every child
  `lezz sea` starts — and not LAN discovery. LAN children need the hub gated
  (`kb/scripts/cluster/shield_gate.py`), which is a separate decision.
- The TXT record is `v=1`, `role=`, `trust_rung=` (`isotope/mdns.go:25`). There is no seed and
  no isotope identity in it, so "collapse by seed then isotope" is not answerable from the
  advertisement today. It needs `seed=` (the project route, or the seed id when no route is
  declared — the same precedence adhd already uses for registration,
  `headless/project_identity.go:37`) and `isotope=` (the full identity `instanceLabel` truncates
  to eight characters, `headless/server.go:682`).
- The app keys Bonjour results by `service.name` and discards TXT
  (`LezzSea/Services/BonjourDiscovery.swift:50`), and the sidebar lists every cluster flat
  (`LezzSea/Views/ClusterSidebar.swift:15`). With a hub plus N children, #48's per-instance
  labels turn into N+1 anonymous rows.

**The collapse rule is already written down, and it is not "group by name."** `kb/scripts/lab/ISOTOPE-COLLAPSE.md`
establishes that a name is the hot path and an endpoint is shared by design; the inventory keys
on route id and claimant, keeps the replica set visible, and never collapses a shielded route.
Applied here: group by **seed** (the session/project scope), then by **isotope** (the instance
identity), and keep each instance addressable underneath. An advertisement with no seed stays
ungrouped and named — fail closed, not merged. Two instances that share an endpoint are never
one row. `adhd#49` is the same defect seen from the other side: a self-join doubles lights
because the watcher keys on name rather than route.

## 4. Stop order

`cleanup()` iterates the children map (`supervisor.go:480`) — unordered — and `stop(msg)`
works per cluster. With a hub, the order is a property of the topology, not of a map:
**app first** (it is the reason the lab exists), then every child alarm and child adhd, then
**the hub last**. Both exit paths in `Run` need it (ctx cancel at `supervisor.go:173`, app exit
at `:179`). It should be an explicit ordered list, not map iteration, so a test can assert it.

## 5. Ports

Hub: `9090` when free, otherwise the next free port. Child adhd: `freePort(9190, 9289)`
(`supervisor.go:338`). `freePort` listens-then-closes, so there is a window in which two
children (or the hub and a child) can be handed the same port, and there is no allocation
ledger. One allocator in the supervisor, with the hub's preference expressed as "9090 first,
then the shared range", removes the race.

## Decisions proposed

| # | decision | why |
|---|---|---|
| D1 | The hub is a supervisor child, started before the app, announced in a `hub` message. | Additive: the current app drops lines whose id it is not waiting for (`LabChannel.swift:225`), so no protocol bump and no version skew. |
| D2 | The hub endpoint is authoritative when the channel is active; a Finder launch keeps the typed/default endpoint. | The app stops guessing 9090 without losing its standalone mode. |
| D3 | lezz calls `adhd.cluster.join` on the hub for each child, reusing `clusterJoinParams` (`internal/demo/cluster.go:251`) with `alarm_a` = the cluster's first member and `adhd_mcp` = the child endpoint. | The payload and the handler already agree; no new wire contract. |
| D4 | Children start `adhd --headless --config … --prime-plus --prime-addr http://127.0.0.1:<hub>/mcp`. | Log aggregation on the existing method; light path is D3. |
| D5 | **Decided.** The hub carries one `smoke_alarm` entry: lezz's loopback channel status. | The only way to get a channel light without an adhd write path (§2b). It retires "empty": the hub is the session's adhd plus the channel source, and nothing else. |
| D6 | The hub binds `127.0.0.1` only, never `--force`, never advertised beyond loopback. | `adhd.proxy` has no target allowlist (`internal/proxy/proxy.go:56`); a reachable hub is a confused deputy. |
| D7 | A `stop` request never stops the hub; only app exit or Ctrl-C does, last. | The hub outlives any one cluster. |
| D8 | Seed→isotope grouping ships after the hub, not with it. | It needs an adhd TXT change and an app parse; keeping it separate keeps the hub reviewable. |

## Work, in order

**Phase 1 — hub lifecycle (lezz.go).**
`internal/sea/hub.go`: build the hub config (loopback addr, one channel entry per D5), start it,
write `hub.pid` under the lab root, return its endpoint. `supervisor.go`: start the hub before
the app, send `hub` once the app's `hello` arrives, add the hub to the ordered cleanup. Tests:
hub starts before the app; the endpoint is announced; the config is under the lab root and
loopback; 9090 is preferred and a free alternative is used when taken.

**Phase 2 — children as prime-plus (lezz.go).**
`startADHD`: argv gains `--prime-plus --prime-addr`; after the child answers, `adhd.cluster.join`
is sent to the hub (best-effort, as `sendJoin` already is) with `alarm_a` = the cluster's first
member and `adhd_mcp` = the child endpoint; `ADHD` gains `HubEndpoint`. Tests: the argv shape;
the join payload; a failed join does not fail the start; the reply carries both endpoints.

**Phase 3 — the channel light (lezz.go).**
A loopback `/status` on the supervisor's side: connected, app version and build, last refused
request, last UI action. The hub config points at it. Test: a fake channel produces the
expected status document, and the hub config names it.

**Phase 4 — the app (lezz.sea).**
`LabChannel` decodes `hub`; `AppModel.defaultEndpointText` takes the hub when active;
`ScenarioLab` stores `{hub, child}`; the Lights panel reads the hub; the Scenarios row reads the
child through `adhd.proxy`; the app/channel light is shown with the cluster lights.

**Phase 5 — discovery and collapse (adhd + lezz.sea).**
adhd advertises without a registry and adds `seed=` / `isotope=` to the TXT record;
`BonjourDiscovery` parses TXT; the sidebar groups seed → isotope and leaves an ungrouped
advertisement named.

## Risks and non-goals

- **SSRF at the hub.** `adhd.proxy` forwards to any `target_endpoint` the caller names and has
  no allowlist. Loopback binding is the whole of its protection (D6). Do not add a hub route
  that takes a caller-supplied URL from the app.
- **Protocol drift.** The `hub` message must stay additive. Changing reply semantics or bumping
  `v` makes the currently published app time out on every request (`LabChannel.send`, 60s).
- **Non-goals.** LAN-wide hub discovery; removing per-cluster ports (they stay as the Scenarios
  fallback); an adhd route-identity query (`ISOTOPE-COLLAPSE` marks that `declared`); and
  deduplicating the self-join on the hub (`adhd#49`, its own fix).

## Questions for the owner

D5 is settled: the hub watches a loopback status endpoint that lezz serves, and gains no adhd
write path. Still open:

1. **Phase 5 scope**: is same-host mDNS enough for now, or should the hub be gated for LAN
   discovery?
2. **Collapse surface**: the sidebar only, or also the Lights panel's per-cluster grouping
   (which is where the doubling in `adhd#49` is most visible)?
