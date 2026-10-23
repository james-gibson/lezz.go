package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBase(t *testing.T) {
	cases := map[string]string{
		"":          "/",
		"/":         "/",
		"/lezz.go":  "/lezz.go/",
		"/lezz.go/": "/lezz.go/",
		"lezz.go":   "/lezz.go/",
		"  /x/y  ":  "/x/y/",
		"//":        "/",
	}
	for in, want := range cases {
		if got := normalizeBase(in); got != want {
			t.Errorf("normalizeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPagePath(t *testing.T) {
	cases := map[string]string{
		"app.md":            "docs/app.html",
		"queue-acp.md":      "docs/queue-acp.html",
		"adr/README.md":     "docs/adr/index.html",
		"adr/0001-thing.md": "docs/adr/0001-thing.html",
	}
	for in, want := range cases {
		if got := pagePath(in); got != want {
			t.Errorf("pagePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleAndDescription(t *testing.T) {
	body := []byte("# Queue sidecar: spool + FIFO doorbell\n\nSee [x](y.md) for the model.\n")
	if got := titleOf(body, "queue.md"); got != "Queue sidecar: spool + FIFO doorbell" {
		t.Errorf("titleOf = %q", got)
	}
	if got := firstParagraph(body); !strings.HasPrefix(got, "See [x](y.md)") {
		t.Errorf("firstParagraph = %q", got)
	}

	// A document without a heading still needs a nav label.
	if got := titleOf([]byte("no heading here\n"), "some-file.md"); got != "some file" {
		t.Errorf("titleOf fallback = %q", got)
	}
}

func TestResolveDocLink(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "queue-acp.md"), "# A\n")
	mustWrite(t, filepath.Join(root, "adr", "README.md"), "# B\n")

	cases := []struct {
		name   string
		dest   string
		srcDir string
		want   string
		ok     bool
	}{
		{"sibling", "queue-acp.md", ".", "/lezz.go/docs/queue-acp.html", true},
		{"fragment", "queue-acp.md#spool", ".", "/lezz.go/docs/queue-acp.html#spool", true},
		{"nested readme", "adr/README.md", ".", "/lezz.go/docs/adr/index.html", true},
		{"parent", "../queue-acp.md", "adr", "/lezz.go/docs/queue-acp.html", true},
		{"external", "https://example.com/x.md", ".", "", false},
		{"anchor only", "#section", ".", "", false},
		{"mailto", "mailto:a@b.c", ".", "", false},
		{"not markdown", "diagram.png", ".", "", false},
		{"missing file", "nope.md", ".", "", false},
		{"escapes docs root", "../../README.md", ".", "", false},
	}

	for _, c := range cases {
		got, ok := resolveDocLink(c.dest, c.srcDir, root, "/lezz.go/")
		if ok != c.ok || got != c.want {
			t.Errorf("%s: resolveDocLink(%q) = (%q, %v), want (%q, %v)",
				c.name, c.dest, got, ok, c.want, c.ok)
		}
	}
}

// TestRunRendersSite exercises the whole pipeline on a small fixture: the
// failure this guards against is a site that builds but publishes dead links,
// because nothing else in CI would notice until the pages were live.
func TestRunRendersSite(t *testing.T) {
	docs := t.TempDir()
	out := t.TempDir()

	mustWrite(t, filepath.Join(docs, "app.md"),
		"# The app\n\nSee [the queue](queue.md) and [the ADRs](adr/README.md).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	mustWrite(t, filepath.Join(docs, "queue.md"), "# Queue\n\nBack to [the app](app.md).\n")
	mustWrite(t, filepath.Join(docs, "adr", "README.md"), "# Decisions\n\n- [one](0001-one.md)\n")
	mustWrite(t, filepath.Join(docs, "adr", "0001-one.md"), "# One\n")

	if err := run(docs, out, "/lezz.go/", "v9.9.9"); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, name := range []string{
		"index.html",
		"docs/app.html",
		"docs/queue.html",
		"docs/adr/index.html",
		"docs/adr/0001-one.html",
		"assets/style.css",
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(name))); err != nil {
			t.Errorf("missing output %s", name)
		}
	}

	app := readFile(t, filepath.Join(out, "docs", "app.html"))
	for _, want := range []string{
		`href="/lezz.go/docs/queue.html"`,
		`href="/lezz.go/docs/adr/index.html"`,
		"<table>", // GFM tables must survive
		"v9.9.9",  // footer version
	} {
		if !strings.Contains(app, want) {
			t.Errorf("docs/app.html missing %q", want)
		}
	}
	if strings.Contains(app, `.md"`) {
		t.Errorf("docs/app.html still links to a .md source")
	}

	landing := readFile(t, filepath.Join(out, "index.html"))
	if !strings.Contains(landing, `href="/lezz.go/docs/app.html"`) {
		t.Errorf("landing page does not link into the docs")
	}
	if !strings.Contains(landing, "lezz.sea") {
		t.Errorf("landing page is missing the companion app")
	}
}

func TestRunRejectsEmptyDocs(t *testing.T) {
	if err := run(t.TempDir(), t.TempDir(), "/", "dev"); err == nil {
		t.Fatal("expected an error for a docs directory with no markdown")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
