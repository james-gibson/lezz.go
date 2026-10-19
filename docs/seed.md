# seed.json and `lezz see`

A project-local identity and placement file. `lezz see` reads it, recomputes
the 42i start, resolves the external auth reference, and shows the cluster
surface. It does **not** join or create anything yet.

## The 42i start

`isotope.NewAgentState(id)` starts at **42 + 0i**, rung 0, direction
`seed-42-ground-truth`. An agent with no key starts there. Supplying that result
to the cluster is a **position handshake**, not authentication: anyone can
compute 42. The external key is what authorizes passage beyond the
anonymous/empty surface. `isotope.NewAgentStateAtRung` is the other path: an
externally verified rung supplied at boot (pre-hydration).

## Schema

```json
{
  "version": 1,
  "seed": {
    "id": "20261019T200001-dd876f90",
    "surface": "base",
    "start": { "real": 42, "imaginary": 0, "rung": 0 }
  },
  "project": {
    "name": "local-time",
    "repo": "james-gibson/local-time"
  },
  "auth": {
    "mode": "external",
    "ref": "env:LEZZ_CLUSTER_KEY",
    "subject": "external-id"
  }
}
```

- `seed.id` — stable project id. **Not** a GitHub identity, not derived from a
  repo or username. It namespaces the dynamic tool:
  `adhd.project.<id>.call`.
- `seed.surface` — the cluster surface to join or create (`base`, `demo`,
  `anonymous`, …). Separate from the id; see the challenge below.
- `seed.start` — the declared 42i start; `lezz see` recomputes and checks it.
- `project.repo` — provenance only. It is never used for auth.
- `auth.ref` — a **reference** (`env:NAME`, `file:/path`, `keychain:NAME`), not
  a secret. The CLI resolves presence; it never prints the value.
  `lezz auth github` stores a device-flow token as
  `keychain:LEZZ_GITHUB_TOKEN` (see [github-auth.md](github-auth.md)).
- `auth.subject` — an external id label. An id alone is not proof; it must be
  paired with the key/challenge at claim time.

`seed.id` is a **binding**, not a secret: it scopes claims and leases (so a
grant for this project cannot open another), but it is not the proof. A flag or
an id on its own is enablement/identification; the proof is `auth.ref` plus the
cluster's verification.

## Commands

```
lezz seed init [--surface base] [--auth-ref env:LEZZ_CLUSTER_KEY] [--name NAME] [--force]
lezz see [--file seed.json]
```

`seed init` writes `seed.json` atomically at 0644. It refuses to overwrite
without `--force`.

`see` currently:

1. loads and validates the seed;
2. computes `42 + 0i (seed-42, rung 0)`;
3. resolves the auth reference to present/absent/invalid (never the value);
4. reads the registry at `:19100/cluster` and prints a matching surface.

It does not claim, join, or create. Those need the decisions below.

## What join/create should do (proposed)

1. **Claim** `POST /seed/claim` with `{id, 42i, subject, proof}`.
   The surface verifies the proof, binds `seed.id` to the subject on first
   claim, and rejects a later claim from a different subject. The 42i result is
   a format/liveness check, not the proof.
2. **Place** the project on the requested surface. The surface policy, not the
   seed, decides whether the key is sufficient for `base`/`trusted` or only
   `anonymous`/`honeypot`.
3. **Register** `{id, name, repo, mcp_url, surface}` via `adhd.cluster.join`.
   adhd mints `adhd.project.<id>.call`.
4. **Idempotent**: the same id + subject + surface is a no-op join; a different
   subject is a conflict.

Creation is a privileged action: require `--create`, an authorised surface, and
an explicit confirmation. A folder with no key must never auto-create a trusted
surface.

## Project ids (implemented)

- `ProjectEntry` now carries `id`; `clusterJoinParams` sends it.
- `adhd.cluster.join` reads `id` and names the dynamic tool
  `adhd.project.<id>.call`; the display name stays in the description.
- Legacy payloads without `id` fall back to the name (existing behaviour).
- Two projects named `grep` with ids `01JA`/`01JB` now produce two tools instead
  of the silent collision in #40.

## Never touch the GitHub identity (implemented as a constraint)

- `lezz see` does not invoke `gh` and does not read `~/.config/gh`.
- `seed.json` holds no token and no GitHub identity; `repo` is a slug for
  provenance.
- `gh` remains the CLI used by `adhd.gh.run` / `lezz cache` for GitHub
  operations. That auth is not the cluster identity.

## Challenges to the current wording

- **"The seed id is what cluster surface"** conflates identity with placement.
  Keep `seed.id` (project) and `seed.surface` (placement) separate; otherwise a
  surface rename changes the project identity, and two projects cannot share a
  surface.
- **"Supply the 42 result to continue"** is a position handshake, not auth.
  Anyone can compute 42. Do not let a valid 42 result admit anyone to a trusted
  surface.
- **"An external key or id"** — a key proves possession; an id is a label. An id
  used as auth needs a signed challenge or a bound key. Do not treat
  `auth.subject` alone as a credential.
- **"Joined or created"** — creation is a privilege. Explicit flag, surface
  policy, and auth; no silent trusted surface from an unauthenticated folder.

## Status

Implemented: `seed.json` schema/validation, `lezz seed init`, `lezz see` (plan +
registry lookup), project-id plumbing and namespaced dynamic tools.

Not implemented: `/seed/claim`, proof verification, surface placement policy,
and the actual join/create. Those are the next step, after deciding whether the
proof is an HMAC key, a public-key signature, or an external OIDC subject.
