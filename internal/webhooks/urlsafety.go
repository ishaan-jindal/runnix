// Package webhooks holds webhook URL safety, signing, and delivery.
//
// Submit-time URL validation lives here (used by the gateway handlers) and
// delivery-time re-validation (used by the dispatcher's Deliverer), so a
// callback URL is checked against the SSRF blocklist both when accepted and
// when used.
package webhooks

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Options tunes callback-URL validation.
type Options struct {
	// AllowPrivate disables the SSRF blocklist so development and tests can
	// target loopback receivers. Production must leave it false.
	AllowPrivate bool
}

// lookupIP is the DNS resolver used for validation, swappable in tests.
var lookupIP = net.DefaultResolver.LookupIPAddr

// ValidateCallbackURL enforces the callback-URL contract: http(s) only, and
// (unless AllowPrivate) a host that resolves only to public addresses so
// webhooks cannot probe loopback, private ranges, or cloud metadata.
func ValidateCallbackURL(ctx context.Context, raw string, opts Options) error {
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
	if opts.AllowPrivate {
		return nil
	}
	return checkPublicHost(ctx, u.Hostname())
}

func checkPublicHost(ctx context.Context, host string) error {
	lower := strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("webhook URL must not target localhost")
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return fmt.Errorf("webhook host %q does not resolve: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("webhook host %q does not resolve", host)
	}
	for _, ip := range ips {
		if !isPublic(ip.IP) {
			return fmt.Errorf("webhook host %q resolves to a non-public address (%s)", host, ip.IP)
		}
	}
	return nil
}

// isPublic reports whether ip is a public unicast address: not loopback,
// RFC1918/ULA private, link-local (which covers 169.254.169.254 metadata),
// multicast, or unspecified.
func isPublic(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}
