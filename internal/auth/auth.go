// Package auth provides JWT, API-key, and password helpers.
// Scaffold: interfaces + stateless helpers only. Postgres persistence (deferred: auth-plus-postgres).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TenantClaim is one tenant membership embedded in the access token.
type TenantClaim struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// Claims is the Runnix access-token shape. Note: tenants, not a single tenant.
type Claims struct {
	TenantClaims []TenantClaim `json:"tenants"`
	jwt.RegisteredClaims
}

// HashPassword hashes with bcrypt (cost 12 matches legacy posture).
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword compares a bcrypt hash with a candidate password.
func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// SignAccessToken issues a 15-minute access token carrying tenant memberships.
func SignAccessToken(secret, userID string, tenants []TenantClaim) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantClaims: tenants,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// SignRefreshToken issues a 7-day refresh token (no tenant claims; re-resolved on refresh).
func SignRefreshToken(secret, userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseAccessToken validates and returns access claims.
func ParseAccessToken(secret, token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// GenerateAPIKey returns (keyID, secret, fullKey). Only SHA-256 of the secret is stored.
func GenerateAPIKey() (keyID, secret, fullKey string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	keyID = hex.EncodeToString(sum[:8])
	fullKey = fmt.Sprintf("sk_live_%s.%s", keyID, secret)
	return keyID, secret, fullKey, nil
}

// HashAPIKeySecret returns the stored hash for an API-key secret.
func HashAPIKeySecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
