package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/james-gibson/lezz.go/internal/demo"
	"github.com/james-gibson/lezz.go/internal/mcpwrapper"
)

func TestGitHubClientID(t *testing.T) {
	old := buildGitHubClientID
	defer func() { buildGitHubClientID = old }()
	buildGitHubClientID = "build-id"
	t.Setenv("LEZZ_GITHUB_CLIENT_ID", "")
	if got := githubClientID(""); got != "build-id" {
		t.Fatalf("build id = %q", got)
	}
	t.Setenv("LEZZ_GITHUB_CLIENT_ID", "env-id")
	if got := githubClientID(""); got != "env-id" {
		t.Fatalf("env id = %q", got)
	}
	if got := githubClientID("flag-id"); got != "flag-id" {
		t.Fatalf("flag id = %q", got)
	}
}

func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "finder sends psn to a bundle",
			in:   []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "-psn_0_12345"},
			want: []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "auto"},
		},
		{
			name: "bundle with an explicit command keeps it",
			in:   []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "anon"},
			want: []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "anon"},
		},
		{
			name: "psn is stripped before the command",
			in:   []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "-psn_0_1", "version"},
			want: []string{"/Users/x/Applications/Lezz.app/Contents/MacOS/lezz", "version"},
		},
		{
			name: "cli launch is unchanged",
			in:   []string{"/Users/x/go/bin/lezz"},
			want: []string{"/Users/x/go/bin/lezz"},
		},
		{
			name: "cli launch with a command is unchanged",
			in:   []string{"/Users/x/go/bin/lezz", "app", "info"},
			want: []string{"/Users/x/go/bin/lezz", "app", "info"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeArgs(%v) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestHelpRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"long", []string{"--help"}, true},
		{"short", []string{"-h"}, true},
		{"single dash long", []string{"-help"}, true},
		{"double dash short", []string{"--h"}, true},
		{"after another flag", []string{"--dock", "--help"}, true},
		{"no args", nil, false},
		{"positional", []string{"adhd"}, false},
		{"tool args are not ours", []string{"adhd", "-h"}, false},
		{"option terminator", []string{"--", "--help"}, false},
		{"lone dash", []string{"-"}, false},
		{"value stops the scan", []string{"--config", "/p", "--help"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := helpRequested(tc.args); got != tc.want {
				t.Fatalf("helpRequested(%v) = %v; want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestLookupCommand(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"demo", "demo"},
		{"prod", "prod"},
		{"app", "app"},
		{"app install", "app install"},
		{"service purge", "service purge"},
		{"agent build", "agent build"},
		{"debug keygen", "debug"},
		{"seed", "seed"},
	}
	for _, tc := range cases {
		doc, ok := lookupCommand(tc.path)
		if !ok {
			t.Fatalf("lookupCommand(%q) found nothing", tc.path)
		}
		if doc.path != tc.want {
			t.Fatalf("lookupCommand(%q) = %q; want %q", tc.path, doc.path, tc.want)
		}
	}
	if _, ok := lookupCommand("nope"); ok {
		t.Fatal("lookupCommand(nope) resolved")
	}
}

// The command table drives both usage() and per-command help. A duplicate or
// empty row would make one of them lie.
func TestCommandDocs(t *testing.T) {
	seen := map[string]bool{}
	listed := 0
	for _, d := range commandDocs {
		if d.path == "" || d.display == "" || d.synopsis == "" {
			t.Fatalf("incomplete doc: %+v", d)
		}
		if seen[d.path] {
			t.Fatalf("duplicate doc path %q", d.path)
		}
		seen[d.path] = true
		if doc, ok := lookupCommand(d.path); !ok || doc.path != d.path {
			t.Fatalf("lookupCommand(%q) did not resolve to its own doc", d.path)
		}
		if d.listed {
			listed++
		}
	}
	if listed == 0 {
		t.Fatal("no listed rows: usage() would print nothing")
	}
	if usageWidth == 0 {
		t.Fatal("usageWidth is zero")
	}
}

func TestPrintUsageListsEveryListedCommand(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	for _, d := range commandDocs {
		if !d.listed {
			continue
		}
		if !strings.Contains(out, d.display) {
			t.Fatalf("usage() is missing %q", d.display)
		}
		if !strings.Contains(out, d.synopsis) {
			t.Fatalf("usage() is missing the synopsis for %q", d.path)
		}
	}
	if !strings.Contains(out, "lezz <command> --help") {
		t.Fatal("usage() does not mention per-command help")
	}
}

func TestPrintCommandUsage(t *testing.T) {
	var buf bytes.Buffer
	demoFS := flag.NewFlagSet("demo", flag.ContinueOnError)
	demoFS.Int("alarm-a-port", 0, "pin the alarm-a health port")
	printCommandUsage(&buf, "demo", demoFS)
	out := buf.String()
	for _, want := range []string{
		"lezz demo — Launch a self-contained demo cluster",
		"Usage:\n  lezz demo [flags]\n",
		"-alarm-a-port",
		"Run 'lezz --help'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo usage is missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	fs := flag.NewFlagSet("cache", flag.ContinueOnError)
	fs.String("config", "", "path to a cache YAML")
	printCommandUsage(&buf, "cache", fs)
	out = buf.String()
	for _, want := range []string{"-config", "Flags:", "lezz cache [--config file]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cache usage is missing %q:\n%s", want, out)
		}
	}

	// A parent path speaks for its sub-actions but keeps their invocation.
	buf.Reset()
	printCommandUsage(&buf, "debug keygen", nil)
	out = buf.String()
	if !strings.Contains(out, "Usage:\n  lezz debug keygen\n") {
		t.Fatalf("sub-command usage line is wrong:\n%s", out)
	}
	if !strings.Contains(out, "Debug lease keys and signing") {
		t.Fatalf("sub-command synopsis is missing:\n%s", out)
	}
}

func TestNoArguments(t *testing.T) {
	if err := noArguments("demo", nil); err != nil {
		t.Fatalf("noArguments() = %v; want nil", err)
	}
	err := noArguments("demo", []string{"--dock"})
	if err == nil {
		t.Fatal("noArguments(--dock) = nil; want an error")
	}
	if !strings.Contains(err.Error(), "lezz demo --help") {
		t.Fatalf("error should point at --help: %v", err)
	}
}

// TestRegistryBindIsOnEveryRegistryStarter guards the user-facing spelling for
// lezz.go#59. Three commands start the /cluster registry, and each one must
// offer the same opt-in: a command with no flag either leaves the registry on
// the LAN or has no way to publish it.
func TestRegistryBindIsOnEveryRegistryStarter(t *testing.T) {
	if fs, _ := newDemoFlagSet("demo"); fs.Lookup("registry-bind") == nil {
		t.Error("demo has no --registry-bind flag")
	}
	if fs, _ := newAutoFlagSet(); fs.Lookup("registry-bind") == nil {
		t.Error("auto has no --registry-bind flag")
	}
	// prod parses through demo.ParseProdArgs.
	if _, err := demo.ParseProdArgs([]string{"--registry-bind", "0.0.0.0"}, nil); err != nil {
		t.Errorf("prod rejects --registry-bind: %v", err)
	}
	// The flag is an address, not a name: the same rejection demo applies.
	if _, err := demo.ParseProdArgs([]string{"--registry-bind", "localhost"}, nil); err == nil {
		t.Error("prod accepted a hostname registry bind")
	}
}

// TestPathHint_IsIdempotent is the #15 regression: the printed profile edit must
// leave exactly one line no matter how many times it is pasted. One real
// ~/.zshrc collected 294 copies from the old unguarded `>>`.
func TestPathHint_IsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	hint := pathHint("/tmp/lezzbin", "/bin/zsh")
	command := pathProfileCommand(`export PATH="/tmp/lezzbin:$PATH"`, "~/.zshrc")
	if !strings.Contains(hint, command) {
		t.Fatalf("hint does not contain the guarded command:\n%s", hint)
	}
	if strings.Contains(hint, "&& source ~/.zshrc") {
		t.Errorf("hint still offers the unguarded append:\n%s", hint)
	}

	for range 3 {
		out, err := exec.Command("sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Fatalf("run printed command: %v\n%s", err, out)
		}
		if len(bytes.TrimSpace(out)) != 0 {
			t.Errorf("printed command is noisy on an empty profile: %q", out)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if got := strings.Count(string(data), "export PATH="); got != 1 {
		t.Errorf("profile has %d export lines after 3 runs, want 1:\n%s", got, data)
	}
}

// TestPathHint_AppendsAlongsideOtherContent keeps the guard from being a blunt
// "file is non-empty" check: an unrelated profile must still get the line.
func TestPathHint_AppendsAlongsideOtherContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(profile, []byte("export EDITOR=vim\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := pathProfileCommand(`export PATH="/tmp/lezzbin:$PATH"`, "~/.zshrc")
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	data, _ := os.ReadFile(profile)
	if !strings.Contains(string(data), "export EDITOR=vim") {
		t.Error("existing profile content was lost")
	}
	if !strings.Contains(string(data), "/tmp/lezzbin") {
		t.Errorf("PATH line was not added:\n%s", data)
	}
}

// TestShellProfile pins the file choice, including the shells this hint cannot
// write for.
func TestShellProfile(t *testing.T) {
	for _, tc := range []struct{ shell, want string }{
		{"/bin/zsh", "~/.zshrc"},
		{"/usr/local/bin/zsh", "~/.zshrc"},
		{"/bin/bash", "~/.bash_profile"},
		{"/bin/sh", "~/.profile"},
		{"", "~/.profile"},
		{"/usr/local/bin/fish", ""},
		{"/opt/homebrew/bin/nu", ""},
	} {
		if got := shellProfile(tc.shell); got != tc.want {
			t.Errorf("shellProfile(%q) = %q, want %q", tc.shell, got, tc.want)
		}
	}
}

// TestMotdIsCheckable: the greeting is a claim about trust, so it has to be the
// kind of claim the lab makes — checkable, not asserted. It names the vector and
// tells the reader not to believe it.
func TestMotdIsCheckable(t *testing.T) {
	text := mcpwrapper.Motd
	if !strings.Contains(text, "Don't Panic") {
		t.Fatalf("the MOTD does not greet: %q", text)
	}
	for _, want := range []string{"isotope/testdata/burned", "zaphod-beeblebrox", "don't believe it"} {
		if !strings.Contains(text, want) {
			t.Errorf("the MOTD does not mention %q: %q", want, text)
		}
	}
}

// TestMotdIsListedInUsage keeps the human path and the agent path in step: a
// command that prints the greeting is useless if nothing tells anyone it exists.
func TestMotdIsListedInUsage(t *testing.T) {
	doc, ok := lookupCommand("motd")
	if !ok {
		t.Fatal("no `lezz motd` command")
	}
	if !doc.listed {
		t.Fatal("`lezz motd` is not listed in usage")
	}
}
