# lezz-owned stdio skills (HTTP bridge)

lezz can own a stdio MCP server and expose it to the cluster as an ordinary
loopback HTTP endpoint. adhd never learns that a child process exists: it gets
a URL through the normal `adhd.cluster.join` project path and keeps using
`adhd.proxy` / `adhd.project.<name>.call` unchanged.

This is the safe form of "adhd supports stdio": the command lives in lezz's
static config, never in a request, so `target_endpoint` stays a URL.

## What lezz does

1. Starts one child per configured skill (`uvx`, `npx`, or a binary) with an
   explicit environment allow-list and its own process group.
2. Speaks newline-delimited MCP JSON-RPC to the child (the stdio transport in
   the spec, not LSP `Content-Length` framing), and initializes it once.
3. Serves `POST /mcp`, `GET /healthz`, `GET /status` on `127.0.0.1`.
4. Registers the skill with every base smoke-alarm (`POST /isotope/register`),
   so it appears on `/isotope/list` with the rung assigned by `VerifyTrust`
   (rung 2 / Harness Tools for a self-registered skill).
5. Publishes `{name, repo, mcp_url}` in `ClusterInfo.Projects`, which the
   existing `adhd.cluster.join` sends to adhd. adhd mints
   `adhd.project.<name>.call`.

The alarm's `targets` list is deliberately left empty, preserving the
`lezz auto` contract: the skill is announced through the isotope registry and
the project join, not hardcoded as an alarm target.

## Static config

Defaults are embedded in `internal/bridge/configs/*.yaml`. Operators can add or
replace definitions without rebuilding:

```
~/.lezz/lab/bridges/<name>.yaml
```

A file whose `name` matches an embedded definition replaces it; a new name is
appended.

```yaml
name: qdrant
repo: james-gibson/mcp-lab
command: uvx
args: ["mcp-server-qdrant"]
env:
  COLLECTION_NAME: ${QDRANT_COLLECTION:-lezz-bridge-proto}
  QDRANT_URL: ${QDRANT_URL}
  QDRANT_API_KEY: ${QDRANT_API_KEY}
listen: 127.0.0.1:0
allowed_tools: ["qdrant-find", "qdrant-store"]
request_timeout: 120s
```

`${VAR}` and `${VAR:-default}` are expanded when lezz starts the bridge.
`QDRANT_URL` / `QDRANT_API_KEY` are read from lezz's environment; they are
never written to a config file by lezz.

## Credentials resolved at spawn

`env:` is readable but not always the right source. A token that lezz already
holds — the `lezz auth github` device-flow token, for example — should not have
to be exported into the operator's shell just to reach a child. `secrets:` names
a credential instead of a value:

```yaml
name: lemon-extract
repo: gibsunas/lemon-extract
command: lemon-extract-mcp
secrets:
  LEMON_GITHUB_TOKEN: keychain:LEZZ_GITHUB_TOKEN
allowed_tools: ["lemon.config", "github.viewer", "github.repository.projects"]
request_timeout: 30s
```

The ref is resolved **at spawn**, immediately before `exec`, and the value goes
straight into the child's allow-listed environment. It is never placed in argv,
written to the YAML, logged, or exposed on `/status`.

Ref forms match `lezz seed init --auth-ref`:

| Ref | Source |
|---|---|
| `env:NAME` | the lezz process environment |
| `file:/abs/path` | a file that must be 0600 (0644 is refused, not warned about) |
| `keychain:SERVICE` | any account on a macOS generic-password item |
| `keychain:SERVICE/ACCOUNT` | one specific account |

Failure is fatal, and it is fatal **before** the child starts:

```
bridge "lemon-extract": resolve secret LEMON_GITHUB_TOKEN (keychain:LEZZ_GITHUB_TOKEN): credential not found
```

A skill that starts without its credential does not fail where the problem is;
it fails later as a 401 from a remote API, or worse, quietly degrades to an
anonymous request. Refusing to start is the only failure mode that points at the
credential.

Resolving a secret is not the same as authorizing one. lezz resolves what the
operator named in static config; it does not decide that a skill *should* hold a
credential. The tool allow-list is what bounds the child's reach, and the
credential's own scopes are what bounds what it can do with it.

## Security rules the bridge enforces

- **Command is config-only.** No request field can name or build a command.
- **Environment allow-list.** The child gets `PATH`, `HOME`, `TMPDIR`, locale,
  TLS and uv cache variables plus exactly the keys in `env` — not lezz's whole
  environment.
- **Secrets resolved at spawn.** `secrets:` turns a credential reference into a
  value immediately before `exec`; the value only ever exists in the child's
  environment. A ref that cannot be resolved stops the bridge.
- **Tool allow-list.** `allowed_tools` filters `tools/list` and refuses
  `tools/call` for anything else.
- **Process group.** The child is started with `Setpgid`; shutdown signals the
  group, so `uvx`/`npx` wrappers cannot leave the real server orphaned.
- **Loopback only.** The listener binds `127.0.0.1` by default. There is no
  inbound auth on the bridge yet; a unix socket or a token is the next step
  (adhd's `ProjectEntry` has no auth field today).

## Verified prototype

With Qdrant credentials in the environment:

```sh
lezz auto
```

Observed:

```
skill qdrant     http://127.0.0.1:50613/mcp  (uvx mcp-server-qdrant)
registered skill qdrant     with http://127.0.0.1:50610  rung 2 (Harness Tools)
registered skill qdrant     with http://127.0.0.1:50611  rung 2 (Harness Tools)
...
lezz-owned skills (stdio behind loopback HTTP):
  qdrant     http://127.0.0.1:50613/mcp  (uvx mcp-server-qdrant)
             adhd tool: adhd.project.qdrant.call
```

Then, against the cluster's adhd endpoint:

```sh
# tools/list through the project tool
{"jsonrpc":"2.0","id":1,"method":"adhd.project.qdrant.call",
 "params":{"method":"tools/list"}}
# => ["qdrant-find", "qdrant-store"]

# a real round trip
{"jsonrpc":"2.0","id":1,"method":"adhd.project.qdrant.call",
 "params":{"method":"tools/call","params":{
   "name":"qdrant-store","arguments":{"information":"marker"}}}}
```

`/isotope/list` on either alarm names the skill; `/healthz` reports the child,
not just the socket; SIGINT to `lezz auto` closes the bridge and leaves no
process-group survivors.

## Limits (deliberate)

- **Tools only.** Server-to-client requests are refused with `-32601` and
  notifications are dropped. The bridge advertises this in `/status` as
  `"scope":"tools-only"`.
- **One child, one session.** The child is initialized once; the HTTP side has
  no `Mcp-Session-Id` lifecycle. That matches adhd's `callPeerMCP`, which sends
  a single POST per call.
- **TCP loopback.** `unix:` listeners need a matching change in
  `adhd/internal/mcpserver.callPeerMCP` (custom `DialContext`).
- **The child is not monitored as a target.** Announcement is the isotope
  registry + project join. To get a light, add the bridge URL as an alarm
  target (and accept that it is no longer an "empty" base cluster), or fix the
  stdio prober framing first (`smoke-alarm#43`) if you want stdio monitoring.
