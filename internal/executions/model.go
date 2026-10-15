// Package executions defines the execution (job) model.
// Executions are always scoped to a tenant_id; queries must filter by it.
package executions

import "slices"

// SupportedLanguages is the allowlist. Python only for now;
// other languages arrive later (deferred: <lang>-runner).
var SupportedLanguages = []string{"python"}

// ValidLanguage reports whether lang is supported.
func ValidLanguage(lang string) bool {
	return slices.Contains(SupportedLanguages, lang)
}
