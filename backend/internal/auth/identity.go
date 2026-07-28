package auth

// Identity is the immutable, per-request identity Middleware resolves for
// one specific tenant — built fresh on every request from the (cached or
// live) introspection result, never itself cached (see CacheEntry).
type Identity struct {
	UserID      uint64
	Name        string
	Email       string
	TenantID    uint64
	TenantSlug  string
	RoleID      uint64
	RoleName    string
	Permissions []string
}

// superAdminRole ships with an empty Permissions slice — a naive
// contains-check against Permissions would deny it every action. Can is the
// only sanctioned way to check a permission for exactly this reason.
const superAdminRole = "super-admin"

// Can reports whether identity is allowed permission.
func Can(identity Identity, permission string) bool {
	if identity.RoleName == superAdminRole {
		return true
	}
	for _, p := range identity.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}
