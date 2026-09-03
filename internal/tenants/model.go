// Package tenants defines the tenant model.
//
// Tenancy is many-to-many: a user may belong to many tenants via memberships.
// Every request carries exactly one tenant_id (X-Tenant-ID header); authz must
// verify membership and never assume user_id == tenant_id.
package tenants

import "time"

// Tier maps to future ResourceQuota/rate-limit values.
type Tier string

const (
	TierFree         Tier = "free"
	TierStarter      Tier = "starter"
	TierProfessional Tier = "professional"
	TierEnterprise   Tier = "enterprise"
)

// Tenant is an org mapped to a Kubernetes namespace (runnix-tenant-<id>).
type Tenant struct {
	ID        string
	Slug      string
	Namespace string
	Tier      Tier
	CreatedAt time.Time
}

// Role is the user's role within a tenant.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// Membership joins users to tenants.
type Membership struct {
	UserID   string
	TenantID string
	Role     Role
}
