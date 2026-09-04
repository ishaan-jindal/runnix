package dispatcher

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	zero := 0
	one := 1
	for name, tc := range map[string]struct {
		res         RunResult
		status      string
		exitCode    *int
		wantStderr  string
		checkStderr func(string) bool
	}{
		"succeeded":       {RunResult{ExitCode: 0}, "succeeded", &zero, "", nil},
		"failed":          {RunResult{ExitCode: 1, Stderr: "boom"}, "failed", &one, "boom", nil},
		"timeout":         {RunResult{ExitCode: 137, TimedOut: true}, "timeout", nil, "", nil},
		"oom":             {RunResult{ExitCode: 137, OOMKilled: true}, "failed", nil, "", func(s string) bool { return strings.Contains(s, "out of memory") }},
		"oom with stderr": {RunResult{ExitCode: 137, OOMKilled: true, Stderr: "traceback"}, "failed", nil, "", func(s string) bool { return strings.Contains(s, "traceback") && strings.Contains(s, "out of memory") }},
	} {
		t.Run(name, func(t *testing.T) {
			status, code, stderr := classify(tc.res)
			if status != tc.status {
				t.Fatalf("status = %q, want %q", status, tc.status)
			}
			if (code == nil) != (tc.exitCode == nil) {
				t.Fatalf("exitCode = %v, want %v", code, tc.exitCode)
			}
			if code != nil && *code != *tc.exitCode {
				t.Fatalf("exitCode = %d, want %d", *code, *tc.exitCode)
			}
			if tc.checkStderr != nil && !tc.checkStderr(stderr) {
				t.Fatalf("stderr = %q", stderr)
			} else if tc.checkStderr == nil && tc.wantStderr != stderr {
				t.Fatalf("stderr = %q, want %q", stderr, tc.wantStderr)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	small := strings.Repeat("a", maxOutputBytes)
	if got := truncateOutput(small); got != small {
		t.Fatalf("small output mutated")
	}
	big := strings.Repeat("a", maxOutputBytes+10)
	got := truncateOutput(big)
	if len(got) > maxOutputBytes+80 {
		t.Fatalf("truncated output too large: %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}
