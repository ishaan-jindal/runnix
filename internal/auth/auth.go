// Package auth provides JWT, API-key, and password helpers.
// Stateless helpers only: persistence lives in the HTTP handlers.
// Refresh-token revocation lives in the store/handler layer.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
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

// AccessAudience marks tokens issued for API authorization.
const AccessAudience = "runnix-access"

// RefreshAudience marks tokens issued for session renewal.
const RefreshAudience = "runnix-refresh"

// SignAccessToken issues a 15-minute access token carrying tenant memberships.
func SignAccessToken(secret, userID string, tenants []TenantClaim) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantClaims: tenants,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Audience:  jwt.ClaimStrings{AccessAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// SignRefreshToken issues a 7-day refresh token (no tenant claims; re-resolved on refresh).
func SignRefreshToken(secret, userID string) (string, string, error) {
	now := time.Now()
	jti := uuid.NewString()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		Audience:  jwt.ClaimStrings{RefreshAudience},
		ID:        jti,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", "", err
	}
	return tok, jti, nil
}

// ParseAccessToken validates and returns access claims.
func ParseAccessToken(secret, token string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(token, claims, hmacKeyFunc(secret), jwt.WithAudience(AccessAudience))
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, fmt.Errorf("invalid access token")
	}
	return claims, nil
}

// ParseRefreshToken validates a refresh token and returns the user id and jti.
// Refresh tokens carry no tenant claims; memberships are re-resolved on refresh.
func ParseRefreshToken(secret, token string) (string, string, error) {
	claims := &jwt.RegisteredClaims{}
	tok, err := jwt.ParseWithClaims(token, claims, hmacKeyFunc(secret), jwt.WithAudience(RefreshAudience))
	if err != nil {
		return "", "", err
	}
	if !tok.Valid || claims.Subject == "" || claims.ID == "" {
		return "", "", fmt.Errorf("invalid refresh token")
	}
	return claims.Subject, claims.ID, nil
}

func hmacKeyFunc(secret string) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	}
}

// GenerateAPIKey returns (keyID, secret, fullKey). Only SHA-256 of the secret is stored.
func GenerateAPIKey() (keyID, secret, fullKey string, err error) {
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", "", err
	}
	keyID = hex.EncodeToString(idBytes)
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret = hex.EncodeToString(raw)
	fullKey = fmt.Sprintf("sk_live_%s.%s", keyID, secret)
	return keyID, secret, fullKey, nil
}

// HashAPIKeySecret returns the stored hash for an API-key secret.
func HashAPIKeySecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ParseAPIKey parses sk_live_<keyID>.<secret> into its parts.
func ParseAPIKey(full string) (string, string, error) {
	const prefix = "sk_live_"
	if !strings.HasPrefix(full, prefix) {
		return "", "", fmt.Errorf("invalid API key format")
	}
	rest := strings.TrimPrefix(full, prefix)
	idx := strings.Index(rest, ".")
	if idx < 0 {
		return "", "", fmt.Errorf("invalid API key format")
	}
	keyID, secret := rest[:idx], rest[idx+1:]
	if keyID == "" || secret == "" {
		return "", "", fmt.Errorf("invalid API key format")
	}
	return keyID, secret, nil
}

// EqualAPIKeyHash compares two API-key hashes in constant time.
func EqualAPIKeyHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
