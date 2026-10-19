package bridge

import "embed"

// configFS holds the checked-in bridge definitions lezz owns. Operators can
// override or add entries under ~/.lezz/lab/bridges without rebuilding.
//
//go:embed configs/*.yaml
var configFS embed.FS
