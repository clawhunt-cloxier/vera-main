// Package modelguard rejects request switches that could delegate data
// acquisition to a model rather than the registered data service.
package modelguard

import (
	"errors"
	"strings"
)

// ValidateExtraBody rejects tool invocation controls, including the legacy
// function-calling fields and server-side search options. It does not inspect
// or log their values, which may contain untrusted text or secrets.
func ValidateExtraBody(extra map[string]any) error {
	for name := range extra {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "tools", "tool_choice", "parallel_tool_calls", "functions", "function_call", "web_search_options":
			return errors.New("model tools and search are not permitted")
		}
	}
	return nil
}
