// Package githubauth implements GitHub's OAuth device flow and the small
// amount of state lezz keeps around it.
//
// Device flow is used deliberately: a distributed CLI cannot keep an OAuth
// client secret, and device flow does not need one. Only the public client ID
// is embedded (or supplied at runtime for development).
package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/james-gibson/lezz.go/internal/credential"
)

const (
	// TokenService is the Keychain service / seed keychain ref name.
	TokenService = "LEZZ_GITHUB_TOKEN" //nolint:gosec // G101: a Keychain service name, not a credential.
	// TokenAccount is the Keychain account name.
	TokenAccount = "lezz"
	// DefaultScopes is the minimum scope: identity only, no repo access.
	DefaultScopes = "read:user"
	// AuthRef is the seed auth.ref for the stored token.
	AuthRef = "keychain:" + TokenService
)

// Client talks to GitHub's device-flow and user endpoints.
type Client struct {
	HTTP          *http.Client
	ClientID      string
	DeviceCodeURL string
	TokenURL      string
	UserURL       string
	UserAgent     string
	// Sleep is injectable for tests. It must respect ctx.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client pointed at github.com.
func New(clientID string) *Client {
	//nolint:gosec // G101: the OAuth client ID is public by design; device flow has no secret.
	return &Client{
		HTTP:          &http.Client{Timeout: 30 * time.Second},
		ClientID:      clientID,
		DeviceCodeURL: "https://github.com/login/device/code",
		TokenURL:      "https://github.com/login/oauth/access_token",
		UserURL:       "https://api.github.com/user",
		UserAgent:     "lezz-github-auth",
		Sleep:         sleepCtx,
	}
}

// DeviceCode is the first device-flow response.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

// Token is a device-flow token response or an OAuth error.
type Token struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"`
}

// Identity is the subset of GET /user that lezz records.
type Identity struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
}

// Record is non-secret metadata stored next to the token.
type Record struct {
	Login        string     `json:"login"`
	ID           int64      `json:"id"`
	Name         string     `json:"name,omitempty"`
	HTMLURL      string     `json:"html_url,omitempty"`
	Scopes       []string   `json:"scopes,omitempty"`
	TokenService string     `json:"token_service"`
	TokenAccount string     `json:"token_account"`
	AuthRef      string     `json:"auth_ref"`
	StoreKind    string     `json:"store_kind"`
	CreatedAt    time.Time  `json:"created_at"`
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
}

// StartDeviceFlow requests a user code and returns the instructions.
func (c *Client) StartDeviceFlow(ctx context.Context, scopes string) (*DeviceCode, error) {
	if c.ClientID == "" {
		return nil, errors.New("github client id is empty")
	}
	if strings.TrimSpace(scopes) == "" {
		scopes = DefaultScopes
	}
	form := url.Values{
		"client_id": {c.ClientID},
		"scope":     {scopes},
	}
	var dc DeviceCode
	if err := c.postForm(ctx, c.DeviceCodeURL, form, &dc); err != nil {
		return nil, err
	}
	if dc.Error != "" {
		return nil, fmt.Errorf("github device code: %s: %s", dc.Error, dc.ErrorDescription)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("github device code: incomplete response")
	}
	if dc.VerificationURI == "" {
		dc.VerificationURI = "https://github.com/login/device"
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	return &dc, nil
}

// WaitForToken polls until the user authorizes, denies, or the code expires.
func (c *Client) WaitForToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	if dc == nil {
		return nil, errors.New("github device code is nil")
	}
	if dc.ExpiresIn > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(dc.ExpiresIn)*time.Second)
		defer cancel()
	}
	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}
	for {
		if err := c.sleep(ctx, time.Duration(interval)*time.Second); err != nil {
			return nil, err
		}
		form := url.Values{
			"client_id":   {c.ClientID},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		var tok Token
		if err := c.postForm(ctx, c.TokenURL, form, &tok); err != nil {
			return nil, err
		}
		if tok.AccessToken != "" {
			return &tok, nil
		}
		switch tok.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5
			if tok.Interval > 0 {
				interval = tok.Interval
			}
			continue
		case "expired_token":
			return nil, errors.New("github device code expired; run the command again")
		case "access_denied":
			return nil, errors.New("github authorization denied")
		case "":
			return nil, errors.New("github token response contained no access token")
		default:
			return nil, fmt.Errorf("github token: %s: %s", tok.Error, tok.ErrorDescription)
		}
	}
}

// FetchIdentity verifies the token and returns the GitHub account.
func (c *Client) FetchIdentity(ctx context.Context, accessToken string) (*Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.UserURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github user: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var id Identity
	if err := json.Unmarshal(body, &id); err != nil {
		return nil, fmt.Errorf("github user: %w", err)
	}
	if id.Login == "" {
		return nil, errors.New("github user: login is empty")
	}
	return &id, nil
}

// Save stores the token and writes non-secret metadata.
func Save(store credential.Store, token *Token, identity *Identity) (*Record, error) {
	return SaveAt(store, DefaultRecordPath(), token, identity)
}

// SaveAt is Save with an explicit metadata path (tests).
func SaveAt(store credential.Store, recordPath string, token *Token, identity *Identity) (*Record, error) {
	if token == nil || token.AccessToken == "" {
		return nil, errors.New("github token is empty")
	}
	if identity == nil || identity.Login == "" {
		return nil, errors.New("github identity is empty")
	}
	if err := store.Set(TokenService, TokenAccount, token.AccessToken); err != nil {
		return nil, err
	}
	rec := &Record{
		Login:        identity.Login,
		ID:           identity.ID,
		Name:         identity.Name,
		HTMLURL:      identity.HTMLURL,
		Scopes:       strings.Fields(token.Scope),
		TokenService: TokenService,
		TokenAccount: TokenAccount,
		AuthRef:      store.Ref(TokenService, TokenAccount),
		StoreKind:    store.Kind(),
		CreatedAt:    time.Now().UTC(),
	}
	if err := WriteRecord(recordPath, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// LoadRecord reads the metadata file.
func LoadRecord() (*Record, error) {
	return LoadRecordFrom(DefaultRecordPath())
}

// LoadRecordFrom reads a metadata file at path.
func LoadRecordFrom(path string) (*Record, error) {
	//nolint:gosec // G304: path is the caller-supplied credential record location.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, credential.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("github record: %w", err)
	}
	return &rec, nil
}

// WriteRecord writes metadata atomically with 0600.
func WriteRecord(path string, rec *Record) error {
	if path == "" {
		path = DefaultRecordPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".github-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// LoadToken returns the stored access token. Metadata is not required.
func LoadToken(store credential.Store) (string, error) {
	return store.Get(TokenService, TokenAccount)
}

// Status returns the stored token and, when reachable, the current identity.
// A stale token still returns the record; the caller decides whether the
// verification error is fatal.
func Status(ctx context.Context, c *Client, store credential.Store) (*Record, *Identity, error) {
	rec, err := LoadRecord()
	if err != nil {
		return nil, nil, err
	}
	token, err := store.Get(TokenService, TokenAccount)
	if err != nil {
		return rec, nil, err
	}
	id, err := c.FetchIdentity(ctx, token)
	if err != nil {
		return rec, nil, err
	}
	now := time.Now().UTC()
	rec.VerifiedAt = &now
	_ = WriteRecord(DefaultRecordPath(), rec)
	return rec, id, nil
}

// Logout deletes the token and metadata. Missing items are not an error.
func Logout(store credential.Store) error {
	if err := store.Delete(TokenService, TokenAccount); err != nil && !errors.Is(err, credential.ErrNotFound) {
		return err
	}
	if err := os.Remove(DefaultRecordPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// DefaultRecordPath is ~/.lezz/auth/github.json.
func DefaultRecordPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".lezz", "auth", "github.json")
	}
	return filepath.Join(home, ".lezz", "auth", "github.json")
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	return nil
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
