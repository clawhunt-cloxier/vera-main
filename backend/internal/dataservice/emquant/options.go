package emquant

import (
	"errors"
	"strings"
)

// validateOptions rejects SDK modes that turn missing observations into
// synthetic values, and prevents passing credentials as query options.
// Further per-dataset allowlists and methodology checks belong to mappings.
func validateOptions(options string) error {
	seen := make(map[string]bool)
	for _, part := range strings.Split(options, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name, value = strings.ToUpper(strings.TrimSpace(name)), strings.TrimSpace(value)
		if !ok || name == "" || value == "" || strings.ContainsAny(name, "\x00 \t\r\n;=") || strings.ContainsAny(value, "\x00\r\n;=") || seen[name] {
			return errors.New("emquant: invalid or duplicate query option")
		}
		seen[name] = true
		switch name {
		case "SHOWBLANK", "USERNAME", "PASSWORD", "API_KEY", "APIKEY":
			return errors.New("emquant: prohibited query option")
		case "FILLDATA":
			if value != "0" {
				return errors.New("emquant: data carry-forward is prohibited")
			}
		}
	}
	return nil
}
