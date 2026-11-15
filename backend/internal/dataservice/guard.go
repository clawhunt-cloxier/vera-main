package dataservice

import "fmt"

// GuardProduction returns an error if a synthetic (fixture) provider is registered
// and production mode is active. Call this during app startup before any sync.
func GuardProduction(p Provider, production bool) error {
	if production && p.Synthetic() {
		return fmt.Errorf("dataservice: fixture provider %q must not run in production mode", p.ID())
	}
	return nil
}
