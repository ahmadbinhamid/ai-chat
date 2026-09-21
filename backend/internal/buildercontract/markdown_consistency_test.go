package buildercontract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-chat/internal/buildercontract"
)

// Ensures the human MD documents every frozen vocabulary op (preference A:
// machine contract is canonical; MD must stay aligned).
func TestMarkdownDocumentsVocabulary(t *testing.T) {
	mdPath := filepath.Join("..", "..", "..", "AI_BUILDER_PAGE_LIFECYCLE.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Skipf("lifecycle MD not found at %s: %v", mdPath, err)
	}
	body := string(raw)
	if !strings.Contains(body, "contract_version = `1`") && !strings.Contains(body, "contract_version=1") && !strings.Contains(body, "**contract_version = `1`**") {
		t.Fatal("MD must declare contract_version=1")
	}
	for _, op := range buildercontract.CanonicalVocabulary() {
		needle := "`" + string(op) + "`"
		if !strings.Contains(body, needle) && !strings.Contains(body, string(op)) {
			t.Errorf("MD missing operation %q", op)
		}
	}
	for _, op := range buildercontract.NotImplementedOps() {
		if !strings.Contains(body, "CONTRACT_DEFINED_NOT_IMPLEMENTED") {
			t.Fatal("MD must mark CONTRACT_DEFINED_NOT_IMPLEMENTED")
		}
		if !strings.Contains(body, string(op)) {
			t.Errorf("MD missing not-implemented op %q", op)
		}
	}
}
