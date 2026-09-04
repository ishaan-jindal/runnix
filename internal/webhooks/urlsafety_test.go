package webhooks

import (
	"context"
	"fmt"
	"net"
	"testing"
)

// stubResolver replaces DNS resolution with a fixed host table.
func stubResolver(t *testing.T, hosts map[string][]net.IPAddr) {
	t.Helper()
	orig := lookupIP
	lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := hosts[host]
		if !ok {
			return nil, fmt.Errorf("no such host %q", host)
		}
		return ips, nil
	}
	t.Cleanup(func() { lookupIP = orig })
}

func TestValidateCallbackURLAcceptsPublic(t *testing.T) {
	ctx := context.Background()
	stubResolver(t, map[string][]net.IPAddr{
		"example.com": {net.IPAddr{IP: net.ParseIP("93.184.216.34")}},
	})
	for _, raw := range []string{
		"https://example.com/hook",
		"http://example.com/hook",
	} {
		if err := ValidateCallbackURL(ctx, raw, Options{}); err != nil {
			t.Errorf("ValidateCallbackURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestValidateCallbackURLAcceptsPublicLiteral(t *testing.T) {
	// Literal IPs are checked without DNS, so no stub resolver here.
	if err := ValidateCallbackURL(context.Background(), "https://93.184.216.34/hook", Options{}); err != nil {
		t.Fatalf("ValidateCallbackURL = %v, want nil", err)
	}
}

func TestValidateCallbackURLRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{
		"ftp://example.com/x",   // not http(s)
		"https://",              // empty host
		"https://ex ample.com/", // space in host
		"://nope",
	} {
		if err := ValidateCallbackURL(ctx, raw, Options{}); err == nil {
			t.Errorf("ValidateCallbackURL(%q) = nil, want error", raw)
		}
	}
}

func TestValidateCallbackURLRejectsPrivateLiteral(t *testing.T) {
	ctx := context.Background()
	// Literal IPs are checked without DNS, so no stub resolver here.
	for _, raw := range []string{
		"http://127.0.0.1:9000/hook",    // loopback
		"http://[::1]/hook",             // IPv6 loopback
		"http://10.0.0.5/hook",          // RFC1918
		"http://192.168.1.1/hook",       // RFC1918
		"http://172.16.0.9/hook",        // RFC1918
		"http://169.254.169.254/latest", // cloud metadata (link-local)
		"http://0.0.0.0/hook",           // unspecified
		"http://fd00::1/hook",           // IPv6 ULA
	} {
		if err := ValidateCallbackURL(ctx, raw, Options{}); err == nil {
			t.Errorf("ValidateCallbackURL(%q) = nil, want error", raw)
		}
	}
}

func TestValidateCallbackURLRejectsPrivateDNS(t *testing.T) {
	ctx := context.Background()
	stubResolver(t, map[string][]net.IPAddr{
		"internal.corp": {net.IPAddr{IP: net.ParseIP("192.168.0.10")}},
		"mixed.example": {
			net.IPAddr{IP: net.ParseIP("93.184.216.34")}, // public first…
			net.IPAddr{IP: net.ParseIP("10.1.2.3")},      // …private second
		},
		"dead.example": {}, // resolves to nothing
	})
	for _, raw := range []string{
		"http://internal.corp/hook",
		"https://mixed.example/hook",
		"http://dead.example/hook",
		"http://never-resolves.example/hook", // missing from the table
		"http://localhost:8080/hook",
		"http://metadata.localhost/hook",
	} {
		if err := ValidateCallbackURL(ctx, raw, Options{}); err == nil {
			t.Errorf("ValidateCallbackURL(%q) = nil, want error", raw)
		}
	}
}

func TestValidateCallbackURLAllowPrivateBypass(t *testing.T) {
	ctx := context.Background()
	if err := ValidateCallbackURL(ctx, "http://127.0.0.1:9000/hook", Options{AllowPrivate: true}); err != nil {
		t.Fatalf("AllowPrivate loopback = %v, want nil", err)
	}
}
