package dispatcher

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
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

func TestLangCommands(t *testing.T) {
	cases := map[string]struct{ file, cmdPart string }{
		"python":     {"main.py", "exec python /work/main.py"},
		"javascript": {"main.js", "exec node /work/main.js"},
		"go":         {"main.go", "exec go run /work/main.go"},
		"java":       {"Main.java", "exec java /work/Main.java"},
		"ruby":       {"main.rb", "exec ruby /work/main.rb"},
		"rust":       {"main.rs", "rustc /work/main.rs -o /tmp/main"},
		"cpp":        {"main.cpp", "g++ /work/main.cpp -o /tmp/main"},
		"Python":     {"main.py", "exec python /work/main.py"},
	}
	if len(cases) != len(langCommands)+1 { // +1 for the mixed-case duplicate
		t.Fatalf("%d cases for %d languages, table drift", len(cases), len(langCommands))
	}
	for lang, want := range cases {
		t.Run(lang, func(t *testing.T) {
			spec, err := resolveLang(lang)
			if err != nil {
				t.Fatalf("resolveLang(%q) = %v", lang, err)
			}
			if spec.file != want.file {
				t.Fatalf("file = %q, want %q", spec.file, want.file)
			}
			if !strings.Contains(spec.cmd, want.cmdPart) {
				t.Fatalf("cmd = %q, want substring %q", spec.cmd, want.cmdPart)
			}
			if !strings.Contains(spec.cmd, "< /work/stdin.txt") {
				t.Fatalf("cmd = %q, missing stdin redirect", spec.cmd)
			}
			if strings.Count(spec.cmd, "'")%2 != 0 {
				t.Fatalf("cmd = %q, unbalanced single quotes", spec.cmd)
			}
		})
	}
}

func TestLangUnknown(t *testing.T) {
	if _, err := resolveLang("haskell"); !errors.Is(err, errNotStarted) {
		t.Fatalf("resolveLang(haskell) = %v, want errNotStarted", err)
	}
}

func TestWorkArchive(t *testing.T) {
	tr := tar.NewReader(bytes.NewReader(workArchive("print(1)", "hello", "main.rb")))
	got := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar entry %q: %v", hdr.Name, err)
		}
		got[hdr.Name] = string(b)
	}
	if got["main.rb"] != "print(1)" {
		t.Fatalf("source entry = %q, want print(1)", got["main.rb"])
	}
	if got["stdin.txt"] != "hello" {
		t.Fatalf("stdin entry = %q, want hello", got["stdin.txt"])
	}
	if len(got) != 2 {
		t.Fatalf("entries = %v, want 2", got)
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
