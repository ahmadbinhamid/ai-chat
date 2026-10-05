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
