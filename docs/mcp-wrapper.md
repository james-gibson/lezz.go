# Read-only wrappers (debug/trusted)

These wrappers are **not** the anonymous surface. The anonymous surface is
`lezz anon` — an empty MCP server with no tools (`docs/surfaces.md`). The
wrappers sit on the debug/trusted tracks: the debug track uses them for testing,
the trusted track exposes them after entitlement.

The wrapper is still useful: a caller who reaches one cannot turn it into an
SSRF hop, run a command, or read an arbitrary file. But the first line of
defence is the empty proxy, not a filtered tool list.

## What makes a wrapper safe

- **No caller-supplied endpoint.** The upstream (cache/queue) is local config,
  not a request field. Contrast `adhd.proxy`'s `target_endpoint`.
- **No credentials in the request.** The sidecar owns the `gh` auth, the queue
  owns the process environment.
- **Read-only.** `pr.get` and `pr.status` cannot mutate anything.
- **Validated identifiers.** `repo` must be `owner/name`; `number` must be a
  positive integer; `kind` is `view|diff`. No path, shell, or URL injection.
- **Bounded.** 16 MiB body cap; one upstream; loopback bind.

## What is deliberately not in the wrapper

| Tool | Why not |
|---|---|
| `adhd.hurl.run` | writes a caller-supplied script and runs `hurl --test`; exec surface, can certify a domain |
| `adhd.gh.run` | runs `gh` with caller args; exec + credentials |
| `adhd.proxy` | caller-supplied endpoint + auth; SSRF |
| queue `enqueue` | write; the sidecar has no inbound auth |
| `workspace.package.edit` | write; the single-writer queue is behind the gate |

The existing HURL paths are **not removed**:

- `adhd.hurl.run` stays on adhd's real tool list (19 tools with `adhd.pr`).
- smoke-alarm's `internal/safety/hurl.go` stays the pre-protocol **target**
  gate: endpoint- or file-based HURL tests run before MCP/ACP handshakes for
  monitored targets.
- Neither is a wrapper tool. `adhd.hurl.run` executes a caller script; the
  scanner is target-oriented and cannot gate a sidecar's CLI invocation without
  a different contract.

**Open gap:** `lezz queue` and `lezz cache` upstreams have no HURL preflight at
all. If the safety layer should apply there, the smallest honest shape is a
`preflight` hook in the sidecar config that invokes the existing HURL runner
(`hurl --test`) before a batch/fetch — not a second scanner, and not a fake
"target" for a CLI process.

## `lezz mcp`

```
lezz mcp --cache-url http://127.0.0.1:PORT
```

This is a debug/trusted wrapper, not an anonymous endpoint. It is not reachable
from `lezz anon`.

Exposes exactly:

| Tool | Upstream | Notes |
|---|---|---|
| `pr.get` | `lezz cache /pr` | `repo`, `number`, `kind`, `refresh`; returns the body + `_meta.source` |
| `pr.status` | `lezz cache /status` | hits, misses, fetches, entries |

`initialize`, `tools/list`, `tools/call`, and `ping` only. No `adhd.*` tools, no
queue writes, no proxy.

## `adhd.pr`

The native adhd tool for the same cache. It is also read-only and validates the
same fields. The cache URL comes from `ADHD_PR_CACHE_URL`; unset means the tool
fails closed rather than falling back to spawning `gh`.

```json
{"jsonrpc":"2.0","id":1,"method":"adhd.pr",
 "params":{"repo":"james-gibson/local-time","number":139,"kind":"view"}}
```

Returns `{body, kind, source, stale, fetched_at, bytes}`. `source` is
`gh` (filled it), `cache` (fresh hit), or `stale` (served the last good copy).

Both adapters call the same cache; neither reimplements TTL, single-flight, or
SQLite. That is the rule from [queue-acp.md](queue-acp.md): one core, protocol
adapters.

## Multiple wrappers (debug/trusted)

One wrapper per capability, not one mega-server:

| Wrapper | Tools | Why separate |
|---|---|---|
| `pr` | `pr.get`, `pr.status` | read-only GitHub data |
| `queue` | `job.status` (read-only) | do not expose enqueue on the unauthed layer |
| `workspace` | `package.read` | writes stay behind the gate |

Separate processes give separate blast radii, separate ports, and separate
isotope registrations. `lezz mcp` currently serves the `pr` wrapper; the rest
can follow the same `Config`/`Start` shape.

## Cluster registration (next)

A wrapper should be announced like any lab participant:
`POST /isotope/register` on the smoke alarms (role `wrapper`, rung 2), and a
`projects` entry pointing at `http://127.0.0.1:PORT/mcp` so adhd mints
`adhd.project.pr.call`. Not wired yet; `lezz mcp` prints its URL and is
loopback-only.

## Measured (2026-10-19)

```
wrapper tools: ['pr.get', 'pr.status']
wrapper view first:  894.5ms  source=gh    bytes=5965
wrapper view again:    2.3ms  source=cache bytes=5965
adhd.pr diff first: 1107.2ms  source=gh    bytes=23766
adhd.pr diff again:    2.9ms  source=cache bytes=23766
adhd.pr bad repo: "repo must be owner/name"
```

## Limits

- No inbound auth. These tools are *safe*, not *authorized*; do not treat
  reaching one as a trust signal. #27's gate is still the authorization layer.
- The wrapper does not implement resources, prompts, or subscriptions — tools
  only.
- A wrapper can read whatever its sidecar can; `repo_allow` on the cache is the
  data boundary, not the MCP surface.
