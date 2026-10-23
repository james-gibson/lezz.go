package sea

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// allowedHosts are the only hosts a download may touch, including redirects.
// github.com serves the release URL; the asset body is served from GitHub's
// object storage under one of the other names.
var allowedHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"codeload.github.com":                  true,
}

type httpClient struct {
	c      *http.Client
	scheme string
	hosts  map[string]bool
}

func newHTTPClient() *httpClient {
	return &httpClient{
		c: &http.Client{
			Timeout:       2 * time.Minute,
			CheckRedirect: checkRedirect,
		},
		scheme: "https",
		hosts:  allowedHosts,
	}
}

// checkRedirect refuses anything that is not HTTPS to an allowed host. A
// redirect is attacker-controlled content, so it is not allowed to move the
// download to a new origin or downgrade the transport.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to %s (not HTTPS)", req.URL.Redacted())
	}
	if !allowedHosts[req.URL.Hostname()] {
		return fmt.Errorf("refusing redirect to %s (host not allowed)", req.URL.Hostname())
	}
	return nil
}

// manifest is dev.json, published next to the archive. It is a description,
// not a trust anchor: every field is checked against the bundle, and the
// bundle is checked against Apple's signature.
type manifest struct {
	Channel         string `json:"channel"`
	Release         string `json:"release"`
	Version         string `json:"version"`
	Build           string `json:"build"`
	Commit          string `json:"commit"`
	Dirty           bool   `json:"dirty"`
	BundleID        string `json:"bundleId"`
	TeamID          string `json:"teamId"`
	Authority       string `json:"authority"`
	HardenedRuntime bool   `json:"hardenedRuntime"`
	Notarized       bool   `json:"notarized"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	Asset           string `json:"asset"`
}

// validate rejects a manifest that could not describe our app, before its
// values are used to build a URL or to trust a byte.
func (m manifest) validate() error {
	switch {
	case m.BundleID != BundleID:
		return fmt.Errorf("manifest bundleId is %q, expected %q", m.BundleID, BundleID)
	case m.TeamID != TeamID:
		return fmt.Errorf("manifest teamId is %q, expected %q", m.TeamID, TeamID)
	case m.Release == "" || m.Version == "" || m.Build == "":
		return errors.New("manifest is missing the release identity (release, version or build)")
	case m.Asset != AppName+".zip":
		return fmt.Errorf("manifest asset is %q, expected %q", m.Asset, AppName+".zip")
	case path.Base(m.Asset) != m.Asset || strings.Contains(m.Asset, ".."):
		return fmt.Errorf("manifest asset %q is not a plain file name", m.Asset)
	case len(m.SHA256) != sha256.Size*2:
		return fmt.Errorf("manifest sha256 %q is not a SHA-256 digest", m.SHA256)
	}
	if _, err := hex.DecodeString(m.SHA256); err != nil {
		return fmt.Errorf("manifest sha256 %q is not hexadecimal", m.SHA256)
	}
	if m.Size < 0 {
		return fmt.Errorf("manifest size %d is negative", m.Size)
	}
	return nil
}

func (e *env) fetchManifest(ctx context.Context) (manifest, error) {
	raw := fmt.Sprintf("%s/%s/dev.json", strings.TrimRight(e.base, "/"), e.channel)
	body, err := e.client.get(ctx, raw, maxManifest)
	if err != nil {
		var status *statusError
		if errors.As(err, &status) && status.code == http.StatusNotFound {
			return manifest{}, fmt.Errorf("no %q release has a dev.json yet: publish one from lezz.sea with scripts/publish-dev.sh (%s)", e.channel, raw)
		}
		return manifest{}, fmt.Errorf("fetch manifest: %w", err)
	}

	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return manifest{}, fmt.Errorf("parse manifest %s: %w", raw, err)
	}
	if err := m.validate(); err != nil {
		return manifest{}, fmt.Errorf("manifest %s: %w", raw, err)
	}
	return m, nil
}

// fetchArchive downloads the archive to a temporary file, hashing as it goes,
// and returns the path plus a cleanup function. Nothing is unpacked here: a
// bad digest is rejected before the archive is ever read as a zip.
func (e *env) fetchArchive(ctx context.Context, m manifest) (string, func(), error) {
	raw := fmt.Sprintf("%s/%s/%s", strings.TrimRight(e.base, "/"), e.channel, m.Asset)

	limit := int64(maxArchive)
	if m.Size > 0 {
		// One byte of slack so a body that grew since the manifest was written
		// fails as a size mismatch instead of being silently truncated.
		limit = m.Size + 1
	}

	resp, err := e.client.do(ctx, raw)
	if err != nil {
		return "", func() {}, fmt.Errorf("download %s: %w", m.Asset, err)
	}
	defer func() { _ = resp.Body.Close() }()

	f, err := os.CreateTemp("", "lezz-sea-*.zip")
	if err != nil {
		return "", func() {}, fmt.Errorf("create download file: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", cleanup, fmt.Errorf("download %s: %w", m.Asset, err)
	}
	if err := f.Close(); err != nil {
		return "", cleanup, fmt.Errorf("close download: %w", err)
	}
	if limit == m.Size+1 && n != m.Size {
		return "", cleanup, fmt.Errorf("%s is %d bytes, the manifest says %d", m.Asset, n, m.Size)
	}

	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, m.SHA256) {
		return "", cleanup, fmt.Errorf("%s has sha256 %s, the manifest says %s — refusing to unpack it", m.Asset, got, m.SHA256)
	}
	return f.Name(), cleanup, nil
}

// extract unpacks the archive and returns the app bundle inside it.
func (e *env) extract(ctx context.Context, archive, work string) (string, error) {
	dir := filepath.Join(work, "unpacked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create unpack dir: %w", err)
	}
	if _, err := e.run(ctx, "ditto", "-x", "-k", archive, dir); err != nil {
		return "", fmt.Errorf("unpack %s: %w", filepath.Base(archive), err)
	}
	app := filepath.Join(dir, AppName)
	st, err := os.Stat(app)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("the archive does not contain %s", AppName)
	}
	return app, nil
}

// statusError carries an HTTP status so callers can name a missing release.
type statusError struct {
	code int
	url  string
}

func (s *statusError) Error() string {
	return fmt.Sprintf("%s returned %d %s", s.url, s.code, http.StatusText(s.code))
}

func (c *httpClient) do(ctx context.Context, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", raw, err)
	}
	if u.Scheme != c.scheme {
		return nil, fmt.Errorf("refusing %s: only %s is allowed", u.Redacted(), strings.ToUpper(c.scheme))
	}
	if !c.hosts[u.Hostname()] {
		return nil, fmt.Errorf("refusing %s: host %q is not allowed", u.Redacted(), u.Hostname())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lezz-sea-installer")
	resp, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &statusError{code: resp.StatusCode, url: u.Redacted()}
	}
	return resp, nil
}

func (c *httpClient) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	resp, err := c.do(ctx, raw)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) == limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", raw, limit)
	}
	return body, nil
}
