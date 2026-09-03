package nats

import "testing"

func TestSubjects(t *testing.T) {
	if got := SubjectForSubmit("python"); got != "exec.submit.python" {
		t.Fatalf("submit subject = %q", got)
	}
	if got := SubjectForResult("abc"); got != "exec.result.abc" {
		t.Fatalf("result subject = %q", got)
	}
}
