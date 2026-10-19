package main

import (
	"reflect"
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
