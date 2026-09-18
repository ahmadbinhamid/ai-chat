package genfail_test

import (
	"context"
	"errors"
	"testing"

	"ai-chat/internal/genfail"
)

func TestClassify_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		code genfail.Code
	}{
		{errors.New("incomplete multi-page create (need 2 pages): x"), genfail.CodeIncompleteMultiPage},
		{errors.New("proposal/tool contract mismatch: creating 2 pages in one shot requires a direct pages.json FULL-body update (page_registry_entry only registers one page)"), genfail.CodeProposalContractMismatch},
		{errors.New("invalid model proposal: bad path"), genfail.CodeValidationFailed},
		{errors.New("model did not call propose_changes within 6 tool-loop iterations"), genfail.CodeToolThrash},
		{context.Canceled, genfail.CodeCancelled},
		{context.DeadlineExceeded, genfail.CodeAIGenerationTimeout},
		{errors.New("Generation timed out after 10 minutes. Create page 1 of 2 completed successfully; remaining steps were not completed."), genfail.CodeAIGenerationTimeout},
		{errors.New("provider stream first-token timeout: no content"), genfail.CodeAIProviderFirstTokenTimeout},
		{errors.New("provider stream idle timeout: no progress"), genfail.CodeStreamIdleTimeout},
	}
	for _, tc := range cases {
		got := genfail.Classify(tc.err)
		if got.Code != tc.code {
			t.Fatalf("err=%v code=%s want %s", tc.err, got.Code, tc.code)
		}
		if got.Message == "" || got.Message == "Something went wrong while generating a response — please try again in a moment." {
			if tc.code != genfail.CodeUnknown {
				t.Fatalf("expected specific message for %s, got %q", tc.code, got.Message)
			}
		}
	}
}
