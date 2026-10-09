package main

import (
	"testing"

	"ai-chat/internal/auth"
)

func TestCheckEvalUser(t *testing.T) {
	member := []auth.Tenant{{ID: 2}, {ID: 9}}
	tests := []struct {
		name    string
		user    auth.IntrospectResult
		wantErr bool
	}{
		{"active member", auth.IntrospectResult{UserID: 24, IsActive: true, Tenants: member}, false},
		{"not in the tenant", auth.IntrospectResult{UserID: 24, IsActive: true, Tenants: []auth.Tenant{{ID: 9}}}, true},
		{"inactive", auth.IntrospectResult{UserID: 24, IsActive: false, Tenants: member}, true},
		{"no user id", auth.IntrospectResult{IsActive: true, Tenants: member}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkEvalUser(&tt.user, 2); (err != nil) != tt.wantErr {
				t.Fatalf("checkEvalUser = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
