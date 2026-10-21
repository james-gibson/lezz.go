package githubauth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/creativeprojects/go-selfupdate"

	"github.com/james-gibson/lezz.go/internal/credential"
)

// StoredToken returns the access token saved by `lezz auth github`, or
// credential.ErrNotFound when nobody has signed in.
//
// The token is the one the embedded OAuth App client ID minted, so it is this
// CLI's own identity rather than an unrelated personal access token.
func StoredToken() (string, error) {
	rec, err := LoadRecord()
	if err != nil {
		return "", err
	}
	store, err := credential.New(storeKind(rec))
	if err != nil {
		return "", err
	}
	token, err := store.Get(rec.TokenService, rec.TokenAccount)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", credential.ErrNotFound
	}
	return token, nil
}

// storeKind maps a record's backend name back to a credential.New selector.
// Keychain reports "keychain"; FileStore reports "file (0600)".
func storeKind(rec *Record) string {
	if rec != nil && strings.HasPrefix(rec.StoreKind, "file") {
		return "file"
	}
	return "auto"
}

// ReleaseConfig returns the go-selfupdate config used for GitHub release
// downloads.
//
// The preferred credential is the token `lezz auth github` stored — the one the
// embedded OAuth App client ID minted. An ambient $GITHUB_TOKEN is deliberately
// not consulted: a stale or unrelated token turns every public download into a
// 401 (james-gibson/lezz.go#36), and inheriting a stranger's credential is a
// worse default than an anonymous request. With no stored token the config
// carries an anonymous source (60 requests/hour, public repos only).
//
// The source is built here rather than left to NewUpdater because
// selfupdate.Config has no token field: NewUpdater would construct
// GitHubConfig{} and pick up $GITHUB_TOKEN from the environment.
//
// A config that already carries a Source is returned unchanged, so tests can
// substitute their own.
func ReleaseConfig(config selfupdate.Config) (selfupdate.Config, error) {
	if config.Source != nil {
		return config, nil
	}
	token, err := downloadToken()
	if err != nil {
		return config, err
	}
	source, err := newGitHubSource(token)
	if err != nil {
		return config, fmt.Errorf("create github source: %w", err)
	}
	config.Source = source
	return config, nil
}

// downloadToken is the credential policy in one place: the OAuth-app token when
// one is stored, otherwise "" (anonymous). It never reads $GITHUB_TOKEN.
func downloadToken() (string, error) {
	token, err := StoredToken()
	switch {
	case err == nil:
		return token, nil
	case errors.Is(err, credential.ErrNotFound):
		return "", nil // not signed in: anonymous
	default:
		return "", fmt.Errorf("read github token: %w", err)
	}
}

// ReleaseUpdater builds the updater used by `lezz install`, `lezz update` and
// the tool upgrade path.
func ReleaseUpdater(config selfupdate.Config) (*selfupdate.Updater, error) {
	resolved, err := ReleaseConfig(config)
	if err != nil {
		return nil, err
	}
	return selfupdate.NewUpdater(resolved)
}

// newGitHubSource builds a GitHub source that authenticates with token and
// nothing else.
//
// GitHubConfig.APIToken has no "explicitly empty" state — an empty token means
// "read $GITHUB_TOKEN instead" — so the variable is removed for the duration of
// the call. Without this an anonymous updater inherits whatever token happens to
// be exported in the user's shell, which is the failure #36 describes.
func newGitHubSource(token string) (*selfupdate.GitHubSource, error) {
	old, had := os.LookupEnv("GITHUB_TOKEN")
	if had {
		_ = os.Unsetenv("GITHUB_TOKEN")
		defer func() { _ = os.Setenv("GITHUB_TOKEN", old) }()
	}
	return selfupdate.NewGitHubSource(selfupdate.GitHubConfig{APIToken: token})
}
