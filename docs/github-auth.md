# GitHub device-flow auth

`lezz auth github` signs in through a GitHub OAuth App using the **device flow**.
It is the quick path: no client secret, no native helper, no browser extension.
The client ID is public and is embedded at build time.

This is not a passkey. The result is a bearer token stored in the platform
credential store. See [app.md](app.md) for the native passkey path.

## Create the OAuth App

1. GitHub → Settings → Developer settings → OAuth Apps → New OAuth App.
2. Enable **Device Flow**.
3. Copy the **Client ID**. There is no client secret in this flow — do not add
   one to the binary or to the repository.

## Embed the client ID

The client ID is not a secret. Pick one of:

```sh
# Build-time (recommended; what releases do)
go build -ldflags "-X main.buildGitHubClientID=<client-id>" ./cmd/lezz

# Development override
LEZZ_GITHUB_CLIENT_ID=<client-id> go run ./cmd/lezz auth github

# Explicit one-off
lezz auth github --client-id <client-id>
```

Resolution order: `--client-id`, then `LEZZ_GITHUB_CLIENT_ID`, then the embedded
value.

Goreleaser and the OTA build take `LEZZ_GITHUB_CLIENT_ID` from the repository
variable of the same name:

```yaml
env:
  LEZZ_GITHUB_CLIENT_ID: ${{ vars.LEZZ_GITHUB_CLIENT_ID }}
```

If the value is absent, the binary still builds; `auth github` explains how to
supply it.

## Sign in

```sh
lezz auth github
```

It prints a URL and a short code, opens the browser when possible, and polls
GitHub. Approve the code in GitHub; lezz then verifies the token with
`GET /user` and stores it.

```
Open https://github.com/login/device and enter code: ABCD-1234
Waiting for GitHub authorization...
signed in   octocat (id 42)
scopes      read:user
token       keychain:LEZZ_GITHUB_TOKEN (keychain; value not printed)
metadata    ~/.lezz/auth/github.json
auth-ref    keychain:LEZZ_GITHUB_TOKEN
seed        lezz seed init --auth-ref keychain:LEZZ_GITHUB_TOKEN
```

`--no-browser` prints the URL only. `--scopes` defaults to `read:user`; do not
request `repo` unless a feature actually needs it.

## Downloads

`lezz install` and `lezz update` call `api.github.com` for release metadata and
then download the asset. The credential order is:

1. the token `lezz auth github` stored — the one the embedded client ID minted;
2. nothing: an anonymous request (60 requests/hour per IP, public repos only).

`$GITHUB_TOKEN` is never consulted, even when exported. A stale or unrelated
personal access token used to turn every public download into a 401, and
`lezz install` treated that as "no release" and quietly fell back to
`go install` — reporting `latest` instead of a version (james-gibson/lezz.go#36).

Signing in is not required for public repositories. It is worth doing on a
shared IP (CI, an office NAT) where the anonymous budget is already spent, and
it is required for private repositories: those need `--scopes repo`, which the
default `read:user` does not grant.

To use a different credential, sign in again — `lezz auth logout` then
`lezz auth github`. There is no separate token variable to set.

## Check and remove

```sh
lezz auth status            # verifies the token against GET /user
lezz auth status --json
lezz auth logout            # removes token + metadata
```

`status` never prints the token. It reports the login, id, scopes, store,
`auth-ref`, creation time, and whether GitHub still accepts the token. If GitHub
is unreachable, the stored record is still shown with `verified no (…)`.

## Storage

| Platform | Backend | Ref |
|---|---|---|
| macOS | login Keychain via `security(1)` | `keychain:LEZZ_GITHUB_TOKEN` |
| other | `~/.lezz/auth/*.secret` at 0600 | `file:/…/LEZZ_GITHUB_TOKEN__lezz.secret` |

Non-secret metadata lives at `~/.lezz/auth/github.json` (0600): login, id,
scopes, store kind, `auth-ref`, timestamps. The token itself is never in that
file.

Use the token as the seed's external auth reference:

```sh
lezz seed init --auth-ref keychain:LEZZ_GITHUB_TOKEN
lezz see
```

`lezz see` resolves `keychain:` by asking `security(1)` whether the item exists;
it does not print the value. On a machine without a Keychain-backed item, the
reference reads as absent.

The file fallback exists for headless/test systems. It is a 0600 file, not an
encrypted store; prefer Keychain where available.

## Security notes

- **No client secret.** A distributed CLI cannot keep one. Device flow is
  designed for public clients.
- **Bearer token, not an assertion.** A leaked token can be replayed until it is
  revoked. It does not resist phishing the way WebAuthn does.
- **Keychain write exposure.** `security add-generic-password` accepts the
  secret as an argument, so the token is briefly visible in the process table
  during `auth github`. Reads use `security find-generic-password -w` and do not
  put the secret in argv. The native helper can replace this with `SecItemAdd`
  and an access-control object.
- **Not yet app-bound.** The item is a normal login-keychain generic password.
  A future native helper can bind it to the Lezz.app code signature and require
  user presence.
- **Scopes.** `read:user` only. The token authenticates release downloads
  (`lezz install`, `lezz update`) against the 5,000 requests/hour authenticated
  budget instead of the anonymous 60; the queue/cache paths still use `gh` or
  their own credentials.
- **Revocation.** `lezz auth logout` removes the local copy. Revoke the grant in
  GitHub → Settings → Applications if the token may have leaked.

## Cross-OS direction

`internal/credential` is the seam:

```go
type Store interface {
    Set(service, account, secret string) error
    Get(service, account string) (string, error)
    Delete(service, account string) error
    Ref(service, account string) string
    Kind() string
}
```

Next backends: Linux Secret Service (`secret-tool`) and Windows Credential
Manager. The device flow and metadata format are OS-independent. Browser opening
already uses `open` (macOS), `xdg-open` (Linux), and `rundll32` (Windows).

The existing `internal/bridge` and `internal/queue` packages use Unix-only
syscalls, so a clean Windows build is blocked independently of auth. Linux
builds today; Windows needs those packages ported or build-tagged first.

## Relation to passkeys

GitHub OAuth and WebAuthn are complementary:

- GitHub OAuth answers “which GitHub account is this?” once, and can gate
  enrollment or act as recovery.
- A WebAuthn passkey answers “is the private key for this RP ID present?”
  per challenge, without a bearer secret.

For day-to-day cluster auth, the stronger design is: GitHub OAuth for bootstrap
and recovery, a passkey for the assertion. The quick path stops at the first
half.
