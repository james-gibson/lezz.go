package sea

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signature is what the fake codesign/stapler/spctl report. The zero value is
// an unsigned bundle: every test starts from a failure and turns one fact on.
type signature struct {
	authority string
	team      string
	build     string
	runtime   bool
	timestamp bool
	notarized bool
	verifyErr bool
	spctlText string
	noStapler bool
}

func signedSignature() signature {
	return signature{
		authority: Authority,
		team:      TeamID,
		build:     "20261023120000",
		runtime:   true,
		timestamp: true,
		notarized: true,
	}
}

func fakeRunner(sig signature) runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "plutil":
			return []byte(fmt.Sprintf(`{"CFBundleIdentifier":%q,"CFBundleVersion":%q,"CFBundleShortVersionString":"1.0"}`, BundleID, sig.build)), nil
		case "codesign":
			if len(args) > 0 && args[0] == "--verify" {
				if sig.verifyErr {
					return []byte("invalid signature"), errors.New("exit 1")
				}
				return nil, nil
			}
			var b strings.Builder
			b.WriteString("Identifier=" + BundleID + "\n")
			if sig.runtime {
				b.WriteString("CodeDirectory v=20500 size=1 flags=0x10000(runtime) hashes=1+0 location=embedded\n")
			} else {
				b.WriteString("CodeDirectory v=20500 size=1 flags=0x0 hashes=1+0 location=embedded\n")
			}
			if sig.authority != "" {
				b.WriteString("Authority=" + sig.authority + "\n")
			}
			if sig.team != "" {
				b.WriteString("TeamIdentifier=" + sig.team + "\n")
			}
			if sig.timestamp {
				b.WriteString("Timestamp=Oct 23, 2026 at 12:00:00 PM\n")
			}
			return []byte(b.String()), nil
		case "xcrun":
			if sig.noStapler {
				return nil, errors.New("stapler not found")
			}
			if sig.notarized {
				return nil, nil
			}
			return []byte("does not have a ticket stapled"), errors.New("exit 65")
		case "spctl":
			if sig.spctlText != "" {
				return []byte(sig.spctlText), errors.New("exit 3")
			}
			if sig.notarized {
				return []byte("/x/lezz.sea.app: accepted\nsource=Notarized Developer ID\n"), nil
			}
			return []byte("/x/lezz.sea.app: rejected\nsource=Unnotarized Developer ID\n"), errors.New("exit 3")
		case "ditto":
			if len(args) >= 4 && args[0] == "-x" {
				if err := os.MkdirAll(filepath.Join(args[3], AppName), 0o755); err != nil {
					return nil, err
				}
				return nil, nil
			}
			return nil, nil
		case "xattr":
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
	}
}

// testEnv wires an env to fakes: a temp home, the given signature, and an HTTP
// client that may talk to the test server (the production client only speaks
// HTTPS to GitHub).
func testEnv(t *testing.T, srv *httptest.Server, sig signature) *env {
	t.Helper()
	client := &httpClient{
		c:      srv.Client(),
		scheme: "http",
		hosts:  map[string]bool{"127.0.0.1": true},
	}
	return &env{
		home:    t.TempDir(),
		goos:    "darwin",
		channel: Channel,
		base:    srv.URL,
		run:     fakeRunner(sig),
		copyDir: func(_, dst string) error { return os.MkdirAll(dst, 0o755) },
		running: func(string) ([]int, error) { return nil, nil },
		client:  client,
		register: func(string) error {
			return nil
		},
		logf: t.Logf,
	}
}

// releaseServer serves a manifest and archive built from archive bytes.
func releaseServer(t *testing.T, archive []byte, mutate func(*manifest)) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	m := manifest{
		Channel:         Channel,
		Release:         "1.0-dev.20261023120000",
		Version:         "1.0",
		Build:           "20261023120000",
		Commit:          strings.Repeat("a", 40),
		BundleID:        BundleID,
		TeamID:          TeamID,
		Authority:       Authority,
		HardenedRuntime: true,
		Notarized:       true,
		SHA256:          hex.EncodeToString(sum[:]),
		Size:            int64(len(archive)),
		Asset:           AppName + ".zip",
	}
	if mutate != nil {
		mutate(&m)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + Channel + "/dev.json":
			_, _ = w.Write(body)
		case "/" + Channel + "/" + AppName + ".zip":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureInstallsAVerifiedBuild(t *testing.T) {
	archive := []byte("zip-bytes")
	srv := releaseServer(t, archive, nil)
	e := testEnv(t, srv, signedSignature())

	res, err := e.ensure(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !res.Downloaded || res.Build != "20261023120000" || !res.Notarized {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got, want := res.Path, filepath.Join(e.home, "Applications", AppName); got != want {
		t.Fatalf("installed at %s, want %s", got, want)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("installed bundle missing: %v", err)
	}
}

func TestEnsureUsesAnInstalledCopyWithoutTheNetwork(t *testing.T) {
	srv := releaseServer(t, []byte("zip"), nil)
	e := testEnv(t, srv, signedSignature())

	path := filepath.Join(e.home, "Applications", AppName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	e.client = &httpClient{
		c: http.DefaultClient,
		// Any request would be an error: the scheme is deliberately impossible.
		scheme: "not-http",
		hosts:  map[string]bool{},
	}
	e.base = "http://127.0.0.1:1"

	res, err := e.ensure(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if res.Downloaded {
		t.Fatal("re-downloaded although a verified copy was installed")
	}
	if res.Path != path {
		t.Fatalf("returned %s, want %s", res.Path, path)
	}
}

func TestEnsureRejectsABadDigest(t *testing.T) {
	srv := releaseServer(t, []byte("zip"), func(m *manifest) {
		m.SHA256 = strings.Repeat("0", 64)
	})
	e := testEnv(t, srv, signedSignature())

	_, err := e.ensure(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("want a sha256 refusal, got %v", err)
	}
}

func TestEnsureRejectsAShortBody(t *testing.T) {
	srv := releaseServer(t, []byte("zip"), func(m *manifest) {
		m.Size = 4096
	})
	e := testEnv(t, srv, signedSignature())

	_, err := e.ensure(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("want a size refusal, got %v", err)
	}
}

func TestEnsureRejectsAnUnnotarizedBuildByDefault(t *testing.T) {
	sig := signedSignature()
	sig.notarized = false
	srv := releaseServer(t, []byte("zip"), nil)
	e := testEnv(t, srv, sig)

	_, err := e.ensure(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "notarized") {
		t.Fatalf("want a notarization refusal, got %v", err)
	}
}

func TestEnsureCanInstallAnUnnotarizedBuildWhenAsked(t *testing.T) {
	sig := signedSignature()
	sig.notarized = false
	srv := releaseServer(t, []byte("zip"), nil)
	e := testEnv(t, srv, sig)

	res, err := e.ensure(context.Background(), Options{AllowUnnotarized: true})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if res.Notarized {
		t.Fatal("result claims notarization for an un-notarized build")
	}
}

func TestInspectRejectsSignatureFaults(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*signature)
		want   string
	}{
		{"unsigned", func(s *signature) { s.verifyErr = true }, "codesign --verify"},
		{"wrong authority", func(s *signature) { s.authority = "Developer ID Application: Someone Else (ABCDE12345)" }, "authority"},
		{"wrong team", func(s *signature) { s.team = "ABCDE12345" }, "team"},
		{"no hardened runtime", func(s *signature) { s.runtime = false }, "hardened runtime"},
		{"no timestamp", func(s *signature) { s.timestamp = false }, "secure timestamp"},
		{"gatekeeper rejects", func(s *signature) { s.notarized = false }, "notarized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig := signedSignature()
			tc.mutate(&sig)
			e := testEnv(t, httptest.NewServer(http.NotFoundHandler()), sig)
			app := filepath.Join(t.TempDir(), AppName)
			if err := os.MkdirAll(app, 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := e.inspect(context.Background(), app, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in the error, got %v", tc.want, err)
			}
		})
	}
}

func TestInspectAcceptsANotarizedBuild(t *testing.T) {
	e := testEnv(t, httptest.NewServer(http.NotFoundHandler()), signedSignature())
	app := filepath.Join(t.TempDir(), AppName)
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := e.inspect(context.Background(), app, false)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.Notarized || !info.HardenedRuntime || !info.Timestamped {
		t.Fatalf("incomplete info: %+v", info)
	}
}

func TestInstallRefusesARunningBundle(t *testing.T) {
	e := testEnv(t, httptest.NewServer(http.NotFoundHandler()), signedSignature())
	e.running = func(string) ([]int, error) { return []int{42}, nil }

	_, err := e.install(context.Background(), filepath.Join(t.TempDir(), AppName))
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("want a running refusal, got %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	good := manifest{
		Channel:  Channel,
		Release:  "1.0-dev.20261023120000",
		Version:  "1.0",
		Build:    "20261023120000",
		BundleID: BundleID,
		TeamID:   TeamID,
		SHA256:   strings.Repeat("a", 64),
		Asset:    AppName + ".zip",
	}
	if err := good.validate(); err != nil {
		t.Fatalf("a valid manifest was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*manifest)
		want   string
	}{
		{"bundle id", func(m *manifest) { m.BundleID = "co.example.other" }, "bundleId"},
		{"team", func(m *manifest) { m.TeamID = "ABCDE12345" }, "teamId"},
		{"asset traversal", func(m *manifest) { m.Asset = "../" + AppName + ".zip" }, "asset"},
		{"asset name", func(m *manifest) { m.Asset = "other.zip" }, "asset"},
		{"digest length", func(m *manifest) { m.SHA256 = "abc" }, "sha256"},
		{"digest hex", func(m *manifest) { m.SHA256 = strings.Repeat("z", 64) }, "sha256"},
		{"missing build", func(m *manifest) { m.Build = "" }, "release identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := good
			tc.mutate(&m)
			if err := m.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCheckRedirectPolicy(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"github over https", "https://github.com/james-gibson/lezz.sea/releases/download/dev/x.zip", ""},
		{"object storage", "https://objects.githubusercontent.com/blob", ""},
		{"plain http", "http://github.com/x", "not HTTPS"},
		{"other host", "https://evil.example.com/x", "host not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			err = checkRedirect(req, []*http.Request{{}})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestClientRefusesNonHTTPSAndUnknownHosts(t *testing.T) {
	hc := newHTTPClient()

	for _, raw := range []string{"http://github.com/x", "https://evil.example.com/x"} {
		resp, err := hc.do(context.Background(), raw)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("allowed %s", raw)
		}
	}
}

func TestValidChannel(t *testing.T) {
	for _, ok := range []string{"dev", "stable", "v1.2.3", "a_b-c"} {
		if !validChannel(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "dev/../x", "DEV", "dev?x=1", strings.Repeat("a", 65)} {
		if validChannel(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestInspectRealBundle runs the verifier against a real bundle. It is opt-in
// because a Developer ID signature cannot be fabricated in a unit test:
//
//	LEZZ_SEA_APP=/path/to/lezz.sea.app go test ./internal/sea -run RealBundle -v
//
// Set LEZZ_SEA_ALLOW_UNNOTARIZED=1 for a signed build that has no ticket yet.
func TestInspectRealBundle(t *testing.T) {
	app := os.Getenv("LEZZ_SEA_APP")
	if app == "" {
		t.Skip("set LEZZ_SEA_APP to a built lezz.sea.app to run this test")
	}
	allow := os.Getenv("LEZZ_SEA_ALLOW_UNNOTARIZED") == "1"
	info, err := Inspect(context.Background(), app, allow)
	if err != nil {
		t.Fatalf("inspect %s: %v", app, err)
	}
	t.Logf("verified %s %s (%s), team %s, notarized=%t", info.BundleID, info.Version, info.Build, info.TeamID, info.Notarized)
}
