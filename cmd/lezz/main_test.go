package main

import (
	"bytes"
	"flag"
	"reflect"
	"strings"
	"testing"
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
	printCommandUsage(&buf, "demo", nil)
	out := buf.String()
	for _, want := range []string{
		"lezz demo — Launch a self-contained demo cluster",
		"Usage:\n  lezz demo\n",
		"Run 'lezz --help'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo usage is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Flags:") {
		t.Fatalf("demo takes no flags but printed a Flags section:\n%s", out)
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
