# sea: one adhd session hub, with child clusters as prime-plus proxies

**Status:** preparation for [#66](https://github.com/james-gibson/lezz.go/issues/66). No code yet.
**Basis:** lezz.go `4bd091e` (#65), adhd `c483fb3`, lezz.sea `8be7973` (#12), read on 2026-10-26.
**Companion:** james-gibson/lezz.sea#12 (the app side), james-gibson/adhd#49 (the self-join
that doubles lights).

This document answers the questions the issue leaves open before the hub becomes the only
path, records where the proposal as written does not match the code, and sequences the work so
each phase is independently testable. The acceptance criteria are in
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

## 3. The comment: announce an mDNS isotope, and show work groups

The comment on #66 asks for an mDNS isotope announcement and for sea to "collapse what we see
… by seed then isotope". The announcement is right; the second half is a rotation problem, not a
taxonomy — read it as *show work groups*, with isotopes rotating through them.

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
  `lezz sea` starts. **Decided: same-host only, until federation is enabled** (see below).
- The TXT record is `v=1`, `role=`, `trust_rung=` (`isotope/mdns.go:25`): it carries neither a
  route nor an identity a member can be placed by. It needs at least the scope the member serves
  (adhd already resolves a project route from config, `LEZZ_PROJECT_ROUTE` or `seed.json`,
  `headless/project_identity.go:37`) and its isotope identity (the full id — `instanceLabel`
  truncates it to eight characters, `headless/server.go:682`). The rotation claim is a further
  field, and adhd has nothing to put in it yet (§ below).
- The app keys Bonjour results by `service.name` and discards TXT
  (`LezzSea/Services/BonjourDiscovery.swift:50`), and the sidebar lists every cluster flat
  (`LezzSea/Views/ClusterSidebar.swift:15`). With a hub plus N children, #48's per-instance
  labels turn into N+1 anonymous rows.

**What sea shows is a work group, and the reason is rotation.** A route is `RouteRef{Scope, Tool}`,
whose id is invariant across both replicas and rotation
(`smoke-alarm/internal/mcp_faults/rotation_verify.go:60`, `:67`). The cluster rotates every
`ClusterRotationPeriod = 5s` with a `DefaultGraceWindow = 1s`
(`internal/mcp_faults/tool_proxy.go:95`), and a rotation is announced as a claim —
`{node_id, rotated_at, grace, prefix, route_id}` (`rotation_verify.go:41`, `:291`) — which a
mirror verifies. The replicas of one route are a work group (`RouteRegistry` holds route id →
node ids, `:84`), and the group is what persists: its **members rotate through it**.

So the sea view is not a seed→isotope tree; it is work groups with rotating membership:

- **The row is the work group** — the route — not an advertisement, a cluster name, or an
  isotope id. adhd#48 made instance labels per process, so a name-keyed list shows one route as
  a new row on every rotation: churn, not inventory.
- **Members are isotopes at a rotation.** Show the current claimant and its rotation state (last
  claim, inside grace or expired); a silent member ages out of the group rather than deleting it.
- **Announcement is load-bearing, not directory service.** An unannounced rotation is a finding,
  not a test (`kb/scripts/lab/DECLARATION-CHAIN.md` § *Chaos announces its invariant*). A child
  spawned outside `lezz sea` that never announces has no work group to land in — which is what
  the comment on #66 is actually asking for.
- **Fail closed.** A member that cannot name its route stays a named, declared row, and two
  members that merely share an endpoint are never merged (`kb/scripts/lab/ISOTOPE-COLLAPSE.md`).
  `adhd#49` is the same defect from the other side: a self-join keys a light by name and shows
  one route twice.

### The name is the claim

The isotope belongs in the DNS-SD **instance name**, not in a TXT field bolted to a static name:
`<isotope>._adhd-isotope._tcp.local.` An announcement is then the rotation made visible — a new
name appears, the old one is withdrawn, and the sequence of names *is* the rotation.

Who is named in it matters, and two different ids are both called "isotope":

| id | form | identifies |
|---|---|---|
| spec isotope — **task id** | `family-vN:<hmac16>`, colon | what is being done |
| instance isotope — **member id** | 43-char base64url | which process is doing it |

So **the name carries the task, and the member** — a task can have several participants, and a
DNS-SD name must be unique per instance, so one alone cannot do the job. The colon is not legal
in a label (the alphabet is `^[A-Za-z0-9_-]+$`,
`internal/headless/instance_label_test.go:21`), so the encoding is
`<family>-v<N>-<hmac16>-<member8>`: 36 bytes for `sea-hub`, inside the 63-byte limit and the
label stays readable. The tick and the node's role are the rest of the claim; TXT carries
`role`, `trust_rung`, scope (workspace + route) and the current claimant.

The seed array is never on the wire: it is the alphabet both sides already hold, and publishing
it would buy agreement the peers already have while handing a stranger the ability to compute
the same silent variables.

The schedule behind that split, and why a fork is a rotation rather than a branch, is
`kb/scripts/lab/WORKSPACE-SCHEDULE.md`.

Constraints that decide the shape:

- **A DNS-SD instance label is 63 bytes.** The isotope identity is 43 characters of base64url,
  which is a legal label (`internal/headless/instance_label_test.go:21` pins the alphabet), so
  `adhd-<isotope>` fits at 48. Today `instanceLabel()` truncates to eight characters
  (`headless/server.go:659`, `:684`) — enough to be unique, not enough to be the claim.
- **The tick is public and already agreed.** `PublicTick` is `floor(unix / PublicTickSeconds)`
  with `PublicTickSeconds = 5`, and the comment on it is the design: *"every host with a synced
  clock agrees on it without a coordinator"* (`mcpserver/server.go:57-79`). That is the offset.
  Smoke-alarm's `ClusterRotationPeriod` is the same 5 s, with a 1 s grace.
- **Removal is not guaranteed.** adhd's own browser says `grandcat/zeroconf` delivers no removal
  notifications and a health failure is the departure signal (`internal/discovery/mdns.go:22-23`).
  So sea ages a member out by last announcement against the tick, never by `didRemove` — a
  crashed node never sends a goodbye.
- **A duplicate label is not an identity.** DNS-SD resolves a name collision by renaming
  (`… (2)`), so the app reads the isotope out of the label and rejects anything that does not
  parse as one, rather than treating the browser's key as the member.

What exists today: adhd advertises `adhd-<8 chars>`, once per process, with `v=1`, `role=`,
`trust_rung=` (`isotope/mdns.go:25`). It has a public tick but no rotation loop, and no route id
or prefix anywhere in `internal/` or `cmd/`. smoke-alarm computes both halves —
`ToolProxy.RouteID()`, `Announcement()` — and does not expose them on a target yet (the open item
in `ISOTOPE-COLLAPSE.md` § *Next*). So Phase 5 puts the identity in the name and the scope in the
TXT, and rotates the name when adhd grows a rotation; a second advertisement for one route
becomes a second claimant in the same work group either way.

### What silence costs

Two nodes holding the same array and agreeing on the tick derive the same prefix and epoch with
no message at all; the only thing they must ever exchange is the offset — which is what a
rotation claim is, and why grace is a notification-latency budget rather than a second clock
(`mcp_faults/tool_proxy.go:75-85`). The lab already splits the two: **the period is alphabet, the
counter is offset** — share `ClusterRotationPeriod`, allow the counter to differ
(`DECLARATION-CHAIN.md` § *Amendment: delay, rotation, and the price of hammering*).

Computability is symmetric, so silence buys agreement, not secrecy:

- **A derived value that does not bind the claimant is interchangeable between co-computing
  nodes.** That is `RouteKey{Address, Claimant, Epoch}` — the address alone is never a key. Same
  array, same tick, different claimant, different stamp.
- **Integrity comes from verification, not derivation.** `VerifyRotation` derives the route id
  itself and never takes the claim's word; no node is its own witness.
- **A work group exists without a registry, but is not auditable without the announcement.** Two
  nodes silently in phase and two nodes that have diverged look identical until one announces —
  which is why an unannounced rotation is a finding rather than a test. Silence is the
  optimisation; the claim is what makes the silence checkable.

### Workspace, work group, task, claim

Four levels, four lifetimes. The view needs all four because a level that outlives its members
cannot be keyed on them.

| level | lifetime | keyed on |
|---|---|---|
| workspace | durable | the tree (and the project route it serves) |
| work group | durable across tasks | the route — its id is invariant across replicas and rotation |
| work task | as long as the action | seed + action, which is what the isotope id already signs |
| claim | one tick | node + tick, announced as a rotation claim |

This is where the work group meets the workspace: a private group may mint a seed per task —
that is what a fork is — so a seed is a **task** key, never a grouping key. Grouping by seed
gives a task list; grouping by workspace gives the durable boundary, with work groups and their
tasks underneath and the rotating claims at the bottom. A workspace can hold two people on
different tasks, or one task with a person and an agent: **participants attach to the task**, so
presence is per task, not per workspace and not per node.

The boundary that makes the workspace real is inherited unix permissions on the tree of seeds
and isos, not a gateway. That protects the lineage; it does nothing for a call. So the two
boundaries stay separate: **files are private by mode, calls are priced by distance**, and a
workspace is never an authorization boundary — seeing a colleague's task does not grant calling
its route. The PII shield and the rung do that (`ISOTOPE-COLLAPSE.md` § *The PII shield*).

### Federation is the upstream, and the only LAN path

Same-host mDNS is sufficient until federation is enabled, because enabling it introduces an
upstream cluster: smoke-alarm's mesh elects an introducer on `base_port` (5100 by default,
`smoke-alarm/docs/features/catalog/federation.md:39`), followers claim the next slots, and
`GET /membership` (`internal/federation/server.go:306`) returns the whole registry. Membership
is itself the trust evidence — a same-cluster peer is registered as an isotope at rung 4 by
construction (`internal/federation/isotopes.go:15`, `:38`) rather than re-verified.

So the LAN path is "enrol the hub in the mesh's registry", not "bind the hub wider":

- **Today**: the hub advertises `_adhd-isotope._tcp` on loopback and is discoverable on this
  host. No `--force`, no gateway, no root.
- **When federation is enabled**: the hub registers its isotope with a mesh member's smoke-alarm
  and reaches peers through the registry the introducer aggregates. The hub's own MCP bind stays
  loopback; what crosses the LAN is the registration, not the control surface.
- **Never**: an unguarded non-loopback bind, a privileged helper, or a hub advertised by
  `--force`. Those are the alarm offense `headless/advertise_guard.go` exists to refuse, and none
  of them is needed for the app, the children, or the mesh.

Enrolling the hub in a federation is out of scope for #66; it lands with federation, and #66
only has to avoid foreclosing it — which means the hub's advertisement keeps carrying its role
and identity, and the hub's endpoint is announced to the app rather than assumed.

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
| D8 | Work-group grouping ships after the hub, not with it. | It needs an identity-bearing instance name, a scope in TXT, and an app parse; keeping it separate keeps the hub reviewable. |
| D10 | **Decided.** The DNS-SD instance name carries the task id *and* the member id, encoded `<family>-v<N>-<hmac16>-<member8>` because the label takes no colon and must be unique per instance; TXT carries role, rung, scope and the current claimant; the seed array never goes on the wire. | The name is the rotation made observable; the array is the alphabet both sides already hold. |
| D11 | **Decided.** The view groups by workspace → work group → task → claim, and never by seed. | A private group mints a seed per task, so a seed is a task key; grouping by it produces a task list, not a workspace. |
| D9 | **Decided.** The hub advertises same-host only until federation is enabled; the mesh's upstream registry is the LAN path. | Federation introduces an upstream cluster and its own membership trust; widening the hub's bind would add an exposure the mesh does not need. |

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

**Phase 5 — discovery and work groups (adhd + lezz.sea).**
adhd advertises without a registry and puts the task id and its member id in the instance name
(`<family>-v<N>-<hmac16>-<member8>`), with scope (project route) in TXT; `BonjourDiscovery` parses the name and TXT; the sidebar groups
workspace → work group → task → claim, ages a silent claim out by last announcement against the
tick, and leaves a route-less advertisement named. A repeated advertisement for one route
joins the existing group as another claimant. The advertisement is loopback-scoped (D9): it is
not the LAN path and must not be widened to become one. Rotating the name needs adhd to grow a
rotation loop, and carrying the claim needs smoke-alarm to expose `route.identity` — neither is
Phase 5.

**Phase 6 — enrolment in a federation (deferred).**
When federation lands, the hub registers with a mesh member's smoke-alarm and is discovered
through `GET /membership` instead of a wider advertisement. Not part of #66; recorded so Phase 5
does not close the door.

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
write path. D9 is settled: same-host mDNS until federation, which supplies the upstream registry
and stays in user land. Still open:

1. **Work-group surface**: the sidebar only, or the Lights panel too — which is where
   `adhd#49`'s doubling of one route is most visible, and where grouping would also need the
   light name to carry the route rather than the watcher's source name.
