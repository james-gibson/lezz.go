package githubauth

import (
	"errors"
	"os"
	"testing"

	"github.com/creativeprojects/go-selfupdate"

	"github.com/james-gibson/lezz.go/internal/credential"
)

// signedIn stages a record and its secret in a temporary HOME, using the file
// store so the test never touches the login keychain.
func signedIn(t *testing.T, token string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	store := credential.FileStore{}
	if err := store.Set(TokenService, TokenAccount, token); err != nil {
		t.Fatalf("store token: %v", err)
	}
	rec := &Record{
		Login:        "octocat",
		TokenService: TokenService,
		TokenAccount: TokenAccount,
		AuthRef:      store.Ref(TokenService, TokenAccount),
		StoreKind:    store.Kind(), // "file (0600)" — not a credential.New selector
	}
	if err := WriteRecord(DefaultRecordPath(), rec); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

// TestStoredToken_ReadsFileStore covers the "file (0600)" kind mapping: a record
// naming the file backend must resolve back to it.
func TestStoredToken_ReadsFileStore(t *testing.T) {
	signedIn(t, "gho_from_device_flow")

	token, err := StoredToken()
	if err != nil {
		t.Fatalf("StoredToken: %v", err)
	}
	if token != "gho_from_device_flow" {
		t.Errorf("token = %q, want gho_from_device_flow", token)
	}
}

// TestStoredToken_NotSignedIn reports ErrNotFound rather than an empty token, so
// callers can tell "nobody signed in" from "the store is broken".
func TestStoredToken_NotSignedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := StoredToken()
	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("err = %v, want credential.ErrNotFound", err)
	}
}

// TestDownloadToken_PrefersStoredToken is the #36 regression: the OAuth-app
// token is the credential, even when an unrelated GITHUB_TOKEN is exported.
func TestDownloadToken_PrefersStoredToken(t *testing.T) {
	signedIn(t, "gho_from_device_flow")
	t.Setenv("GITHUB_TOKEN", "ghp_someone_elses_stale_pat")

	token, err := downloadToken()
	if err != nil {
		t.Fatalf("downloadToken: %v", err)
	}
	if token != "gho_from_device_flow" {
		t.Errorf("token = %q, want the stored OAuth-app token", token)
	}
}

// TestDownloadToken_IgnoresAmbientToken is the other half: with nobody signed in
// the request is anonymous. Inheriting $GITHUB_TOKEN here is what turned every
// install into a 401 and then a silent `go install`.
func TestDownloadToken_IgnoresAmbientToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "ghp_someone_elses_stale_pat")

	token, err := downloadToken()
	if err != nil {
		t.Fatalf("downloadToken: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want anonymous (empty)", token)
	}
}

// TestReleaseConfig_BuildsSourceForToken pins the lib quirk that made this
// necessary: selfupdate.Config carries no APIToken, so the source must be built
// explicitly or NewUpdater would read the environment.
func TestReleaseConfig_BuildsSourceForToken(t *testing.T) {
	signedIn(t, "gho_from_device_flow")

	config, err := ReleaseConfig(selfupdate.Config{})
	if err != nil {
		t.Fatalf("ReleaseConfig: %v", err)
	}
	if config.Source == nil {
		t.Fatal("Source is nil: an anonymous env-derived source would be used")
	}
}

// TestReleaseConfig_PassesThroughTestSource keeps the mock-source seam working.
func TestReleaseConfig_PassesThroughTestSource(t *testing.T) {
	src := &struct{ selfupdate.Source }{}
	config, err := ReleaseConfig(selfupdate.Config{Source: src})
	if err != nil {
		t.Fatalf("ReleaseConfig: %v", err)
	}
	if config.Source != src {
		t.Error("supplied Source was replaced")
	}
}

// TestNewGitHubSource_RestoresEnvironment proves the temporary unset is
// temporary: a caller's shell must be unchanged afterwards.
func TestNewGitHubSource_RestoresEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_should_survive")

	if _, err := newGitHubSource(""); err != nil {
		t.Fatalf("newGitHubSource: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "ghp_should_survive" {
		t.Errorf("GITHUB_TOKEN = %q after the call, want it restored", got)
	}
}
