package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin/binding"
)

func TestSendMessageRequest_PreviewErrorsBinding(t *testing.T) {
	longMessage := strings.Repeat("x", 2000)
	tests := []struct {
		name    string
		errors  string
		wantErr bool
	}{
		{"field absent", ``, false},
		{"valid list", `[{"type":"error","message":"boom","source":"js/minicart.js","line":42,"column":17,"count":1}]`, false},
		{"every contract type", `[{"type":"error","message":"a","count":1},{"type":"rejection","message":"b","count":1},
			{"type":"console","message":"c","count":1},{"type":"resource","message":"d","count":1},{"type":"render","message":"e","count":1}]`, false},
		{"over-long message accepted, truncated later", `[{"type":"console","message":"` + longMessage + `","count":1}]`, false},
		{"unknown type rejected", `[{"type":"warning","message":"x","count":1}]`, true},
		{"missing message rejected", `[{"type":"error","count":1}]`, true},
		{"negative line rejected", `[{"type":"error","message":"x","line":-1,"count":1}]`, true},
		{"more than 20 rejected", `[` + strings.TrimSuffix(strings.Repeat(`{"type":"error","message":"x","count":1},`, 21), ",") + `]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"theme_slug":"shop","prompt":"fix the cart"`
			if tt.errors != "" {
				body += `,"preview_errors":` + tt.errors
			}
			body += `}`
			var req sendMessageRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			err := binding.Validator.ValidateStruct(&req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// A bad preview context must never fail the send; the service drops it instead.
func TestSendMessageRequest_PreviewContextNeverBlocksSending(t *testing.T) {
	tests := []struct {
		name      string
		fields    string
		wantRoute *string
		wantFocus string
	}{
		{"absent", ``, nil, ""},
		{"home route", `,"preview_route":""`, strPtrForTest(""), ""},
		{"route and focus", `,"preview_route":"shop","focus_file":"components/header.liquid"`, strPtrForTest("shop"), "components/header.liquid"},
		{"traversal focus still binds", `,"focus_file":"../../etc/passwd"`, nil, "../../etc/passwd"},
		{"huge route still binds", `,"preview_route":"` + strings.Repeat("a", 5000) + `"`, strPtrForTest(strings.Repeat("a", 5000)), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req sendMessageRequest
			if err := json.Unmarshal([]byte(`{"theme_slug":"shop","prompt":"fix the cart"`+tt.fields+`}`), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := binding.Validator.ValidateStruct(&req); err != nil {
				t.Fatalf("preview context failed validation: %v", err)
			}
			if (req.PreviewRoute == nil) != (tt.wantRoute == nil) || (req.PreviewRoute != nil && *req.PreviewRoute != *tt.wantRoute) {
				t.Errorf("PreviewRoute = %v, want %v", req.PreviewRoute, tt.wantRoute)
			}
			if req.FocusFile != tt.wantFocus {
				t.Errorf("FocusFile = %q, want %q", req.FocusFile, tt.wantFocus)
			}
		})
	}
}

func strPtrForTest(s string) *string { return &s }
