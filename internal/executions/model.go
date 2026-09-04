// Package executions defines the execution (job) model.
// Executions are always scoped to a tenant_id; queries must filter by it.
package executions

import "time"

// Status is the lifecycle state of an execution.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusTimeout   Status = "timeout"
)

// Execution is one code submission and its result.
type Execution struct {
	ID         string
	TenantID   string
	Language   string
	Status     Status
	Source     string
	Stdin      string
	Stdout     string
	Stderr     string
	ExitCode   *int
	TimeoutS   int    // 1-60, default 2; validated at the gateway
	WebhookURL string // optional, http(s) only
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SupportedLanguages is the allowlist. Python only for now;
// other languages arrive later (deferred: <lang>-runner).
var SupportedLanguages = []string{"python"}

// ValidLanguage reports whether lang is supported.
func ValidLanguage(lang string) bool {
	for _, l := range SupportedLanguages {
		if l == lang {
			return true
		}
	}
	return false
}
