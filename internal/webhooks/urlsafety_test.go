package webhooks

import "testing"

func TestValidateCallbackURL(t *testing.T) {
	if err := ValidateCallbackURL("https://example.com/hook"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCallbackURL("ftp://example.com/x"); err == nil {
		t.Fatal("expected non-http(s) rejection")
	}
}
