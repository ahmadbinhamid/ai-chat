package builderintelligence_test

import (
	"testing"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

func TestPolicy_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prompt   string
		wantCall bool
	}{
		{"change button color to blue", false},
		{"can you register the blog page", false},
		{"create 2 blog pages", false},
		{"make the site better", true},
		{"change blogs for a software house but keep JPRO meta titles", true},
		{"make the blog more professional for SaaS customers and don't change its slug", true},
		{"change the blog title but leave its meta description and slug alone", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.prompt, func(t *testing.T) {
			t.Parallel()
			plan, err := builderplan.BuildPlan(tc.prompt)
			if err != nil {
				t.Fatal(err)
			}
			dec := builderintelligence.ShouldCall(plan)
			if dec.Call != tc.wantCall {
				t.Fatalf("call=%v reason=%s wantCall=%v", dec.Call, dec.Reason, tc.wantCall)
			}
		})
	}
}
