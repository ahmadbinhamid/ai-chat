package auth

// Identity is the per-request identity Middleware resolves for one tenant; built fresh
// every request, never itself cached.
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

// superAdminRole ships with an empty Permissions slice; always use Can, never a raw
// contains-check against Permissions, or it's wrongly denied every action.
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
