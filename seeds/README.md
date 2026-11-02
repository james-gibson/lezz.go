# Seeds

`seed.json` is a project's identity and placement: a stable id, the surface it
joins, and a *reference* to an external key — never the key itself
([docs/seed.md](../docs/seed.md)). `lezz seed init` writes one per project.

This directory holds seeds that are meant to be fetched, not generated.

## `public/zaphod-beeblebrox.json`

A public demo seed. Fetch it over HTTPS and look at it:

```sh
curl -fsSLO https://raw.githubusercontent.com/james-gibson/lezz.go/main/seeds/public/zaphod-beeblebrox.json
lezz see --file zaphod-beeblebrox.json
```

```json
{
  "seed":   { "id": "zaphod-beeblebrox", "surface": "base", "start": { "real": 42, "imaginary": 0, "rung": 0 } },
  "project": { "name": "zaphod", "repo": "james-gibson/isotope" },
  "auth":   { "mode": "external", "ref": "env:ZAPHOD_CLUSTER_KEY", "subject": "zaphod-beeblebrox" }
}
```

**It grants nothing, on purpose.** `auth.ref` names an environment variable that
is set nowhere, so `lezz see` reports `auth absent` and the project is on the
anonymous/empty surface until a key of its own is present. That is the honest
state for a file anyone can download: a seed is a binding, not proof.

**Why Zaphod.** `zaphod-beeblebrox` owns the **burned** keypair committed at
`isotope/testdata/burned` — private half published, comment `DO NOT USE`. A
burned key is one everybody has, which makes him the least trustworthy owner
available, and therefore the right name for an identity handed out in public.
Nothing he signs is worth anything, and verification does not care who signed.

**Why `surface: base`.** `lezz see` matches the surface against the discovery
registry, where a running `lezz auto` cluster calls itself `base-<pid>`. Point
the file at a surface that exists so it is useful the moment it is fetched.

**Why a repo file and not a gist.** A seed binds an identity, so it should be
reviewable: a diff, a test that `internal/seed` accepts it, and a history. A gist
has none of those, and the raw URL from a public repository is just as easy to
fetch.

The file pins no trust roots. That is `roots` in [lezz.go#82][82]; when it lands,
this seed is the natural place to pin Zaphod's already-public key.

[82]: https://github.com/james-gibson/lezz.go/issues/82
