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
	ID        string
	TenantID  string
	Language  string
	Status    Status
	Source    string
	Stdin     string
	Stdout    string
	Stderr    string
	ExitCode  *int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SupportedLanguages is the scaffold allowlist; runner images arrive deferred: k8s-jobs.
var SupportedLanguages = []string{"python", "c", "java"}

// ValidLanguage reports whether lang is supported.
func ValidLanguage(lang string) bool {
	for _, l := range SupportedLanguages {
		if l == lang {
			return true
		}
	}
	return false
}
