// Package webhooks holds webhook helpers.
// Scaffold: SSRF guard port from legacy src/utils/urlSafety.ts (deferred: ssrf-guard).
// This stub documents the contract so gateway handlers can reference it.
package webhooks

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateCallbackURL enforces the scaffold contract: http(s) only.
// Private/loopback/link-local/metadata blocking is deferred: ssrf-guard (see docs/api-parity.md).
func ValidateCallbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL must be http(s)")
	}
	if u.Host == "" || strings.Contains(u.Host, " ") {
		return fmt.Errorf("webhook URL has invalid host")
	}
	return nil
}
