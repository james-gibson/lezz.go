package selfupdate

import "testing"

func TestIsValidSemver(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		// clean release tags
		{"v0.1.25", true},
		{"v1.0.0", true},
		{"v2.3.4", true},
		{"0.1.25", true}, // without v prefix

		// dirty local builds
		{"v0.1.25-0.20260404234801-2a30d6a85e23+dirty", false},
		{"v0.1.25+dirty", false},

		// Go pseudo-versions
		{"v0.1.25-0.20260404234801-2a30d6a85e23", false},
		{"v0.0.0-20230101120000-abcdef123456", false},

		// unresolved dev builds
		{"dev", false},
		{"abcdef12", false},
		{"(devel)", false},
		{"", false},

		// valid pre-release semver (not pseudo-versions)
		{"v1.0.0-alpha.1", true},
		{"v1.0.0-rc.2", true},
	}

	for _, tc := range cases {
		got := isValidSemver(tc.v)
		if got != tc.want {
			t.Errorf("isValidSemver(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}
