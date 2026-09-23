package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("SecurePass123")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPassword(hash, "SecurePass123"); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if err := CheckPassword(hash, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestAccessTokenCarriesTenants(t *testing.T) {
	secret := "test-secret"
	tenants := []TenantClaim{{ID: "t1", Role: "owner"}, {ID: "t2", Role: "member"}}
	tok, err := SignAccessToken(secret, "u1", tenants)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseAccessToken(secret, tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "u1" || len(claims.TenantClaims) != 2 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRefreshTokenJtiRoundTrip(t *testing.T) {
	secret := "test-secret"
	tok, jti, err := SignRefreshToken(secret, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if jti == "" {
		t.Fatal("empty jti")
	}
	userID, parsedJTI, err := ParseRefreshToken(secret, tok)
	if err != nil {
		t.Fatal(err)
	}
	if userID != "u1" {
		t.Fatalf("unexpected user id: %q", userID)
	}
	if parsedJTI != jti {
		t.Fatalf("jti mismatch: got %q want %q", parsedJTI, jti)
	}
}

func TestRefreshTokenJtiUnique(t *testing.T) {
	secret := "test-secret"
	_, jti1, err := SignRefreshToken(secret, "u1")
	if err != nil {
		t.Fatal(err)
	}
	_, jti2, err := SignRefreshToken(secret, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if jti1 == jti2 {
		t.Fatal("expected fresh jti per token")
	}
}

func TestRefreshTokenPreJtiRejected(t *testing.T) {
	secret := "test-secret"
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   "u1",
		Audience:  jwt.ClaimStrings{RefreshAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseRefreshToken(secret, tok); err == nil {
		t.Fatal("pre-jti token accepted")
	}
}

func TestAPIKeyFormat(t *testing.T) {
	keyID, secret, full, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 10 || full[:8] != "sk_live_" {
		t.Fatalf("bad key format: %s", full)
	}
	if len(keyID) != 32 {
		t.Fatalf("bad key id length: got %d want 32", len(keyID))
	}
	for _, c := range keyID {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("key id not lowercase hex: %q", keyID)
		}
	}
	if len(secret) != 64 {
		t.Fatalf("bad secret length: got %d want 64", len(secret))
	}
}

func TestGenerateAPIKeyParses(t *testing.T) {
	keyID, secret, full, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(keyID) != 32 {
		t.Fatalf("bad key id length: got %d want 32", len(keyID))
	}
	parsedID, parsedSecret, err := ParseAPIKey(full)
	if err != nil {
		t.Fatal(err)
	}
	if parsedID != keyID || parsedSecret != secret {
		t.Fatalf("parse mismatch: got %q.%q want %q.%q", parsedID, parsedSecret, keyID, secret)
	}
}

func TestGenerateAPIKeyIDsUnique(t *testing.T) {
	id1, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	id2, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("expected unique key ids")
	}
}

func TestTokenTypeConfusionRejected(t *testing.T) {
	secret := "test-secret"
	accessTok, err := SignAccessToken(secret, "u1", []TenantClaim{{ID: "t1", Role: "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseRefreshToken(secret, accessTok); err == nil {
		t.Fatal("access token accepted as refresh token")
	}
	refreshTok, _, err := SignRefreshToken(secret, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(secret, refreshTok); err == nil {
		t.Fatal("refresh token accepted as access token")
	}
}

func TestParseAPIKey(t *testing.T) {
	keyID, secret, err := ParseAPIKey("sk_live_abc123.secretvalue")
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "abc123" || secret != "secretvalue" {
		t.Fatalf("unexpected parse: %q %q", keyID, secret)
	}

	// Split on first dot after prefix.
	keyID, secret, err = ParseAPIKey("sk_live_id.sec.ret")
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "id" || secret != "sec.ret" {
		t.Fatalf("unexpected first-dot split: %q %q", keyID, secret)
	}

	malformed := []string{
		"",
		"abc123.secret",
		"sk_live_",
		"sk_live_nodot",
		"sk_live_.secret",
		"sk_live_id.",
		"sk_live_.",
		"Bearer sk_live_id.secret",
	}
	for _, in := range malformed {
		if _, _, err := ParseAPIKey(in); err == nil {
			t.Fatalf("malformed input accepted: %q", in)
		}
	}
}

func TestEqualAPIKeyHash(t *testing.T) {
	a := HashAPIKeySecret("s3cret")
	b := HashAPIKeySecret("s3cret")
	if !EqualAPIKeyHash(a, b) {
		t.Fatal("equal hashes rejected")
	}
	if EqualAPIKeyHash(a, HashAPIKeySecret("other")) {
		t.Fatal("different hashes accepted")
	}
	if EqualAPIKeyHash(a, a+"00") {
		t.Fatal("length mismatch accepted")
	}
}
