package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/creativeprojects/go-selfupdate"
)

// ---- checksum wiring --------------------------------------------------------

func TestChecksumConfigRequiresChecksums(t *testing.T) {
	cfg := checksumConfig()
	v, ok := cfg.Validator.(*selfupdate.ChecksumValidator)
	if !ok {
		t.Fatalf("validator is %T, want *selfupdate.ChecksumValidator", cfg.Validator)
	}
	if v.UniqueFilename != "checksums.txt" {
		t.Fatalf("looks for %q, want checksums.txt (the file goreleaser writes)", v.UniqueFilename)
	}
}

func TestInstallAndUpgradePassTheChecksumValidator(t *testing.T) {
	// The fetcher is what carries the config into go-selfupdate; a zero Config
	// verifies nothing at all.
	f := &ghReleaseFetcher{slug: "james-gibson/adhd", config: checksumConfig()}
	if _, ok := f.config.Validator.(*selfupdate.ChecksumValidator); !ok {
		t.Fatalf("fetcher validator is %T", f.config.Validator)
	}
}

// ---- source-install gate ----------------------------------------------------

func TestSourceInstallIsOffByDefault(t *testing.T) {
	t.Setenv("LEZZ_ALLOW_SOURCE_INSTALL", "")
	if sourceInstallAllowed() {
		t.Fatal("source install allowed without the opt-in")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", " true "} {
		t.Setenv("LEZZ_ALLOW_SOURCE_INSTALL", v)
		if !sourceInstallAllowed() {
			t.Fatalf("%q did not enable the source install", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "on"} {
		t.Setenv("LEZZ_ALLOW_SOURCE_INSTALL", v)
		if sourceInstallAllowed() {
			t.Fatalf("%q enabled the source install", v)
		}
	}
}

func TestInstallWithoutAssetDoesNotBuildFromSourceByDefault(t *testing.T) {
	t.Setenv("LEZZ_ALLOW_SOURCE_INSTALL", "")

	binDir := t.TempDir()
	fetcher := noAssetFetcher{}
	_, err := installInto(context.Background(), fetcher, Tool{Name: "adhd", GithubSlug: "james-gibson/adhd"}, binDir)
	if err == nil {
		t.Fatal("install succeeded with no release asset")
	}
	if !errors.Is(err, ErrNoReleaseAsset) {
		t.Fatalf("error is %v, want ErrNoReleaseAsset", err)
	}
	if !strings.Contains(err.Error(), "LEZZ_ALLOW_SOURCE_INSTALL") {
		t.Fatalf("error does not name the opt-in: %v", err)
	}
}

type noAssetFetcher struct{}

func (noAssetFetcher) FetchLatest(context.Context, string) (string, error) {
	return "", ErrNoReleaseAsset
}

// ---- signature policy -------------------------------------------------------

func TestVerifyBinarySignature(t *testing.T) {
	const path = "/tmp/adhd"

	cases := []struct {
		name     string
		goos     string
		output   string
		runErr   error
		wantErr  string
		wantWarn bool
	}{
		{
			name:   "our team",
			goos:   "darwin",
			output: "Authority=Developer ID Application: James Gibson (288ZTJGXX9)\nTeamIdentifier=288ZTJGXX9\n",
		},
		{
			name:     "ad-hoc",
			goos:     "darwin",
			output:   "Identifier=a.out\nCodeDirectory v=20400 flags=0x20002(adhoc,linker-signed)\n",
			wantWarn: true,
		},
		{
			name:     "unsigned",
			goos:     "darwin",
			output:   "code object is not signed at all\nIn subcomponent: /tmp/adhd\n",
			runErr:   errors.New("exit 1"),
			wantWarn: true,
		},
		{
			name:    "another developer id team",
			goos:    "darwin",
			output:  "Authority=Developer ID Application: Someone Else (ABCDE12345)\nTeamIdentifier=ABCDE12345\n",
			wantErr: "another signing identity",
		},
		{
			name:    "development certificate",
			goos:    "darwin",
			output:  "Authority=Apple Development: James Gibson (UGF39K45WD)\nTeamIdentifier=288ZTJGXX9\n",
			wantErr: "not a Developer ID Application",
		},
		{
			name:    "codesign unavailable",
			goos:    "darwin",
			output:  "exec: codesign: executable file not found",
			runErr:  errors.New("exit 1"),
			wantErr: "cannot read the code signature",
		},
		{
			name:   "not darwin",
			goos:   "linux",
			output: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var warned []string
			run := func(context.Context, string, ...string) ([]byte, error) {
				if tc.goos != "darwin" {
					t.Error("codesign was invoked on a non-darwin host")
				}
				return []byte(tc.output), tc.runErr
			}
			err := verifyBinarySignature(context.Background(), tc.goos, run, path, func(msg string) {
				warned = append(warned, msg)
			})

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantWarn != (len(warned) > 0) {
				t.Fatalf("warned=%v, want %v (%v)", warned, tc.wantWarn, warned)
			}
			if tc.wantWarn && !strings.Contains(warned[0], path) {
				t.Fatalf("warning does not name the binary: %q", warned[0])
			}
		})
	}
}
