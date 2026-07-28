package auth

import "testing"

func TestCan_SuperAdminWildcard(t *testing.T) {
	// Ships with an empty Permissions slice — a naive contains-check would
	// deny it every action. This is the case the task explicitly calls out.
	identity := Identity{RoleName: "super-admin", Permissions: []string{}}

	for _, permission := range []string{"dashboard.view", "orders.view", "anything.at.all"} {
		if !Can(identity, permission) {
			t.Errorf("expected super-admin to be allowed %q", permission)
		}
	}
}

func TestCan_CustomRolePermissions(t *testing.T) {
	identity := Identity{
		RoleName:    "second admin",
		Permissions: []string{"dashboard.view", "orders.view"},
	}

	for _, permission := range []string{"dashboard.view", "orders.view"} {
		if !Can(identity, permission) {
			t.Errorf("expected %q to be allowed", permission)
		}
	}
	for _, permission := range []string{"orders.delete", "billing.view"} {
		if Can(identity, permission) {
			t.Errorf("expected %q to be denied", permission)
		}
	}
}
