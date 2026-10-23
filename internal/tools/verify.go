package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// developerTeamID is the Apple team that signs the lab's releases.
const developerTeamID = "288ZTJGXX9"

// commandRunner runs a command and returns its combined output. It exists so
// the signature check can be tested without codesign or a real binary.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: fixed binaries, argv-only, no shell.
}

// verifyBinarySignature checks the Apple signature of an installed binary.
//
// A checksum proves the bytes match the release; it cannot prove who built the
// release, because the checksums file comes from the same origin as the
// archive. The signature is the part that does, so the one thing a download
// must never install is a binary signed by a different team.
//
// Ad-hoc binaries are accepted with a warning: none of the managed tools sign
// their darwin releases yet, and refusing them would make `lezz install` fail
// for every tool. The warning names the gap rather than hiding it. When the
// tools publish Developer ID builds, this becomes a hard requirement by
// deleting the ad-hoc branch — not by changing the caller.
func verifyBinarySignature(ctx context.Context, goos string, run commandRunner, path string, warn func(string)) error {
	if goos != "darwin" {
		return nil
	}

	out, err := run(ctx, "codesign", "-dv", "--verbose=4", path)
	if err != nil {
		// An unsigned Mach-O is a real state, not a failure to read one.
		if strings.Contains(string(out), "not signed at all") {
			warn(fmt.Sprintf("%s is unsigned; its checksum is all that was verified", path))
			return nil
		}
		return fmt.Errorf("cannot read the code signature of %s: %w", path, err)
	}

	var authority, team string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "Authority=") && authority == "":
			authority = strings.TrimPrefix(line, "Authority=")
		case strings.HasPrefix(line, "TeamIdentifier="):
			team = strings.TrimPrefix(line, "TeamIdentifier=")
		}
	}

	switch {
	case authority == "":
		warn(fmt.Sprintf("%s has no Developer ID signature (ad-hoc); its checksum is all that was verified", path))
		return nil
	case !strings.HasPrefix(authority, "Developer ID Application:"):
		return fmt.Errorf("%s is signed by %q, not a Developer ID Application certificate", path, authority)
	case team != developerTeamID:
		return fmt.Errorf("%s is signed by team %q, not %s — refusing to install a binary from another signing identity", path, team, developerTeamID)
	default:
		return nil
	}
}

// defaultWarn writes a verification warning to stderr.
func defaultWarn(msg string) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
}

// verifyInstalledBinary is verifyBinarySignature for this host.
func verifyInstalledBinary(ctx context.Context, path string, warn func(string)) error {
	return verifyBinarySignature(ctx, runtime.GOOS, execCommand, path, warn)
}
