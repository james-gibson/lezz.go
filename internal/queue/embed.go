package queue

import (
	"embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

// configFS holds the checked-in queue definitions lezz owns. Operators can
// point --config at another file to override.
//
//go:embed configs/*.yaml
var configFS embed.FS

// DefaultConfig returns the embedded GitHub queue definition.
func DefaultConfig() (Config, error) {
	data, err := configFS.ReadFile("configs/github.yaml")
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("embedded queue config: %w", err)
	}
	return c, nil
}
