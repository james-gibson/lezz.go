package sea

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// inspect proves that an app bundle is the dashboard, signed by our team, and
// distributable. It never trusts the manifest: the bundle is read, and the
// signature is asked directly.
func (e *env) inspect(ctx context.Context, appPath string, allowUnnotarized bool) (Info, error) {
	if _, err := os.Stat(appPath); err != nil { //nolint:gosec // G703: appPath is a bundle this process just installed or unpacked.
		return Info{}, fmt.Errorf("%s: %w", appPath, err)
	}

	info := Info{Path: appPath}
	identity, err := e.readPlist(ctx, filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return Info{}, err
	}
	info.BundleID = identity.BundleID
	info.Version = identity.ShortVersion
	info.Build = identity.Build
	if info.BundleID != BundleID {
		return Info{}, fmt.Errorf("bundle id is %q, expected %q", info.BundleID, BundleID)
	}

	// The signature is the trust anchor. --deep also covers nested code;
	// --strict enforces resource-rule correctness.
	if _, verifyErr := e.run(ctx, "codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath); verifyErr != nil {
		return Info{}, fmt.Errorf("codesign --verify failed: %w", verifyErr)
	}

	// codesign -dv writes its description to stderr, which the runner captures.
	detailsRaw, err := e.run(ctx, "codesign", "-dv", "--verbose=4", appPath)
	if err != nil {
		return Info{}, fmt.Errorf("codesign -dv failed: %w", err)
	}
	details := string(detailsRaw)
	for _, line := range strings.Split(details, "\n") {
		switch {
		case strings.HasPrefix(line, "Authority="):
			if info.Authority == "" {
				info.Authority = strings.TrimPrefix(line, "Authority=")
			}
		case strings.HasPrefix(line, "TeamIdentifier="):
			info.TeamID = strings.TrimPrefix(line, "TeamIdentifier=")
		case strings.HasPrefix(line, "Timestamp="):
			info.Timestamped = true
		case strings.Contains(line, "flags=") && strings.Contains(line, "(runtime)"):
			info.HardenedRuntime = true
		}
	}

	if info.Authority != Authority {
		return Info{}, fmt.Errorf("authority is %q, expected %q", info.Authority, Authority)
	}
	if info.TeamID != TeamID {
		return Info{}, fmt.Errorf("team id is %q, expected %q", info.TeamID, TeamID)
	}
	if !info.HardenedRuntime {
		return Info{}, fmt.Errorf("the hardened runtime flag is missing from the signature")
	}
	if !info.Timestamped {
		return Info{}, fmt.Errorf("the signature has no secure timestamp (built without archive + exportArchive?)")
	}

	// Notarization is a separate gate from signing, and a separate ticket. A
	// signed but un-notarized app is blocked by Gatekeeper on a downloaded
	// first launch, so installing one silently would just move the failure to
	// the user's first double-click.
	if err := e.checkNotarized(ctx, appPath); err != nil {
		if !allowUnnotarized {
			return Info{}, fmt.Errorf("%w — the published build is signed but not notarized, so macOS will block it; "+
				"publish a notarized build, or pass --allow-unnotarized to install this dev build anyway", err)
		}
		e.logf("warning: %v — installing anyway (--allow-unnotarized)", err)
	} else {
		info.Notarized = true
	}

	return info, nil
}

type plistIdentity struct {
	BundleID     string `json:"CFBundleIdentifier"`
	ShortVersion string `json:"CFBundleShortVersionString"`
	Build        string `json:"CFBundleVersion"`
}

func (e *env) readPlist(ctx context.Context, plistPath string) (plistIdentity, error) {
	raw, err := e.run(ctx, "plutil", "-convert", "json", "-o", "-", plistPath)
	if err != nil {
		return plistIdentity{}, fmt.Errorf("read %s: %w", plistPath, err)
	}
	var id plistIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return plistIdentity{}, fmt.Errorf("parse %s: %w", plistPath, err)
	}
	return id, nil
}

// checkNotarized asks the stapler for the ticket and Gatekeeper for its verdict.
// Both are certificate-free checks; neither proves who built the app, only that
// Apple accepted it.
func (e *env) checkNotarized(ctx context.Context, appPath string) error {
	if _, err := e.run(ctx, "xcrun", "stapler", "validate", appPath); err != nil {
		return fmt.Errorf("no stapled notarization ticket")
	}
	// spctl reports its verdict on stdout and its exit status does not
	// distinguish accept from reject, so the text is the result.
	out, err := e.run(ctx, "spctl", "--assess", "--type", "execute", "--verbose=2", appPath)
	text := string(out)
	if err != nil && text == "" {
		return fmt.Errorf("spctl could not assess the bundle: %w", err)
	}
	if !strings.Contains(text, "accepted") {
		return fmt.Errorf("gatekeeper rejects the bundle")
	}
	if !strings.Contains(text, "source=Notarized Developer ID") {
		return fmt.Errorf("gatekeeper does not see a notarized Developer ID")
	}
	return nil
}
