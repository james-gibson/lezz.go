# PR read-through cache (`lezz cache`)

The read-side counterpart to the queue. Five agents asking for the same pull
request should cause **one** `gh` call, not five; repeated reads should not
touch GitHub at all.

## API

```
GET /pr?repo=owner/name&number=139&kind=view
GET /pr?repo=owner/name&number=139&kind=diff
GET /pr?repo=owner/name&number=139&kind=view&refresh=1
GET /status
GET /healthz
```

Headers on `/pr`:

| Header | Meaning |
|---|---|
| `X-Cache: gh` | fetched from GitHub for this request |
| `X-Cache: cache` | fresh row served from SQLite |
| `X-Cache: stale` | fetch failed; the last good copy is within `max_stale` |
| `X-Fetched-At` | RFC3339 timestamp of the cached body |
| `X-Stale: true` | present on the stale path |

`kind=view` returns the `gh pr view --json` document (metadata, files,
comments); `kind=diff` returns `gh pr diff` as `text/plain`.

## Semantics

- **TTL**: default 60 s. A fresh row is served without a subprocess or a
  network call.
- **Single-flight**: concurrent misses for the same `repo#number#kind` share one
  fetch; followers wait for the leader and get the same body.
- **Stale on error**: if the fetch fails and the last good copy is within
  `max_stale` (default 24 h), the cache serves it with `X-Stale: true` instead
  of failing the caller. This is the read-side equivalent of the queue's
  bounded wait.
- **Refresh**: `refresh=1` bypasses the TTL but still joins an in-flight fetch.
- **Allow-list**: `repo_allow` (empty = any) keeps private repositories out of
  a broadly configured cache.

## Storage

SQLite, same choice as the queue and the lab workspaces:

```sql
CREATE TABLE pr_cache (
  key        TEXT PRIMARY KEY,   -- repo#number#kind
  repo       TEXT NOT NULL,
  number     INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  body       BLOB NOT NULL,
  fetched_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
```

WAL, `synchronous=NORMAL`, one connection. A restart is a hit, not a refetch.

## Measured (2026-10-19, `james-gibson/local-time#139`)

| Request | First | Second |
|---|---|---|
| `kind=view` | 932.7 ms (`X-Cache: gh`) | **1.8 ms** (`X-Cache: cache`) |
| `kind=diff` | 1077.6 ms (`X-Cache: gh`) | **1.7 ms** (`X-Cache: cache`) |

`adhd.pr` through the same cache: diff 1107.2 ms (`gh`) → **2.9 ms**
(`cache`); the wrapper: view 894.5 ms → **2.3 ms**.

Five concurrent misses for another PR (157):

```
all 200, one body size, 0.67 s wall
fetches delta: 1
sqlite pr_cache rows: 3
```

## Limits

- TTL, not ETag: `gh` does not expose a conditional-request surface, so a PR
  edited inside the TTL can be up to 60 s stale. Use `refresh=1` for a forced
  read.
- `view` includes files and comments because the `--json` field list asks for
  them; very large PRs are capped by `max_body_bytes` (16 MiB default).
- Loopback only, no inbound auth. The cache can contain private PR data; do not
  expose it. `repo_allow` is an allow-list, not an auth layer.
- `view` and `diff` only. A separate comments/checks cache would be a new kind,
  not a new service.
- Two adapters call this cache: `adhd.pr` (native adhd tool) and the `lezz mcp`
  `pr.get`/`pr.status` wrapper. Both are read-only and validate the same
  identifiers; neither reimplements TTL or single-flight. See
  [mcp-wrapper.md](mcp-wrapper.md) and [surfaces.md](surfaces.md) for the layer rationale.

## Relation to the queue

| Side | Service | Store | Policy |
|---|---|---|---|
| write | `lezz queue` | file + SQLite mirror | single writer, batch, rate limit |
| read | `lezz cache` | SQLite | TTL, single-flight, stale-on-error |

Both are lezz-owned sidecars with the same failure philosophy: bounded,
inspectable, durable, and loopback-only.
