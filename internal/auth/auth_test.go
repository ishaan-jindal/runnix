package auth

import "testing"

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

func TestAPIKeyFormat(t *testing.T) {
	_, _, full, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 10 || full[:8] != "sk_live_" {
		t.Fatalf("bad key format: %s", full)
	}
}
