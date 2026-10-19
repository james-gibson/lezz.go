# Surfaces: anonymous, debug, trusted

Two unauthenticated surfaces, one authorization gate.

```
anonymous (network / loopback)     lezz anon — empty MCP server, no tools
        │
        │  local only, below the empty proxy
        ▼
debug (Unix socket, 0600)          gate.explain, gate.shadow, wrappers,
                                   connection/deny logs — observe, never invoke
        │
        ▼
trusted (entitlement)              real adhd tools + wrappers, per-effect mandates
```

## Anonymous: empty by default, honeypot by pre-hydration

`lezz anon` starts as a valid MCP server with **no tool registry**:

```
initialize   → protocolVersion, capabilities.tools (empty), serverInfo "anonymous"
ping         → {}
tools/list   → {"tools": []}
tools/call   → -32601 "method not found"   (name not echoed)
other        → -32601
```

There is no backend, no `adhd.proxy`, no cache/queue client, and no credential
in the process. This is stronger than a filtered adhd: a bug in a gate cannot
expose a tool that was never registered.

The response is deliberately generic: no `adhd` identity, no version, no tool
count. A caller learns only that an MCP server exists.

### Pre-hydration isotope

The isotope protocol already has the mechanism: `isotope.NewAgentStateAtRung`
pre-positions an agent at a rung (`Direction: "externally-verified"`, no failure
history), while `VerifyTrust` derives a rung over the network from the lezz
registry and mDNS. Same rung scale, two attestation paths:

| Source | How | What it is |
|---|---|---|
| boot | signed record provided to the binary before hydration | a provisioning credential for **this node's role** |
| network | `VerifyTrust` → registry + mDNS → 2/4/6 | a peer-endpoint check at registration |

A valid boot isotope can select a surface:

```json
{"node":"edge-01","role":"edge","surface":"honeypot","rung":4,
 "expires":"2026-10-20T00:00:00Z","nonce":"...","sig":"<provisioning HMAC>"}
```

- valid + `surface=honeypot` → the edge serves the **honeypot surface** (decoys,
  triggers, no real backend);
- valid + `surface=empty` → the empty proxy;
- missing/invalid/expired → **empty**, fail closed.

The rung is the **node's**, not the caller's. Pre-positioning the edge at rung 4
does not make an anonymous caller rung 4, and it does not admit anyone to the
real surface. It authorizes this node to *serve decoys*, nothing more.

**The verification is the whole point.** `NewAgentStateAtRung` accepts any
integer and labels it `externally-verified`; that is a label, not a proof. The
boot record must be signed by a key the binary does not also read from the same
file, scoped to `surface=honeypot`, bound to a node id and expiry. Otherwise
"provided at boot" is just a self-asserted rung — the same failure as
`certify_domain`. The provisioning key belongs in the keychain/0600 secret, not
in an env var that leaks through logs or `/proc`.

**"Without removing anything" is a surface statement, not a binary statement.**
Keeping the real code in the binary is fine; loading the real tool registry,
proxy method, or backend credentials in honeypot mode is not. The honeypot
surface must be decoy-only and have no route to the real cluster. If the same
process can reach the real tools, a bug in the surface selection is a pivot.

### The two anonymous postures

| Surface | What the caller sees | Detection |
|---|---|---|
| **empty** (default) | `tools/list: []`, generic errors | connection / denial telemetry |
| **honeypot** (pre-trusted boot isotope) | decoy tools, fake results | `honeypot_triggered` on invocation |

Silence and decoys are separate postures, selected by the boot isotope at
startup — not by a request header and not by a flag on the empty proxy.

An empty tool list removes the honeypot/decoy knock surface. In the empty
posture, detection moves to connection and denial telemetry:

- `anonymous_connect` (who, when)
- `anonymous_tools_list` (how often)
- `anonymous_tool_call_denied` (attempted name, logged **locally only**)
- `anonymous_method_denied`

If the lab wants decoy tools as a knock surface, that is the **honeypot
posture** above, with a deliberate fake tool list. It is a different surface,
selected at boot, not a flag on the empty proxy.

## Debug: a lease, not a flag

The local `--debug-surface` flag is **enablement**, not authorization. It only
says the binary is allowed to open the socket. The socket opens only with a
cluster-issued lease:

```json
{"seed_id":"...","actions":["observe","explain","shadow"],
 "not_before":"...","not_after":"...","tick":12345,"sig":"<cluster signature>"}
```

- flag alone → no socket;
- lease expired or not renewed → socket closed (fail closed);
- `seed_id` binds the lease to the project, so project A's lease cannot open
  project B's debug surface;
- `tick` makes the lease fresh (replay window), not authorized;
- `actions` never include `invoke` or `pay`.

A shared-secret derivation such as `HMAC(key, seed_id || tick)` is a **bearer
capability**: whoever holds the key can mint the next window. If the edge holds
the key, the edge can mint its own debug permission. The practical shape is a
cluster-signed lease the edge only **verifies**. The tick then bounds replay,
and the seed id bounds scope; the signature is the authority.

### Reload is not activation — implemented in `lezz anon`

Smoke-alarm's SIGHUP reload swaps its **target set and engine knobs**
(`internal/engine/engine.go:282`); its listeners are deliberately left alone
(`cmd/ocd-smoke-alarm/main.go:446`). That is still true, and it is the wrong
process to own a debug socket.

`lezz anon` owns the debug Unix socket and has the manager:

```
on SIGHUP:
    load lease + .sig
    ssh-keygen -Y verify against allowed_signers
    check seed_id and not_after
    valid   -> activate or renew the debug socket
    invalid -> deactivate (fail closed, even if a previous lease was valid)
expiry timer -> deactivate, independent of SIGHUP
```

SIGHUP is the trigger; the signed lease is the permission; the expiry timer is
the enforcement. "Keep the current configuration" is not used for the debug
socket.

## Lease keys

`lezz anon --lease <file>` needs a trust root:

| Source | Command | Notes |
|---|---|---|
| public GitHub SSH keys | `lezz anon --signer github:<user>` or `lezz debug fetch-keys --user <user>` | fetched from `https://github.com/<user>.keys`, pinned on first use (TOFU); `--refresh` updates |
| local secure default | `lezz debug keygen` | Ed25519 key at `~/.lezz/keys/debug_ed25519` (0600) + `debug.allowed`; identity `local` |
| explicit file | `lezz anon --allowed-signers <file> --identity <id>` | operator-managed allowed_signers |

Leases are SSH signatures (`ssh-keygen -Y sign -n lezz-debug`). The private key
never leaves the operator; the edge only verifies. The lease is bound to
`seed_id`, limited to `observe|explain|shadow`, and expires.

```
lezz debug keygen
lezz debug lease --seed <seed-id> --ttl 5m --out lease.json
lezz anon --lease lease.json --seed <seed-id>
# SIGHUP re-reads the lease; expiry closes the socket
```

The lease file is 0600; the signature is `lease.json.sig`. GitHub keys are
public, so the pin is a trust-on-first-use convenience, not an identity proof;
pin the file and refresh deliberately. Never put the private key or a token in
`seed.json` or the lease.

## Debug: below the empty proxy

Local Unix socket (`~/.lezz/run/adhd-debug.sock`, `0600`), selected by transport
— never by a request header, or an anonymous caller could set it.

The debug track is authorized to **observe**, not to invoke:

- `gate.explain(caller, tool)` — decision, reason, matched entitlement, facade answer
- `tools.list_as(anonymous)` — exactly `[]`, pinned
- `gate.shadow` — run the future trusted policy against live traffic, enforce
  only the current policy, log the delta
- `pr.get` / `pr.status` wrapper access for testing the sidecars
- `x402.quote` — fetch the 402 terms and evaluate the mandate **without paying**
- `hurl.explain` — egress allow-list decision for the robots.txt case

No state mutation, no `adhd.gh.run`, no real `adhd.proxy`. If the debug surface
can invoke the real tools, it is a backdoor, not a debug track.

## Trusted: the only authorization

The real adhd surface plus the wrappers, gated by entitlement and per-effect
mandates. The anonymous and debug surfaces never become trusted by being local,
by having a rung, or by a successful HURL test.

## Capability examples

| | Anonymous | Debug | Trusted |
|---|---|---|---|
| `robots.txt` GET (static, one request, pinned host) | denied — no tools | explains the egress decision, replays it | allowed via a read capability |
| x402 RPC (dynamic expense) | denied — no tools | `quote` + mandate decision, no payment | pays within the mandate, receipt |
| `pr.get` | denied — no tools | allowed (wrapper test) | allowed via entitlement |

## Track selection

| Track | Transport | Advertised? | Registered? |
|---|---|---|---|
| anonymous | loopback/LAN listener | yes (the public endpoint) | maybe as a decoy name, never as adhd |
| debug | Unix socket, 0600 | no | no |
| trusted | separate listener / entitlement | to trusted peers only | as the real adhd isotope |

## Status

- Implemented: `lezz anon` (empty), `lezz mcp` (read-only wrapper),
  `adhd.pr` (trusted adhd tool), `lezz cache`/`lezz queue`.
- Designed, not implemented: the debug UDS (`gate.explain`, `gate.shadow`),
  the trusted entitlement gate (#27), and per-effect mandates (x402).
- `lezz anon` is not yet wired into `lezz auto`; it is a separate process so
  the public and real listeners cannot be confused.
