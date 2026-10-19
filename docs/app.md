# Lezz as a macOS application

`lezz app` wraps the same binary in a `.app` bundle so macOS has one stable
identity to attach permissions and credentials to. The bundle is not a second
implementation: `Contents/MacOS/lezz` is the lezz binary, and the base cluster is
still `lezz auto`.

## Why a bundle

A CLI launched from Terminal is attributed to Terminal for TCC. A CLI launched
from a `.app` through LaunchServices is attributed to the app. That changes:

- **Local Network** — mDNS discovery and LAN peers are attributed to Lezz, not
  Terminal.
- **Full Disk Access** — Lezz appears as its own row in System Settings, so it
  can be granted access to protected paths without granting Terminal the same.
- **Keychain and passkeys** — the bundle identifier and code signature are the
  app's identity. Native passkeys (`ASAuthorizationPlatformPublicKeyCredentialProvider`)
  require a bundle with an associated domain; a bare CLI cannot hold one.

## Commands

```sh
lezz app install                 # ~/Applications/Lezz.app, background (LSUIElement)
lezz app install --dock          # show a Dock icon instead
lezz app install --dest /Applications/Lezz.app
lezz app install --identity "Developer ID Application: Name (TEAMID)"
lezz app open                    # LaunchServices; default command is auto
lezz app open -- anon            # run a different lezz command in the app
lezz app info                    # bundle id, signature, TCC-relevant plist keys
lezz app uninstall [--force]
```

`app open` uses `open -a`, not `exec`. That is deliberate: it is what makes the
process a LaunchServices app with its own TCC identity. `--wait` waits for exit;
`--new` starts a second instance. Without `--new`, opening an already-running
app activates it and does not apply the new arguments.

A Finder double-click passes `-psn_<...>` to the binary. `lezz` strips that
argument and, when it is running inside a bundle with no other arguments,
defaults to `auto`. `lezz app open -- anon` still selects another command.

## Bundle contents

```
Lezz.app/
  Contents/
    Info.plist
    PkgInfo
    MacOS/lezz
```

Relevant `Info.plist` keys:

| Key | Value |
|---|---|
| `CFBundleIdentifier` | `co.james-gibson.lezz` (override with `--bundle-id`) |
| `CFBundleExecutable` | `lezz` |
| `LSUIElement` | `true` by default (background app); `--dock` sets `false` |
| `NSLocalNetworkUsageDescription` | local-network prompt text |
| `NSBonjourServices` | `_lezz-demo._tcp` |
| `CFBundleURLTypes` | `lezz://` callback scheme (reserved; not handled yet) |
| `LSMinimumSystemVersion` | `13.0` |

There is no app sandbox. Sandboxing a process that execs lab binaries and scans
`~/.config` would require a long entitlement list and would break the existing
process model. `--entitlements file.plist` is passed to `codesign` for cases
where a specific entitlement is needed.

## Permissions

After install:

1. `lezz app open`
2. System Settings → Privacy & Security → Local Network → enable **Lezz**.
3. If lezz must read protected paths, add **Lezz.app** to Full Disk Access.
   There is no usage-description key for Full Disk Access; the user grants it.

TCC grants are keyed to the code signature. An **ad-hoc** signature (`-`) is the
default when no Developer ID is present, and its identity can change when the
app is rebuilt. For durable permissions, sign with a Developer ID:

```sh
lezz app install --identity "Developer ID Application: Your Name (TEAMID)"
```

The app still runs with an ad-hoc signature for local work; expect to re-grant
permissions after a rebuild in that case.

## Passkeys

The bundle provides the identity, but passkeys are **not implemented yet**.
There are two viable paths, and they are not equivalent:

1. **Native app passkey** — a small Swift/ObjC helper inside the bundle calls
   `ASAuthorizationPlatformPublicKeyCredentialProvider`. This requires:
   - a `com.apple.developer.associated-domains` entitlement
     (`webcredentials:example.com`),
   - a real domain serving `apple-app-site-association`,
   - a Team ID and a provisioning profile, and
   - signing with that identity (not ad-hoc).

   This is the only way the passkey is associated with **Lezz.app** itself.

2. **Browser WebAuthn ceremony** — `lezz` starts a loopback HTTP server, opens
   the system browser, and the page calls `navigator.credentials.create/get`.
   This works with iCloud Keychain/third-party passkey providers today, but the
   credential belongs to the browser and the RP ID (for example `localhost`),
   not to the Lezz bundle. It is usable for operator/cluster auth, not as an
   app-scoped passkey.

The `lezz://` URL scheme is registered now so a future native callback has a
return path. It does nothing until a helper or URL handler is added.

For the quick path that does not require a native helper, see
[github-auth.md](github-auth.md): GitHub's device flow supplies an operator
identity, but it is a bearer token, not a passkey.

## Security notes

- `Info.plist` is public; never put a token, private key, or seed secret in it.
- The app copies the binary; updating lezz requires `lezz app install --force`
  (or a future `app update`) so the bundle and signature stay in sync.
- `app uninstall` refuses to remove a running app unless `--force`; `--force`
  sends `SIGTERM`, waits three seconds, then sends `SIGKILL`.
- Commands launched by the app (`ocd-smoke-alarm`, `adhd`) are children of the
  app. macOS normally attributes their TCC access to the responsible app, which
  is why the bundle identity matters for the whole cluster.
