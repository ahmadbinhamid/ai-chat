package builderdataset

import (
	"strings"
	"testing"
	"time"

	"ai-chat/internal/builderexamples"
)

func TestBuildReadinessFromSource_NotReady(t *testing.T) {
	src := []builderexamples.Example{
		{
			ID: "1", TenantID: 1, PromptFingerprint: builderexamples.Fingerprint("change button color to blue"),
			Intent: "simple_edit", Operations: []string{"simple_style_edit"},
			Outcome: builderexamples.OutcomeSnapshot{
				Success: true, ValidationPassed: true, TrainingPositive: true,
				Category: builderexamples.OutcomeSuccess,
			},
			Execution: builderexamples.ExecutionSnapshot{DeepSeekUsed: true},
			CreatedAt: time.Now().UTC(),
		},
		{
			ID: "2", TenantID: 1, PromptFingerprint: builderexamples.Fingerprint("make the site better"),
			Intent: "ambiguous", Operations: []string{"clarify"},
			Outcome: builderexamples.OutcomeSnapshot{
				Success: true, Category: builderexamples.OutcomeAmbiguous,
			},
			CreatedAt: time.Now().UTC(),
		},
	}
	rep := BuildReadinessFromSource(src, "file", 0)
	if rep.GateReady || rep.GateStatus != "NOT_READY_FOR_TRAINING" {
		t.Fatalf("expected not ready: %+v", rep)
	}
	if rep.Usable < 1 {
		t.Fatalf("usable=%d", rep.Usable)
	}
	text := FormatReadinessText(rep)
	if !strings.Contains(text, "USABLE_TOTAL:") || !strings.Contains(text, "/ 500") {
		t.Fatalf("bad text:\n%s", text)
	}
	if strings.Contains(text, "button color") || strings.Contains(text, "make the site") {
		t.Fatal("raw prompt leaked in status text")
	}
	if len(rep.Underrepresented) == 0 {
		t.Fatal("expected underrepresented categories")
	}
}

func TestBuildSnapshot(t *testing.T) {
	src := sampleSources()
	result := Transform(src)
	rep := BuildReadinessFromSource(src, "file", 0)
	snap := BuildSnapshot(result, rep)
	if snap.DatasetVersion != DatasetVersion || snap.SourceCount != len(src) {
		t.Fatalf("%+v", snap)
	}
	if snap.GateStatus == "" {
		t.Fatal("missing gate status")
	}
	dir := t.TempDir()
	if err := WriteSnapshotJSON(dir, snap, &rep); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateStats_ExactExtras(t *testing.T) {
	fp := builderexamples.Fingerprint("identical request for software saas")
	src := []builderexamples.Example{
		{
			ID: "a", TenantID: 1, PromptFingerprint: fp, Intent: "simple_edit",
			Operations: []string{"update_page_content"},
			ProtectedFields: []string{"slug"}, Preferences: []string{"saas"},
			Constraints: []string{"make tone more professional"},
			Outcome: builderexamples.OutcomeSnapshot{
				Success: true, ValidationPassed: true, TrainingPositive: true,
				Category: builderexamples.OutcomeSuccess,
			},
			Execution: builderexamples.ExecutionSnapshot{LocalLMUsed: true, RefinementApplied: true},
			CreatedAt: time.Unix(1, 0).UTC(),
		},
		{
			ID: "b", TenantID: 1, PromptFingerprint: fp, Intent: "simple_edit",
			Operations: []string{"update_page_content"},
			ProtectedFields: []string{"slug"}, Preferences: []string{"saas"},
			Constraints: []string{"make tone more professional"},
			Outcome: builderexamples.OutcomeSnapshot{
				Success: true, ValidationPassed: true, TrainingPositive: true,
				Category: builderexamples.OutcomeSuccess,
			},
			Execution: builderexamples.ExecutionSnapshot{LocalLMUsed: true, RefinementApplied: true},
			CreatedAt: time.Unix(2, 0).UTC(),
		},
	}
	rep := BuildReadinessFromSource(src, "file", 0)
	if rep.Duplicates.ExactDuplicateExtraRows < 1 && rep.Duplicates.TransformCollapsed < 1 {
		t.Fatalf("expected duplicate detection: %+v", rep.Duplicates)
	}
}
