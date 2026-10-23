package selfupdate

import (
	"testing"

	"github.com/creativeprojects/go-selfupdate"
)

// A zero Config made go-selfupdate skip validation entirely
// (update.go: `if up.validator != nil`), so `lezz update` replaced the running
// binary with bytes nothing had checked. The updater must always carry the
// release's checksums.txt.
func TestUpdaterConfigRequiresChecksums(t *testing.T) {
	cfg := updaterConfig()
	v, ok := cfg.Validator.(*selfupdate.ChecksumValidator)
	if !ok {
		t.Fatalf("validator is %T, want *selfupdate.ChecksumValidator", cfg.Validator)
	}
	if v.UniqueFilename != "checksums.txt" {
		t.Fatalf("looks for %q, want checksums.txt (the file goreleaser writes)", v.UniqueFilename)
	}
}
