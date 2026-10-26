// Package executions defines the execution (job) model.
// Executions are always scoped to a tenant_id; queries must filter by it.
package executions

import "slices"

// SupportedLanguages is the allowlist served by the single polyglot sandbox
// image (runner/docker), which dispatches on the language ID.
var SupportedLanguages = []string{"python", "javascript", "go", "java", "ruby", "rust", "cpp"}

// ValidLanguage reports whether lang is supported.
func ValidLanguage(lang string) bool {
	return slices.Contains(SupportedLanguages, lang)
}
