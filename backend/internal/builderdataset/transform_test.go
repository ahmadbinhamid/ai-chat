package builderdataset

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/builderexamples"
)

func sampleSources() []builderexamples.Example {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	return []builderexamples.Example{
		{
			ID: "1", TenantID: 7, ChatID: "c1", GenerationID: "g1",
			PromptFingerprint: builderexamples.Fingerprint("can you register the blog page"),
			PromptSanitized:   "can you register the blog page",
			Intent:            "navigation_registry", Complexity: "medium",
			Operations: []string{"register_existing_page"},
			Targets:    []string{"pages/blog.liquid"},
			Execution:  builderexamples.ExecutionSnapshot{DeterministicOperation: "register_existing_page", DeepSeekUsed: false},
			Outcome:    builderexamples.OutcomeSnapshot{Success: true, ValidationPassed: true, TrainingPositive: true, Category: builderexamples.OutcomeLocalOperationSuccess, FinalStatus: "succeeded"},
			CreatedAt:  now,
		},
		{
			ID: "2", TenantID: 7,
			PromptFingerprint: builderexamples.Fingerprint("change the blogs according to software house and keep the JPRO meta titles"),
			PromptSanitized:   "change the blogs according to software house and keep the JPRO meta titles",
			Intent:            "compound", Complexity: "high", Compound: true,
			Operations:      []string{"update_page_content", "update_seo_meta"},
			ProtectedFields: []string{"jpro_meta_title"},
			Preferences:     []string{"software house audience"},
			Constraints:     []string{"preserve existing JPRO meta titles"},
			Execution:       builderexamples.ExecutionSnapshot{DeepSeekUsed: true, LocalLMUsed: true},
			Outcome:         builderexamples.OutcomeSnapshot{Success: true, ValidationPassed: true, TrainingPositive: true, Category: builderexamples.OutcomeSuccess},
			CreatedAt:       now.Add(time.Second),
		},
		{
			ID: "2b", TenantID: 7, // duplicate semantic of #2
			PromptFingerprint: builderexamples.Fingerprint("change the blogs according to software house and keep the JPRO meta titles"),
			PromptSanitized:   "change the blogs according to software house and keep the JPRO meta titles",
			Intent:            "compound", Complexity: "high", Compound: true,
			Operations:      []string{"update_seo_meta", "update_page_content"},
			ProtectedFields: []string{"meta_title"},
			Preferences:     []string{"software_house_audience"},
			Constraints:     []string{"protect_field:meta_title", "preference:software_house_audience"},
			Execution:       builderexamples.ExecutionSnapshot{DeepSeekUsed: true, LocalLMUsed: true},
			Outcome:         builderexamples.OutcomeSnapshot{Success: true, ValidationPassed: true, TrainingPositive: true, Category: builderexamples.OutcomeSuccess},
			CreatedAt:       now.Add(2 * time.Second),
		},
		{
			ID: "3", TenantID: 7,
			PromptFingerprint: builderexamples.Fingerprint("change the header button color to blue"),
			PromptSanitized:   "change the header button color to blue",
			Intent:            "simple_edit", Complexity: "low",
			Operations: []string{"simple_style_edit"},
			Execution:  builderexamples.ExecutionSnapshot{DeepSeekUsed: true},
			Outcome:    builderexamples.OutcomeSnapshot{Success: true, ValidationPassed: true, TrainingPositive: true, Category: builderexamples.OutcomeSuccess},
			CreatedAt:  now.Add(3 * time.Second),
		},
		{
			ID: "4", TenantID: 7,
			PromptFingerprint: builderexamples.Fingerprint("make the site better"),
			PromptSanitized:   "make the site better",
			Intent:            "ambiguous", Complexity: "low",
			Operations: []string{"clarify"},
			Outcome:    builderexamples.OutcomeSnapshot{Success: true, Category: builderexamples.OutcomeAmbiguous, FinalStatus: "succeeded"},
			CreatedAt:  now.Add(4 * time.Second),
		},
		{
			ID: "5", TenantID: 7,
			PromptFingerprint: builderexamples.Fingerprint("create 2 blog pages"),
			PromptSanitized:   "create 2 blog pages",
			Intent:            "compound", Complexity: "high", Compound: true,
			Operations: []string{"create_page", "register_page"},
			Execution:  builderexamples.ExecutionSnapshot{DeepSeekUsed: true},
			Outcome:    builderexamples.OutcomeSnapshot{Failed: true, Category: builderexamples.OutcomeProviderTimeout, FinalStatus: "failed"},
			CreatedAt:  now.Add(5 * time.Second),
		},
		{
			ID: "6", TenantID: 7,
			PromptFingerprint: builderexamples.Fingerprint("secret sk-abcdefghijklmnop change blog"),
			PromptSanitized:   "secret sk-abcdefghijklmnop change blog",
			Intent:            "seo_meta",
			Operations:        []string{"update_seo_meta"},
			Outcome:           builderexamples.OutcomeSnapshot{Success: true, TrainingPositive: true, ValidationPassed: true, Category: builderexamples.OutcomeSuccess},
			CreatedAt:         now.Add(6 * time.Second),
		},
	}
}

func TestTransform_LabelsAndDedupe(t *testing.T) {
	t.Parallel()
	res := Transform(sampleSources())
	if res.Report.TotalSource != 7 {
		t.Fatalf("total=%d", res.Report.TotalSource)
	}
	if res.Report.DuplicateCollapsed < 1 {
		t.Fatalf("expected duplicate collapse, got %d", res.Report.DuplicateCollapsed)
	}
	if res.Report.PositiveSemantic < 1 {
		t.Fatalf("expected semantic positive, got %d", res.Report.PositiveSemantic)
	}
	if res.Report.PositiveRouting < 1 {
		t.Fatalf("expected routing positive")
	}
	if res.Report.AmbiguousEval < 1 || res.Report.NegativeEval < 1 {
		t.Fatalf("ambig=%d neg=%d", res.Report.AmbiguousEval, res.Report.NegativeEval)
	}
	// Sensitive residual excluded
	foundSensitive := false
	for _, e := range res.Excluded {
		if e.Reason == ExcludeSensitiveResidual {
			foundSensitive = true
		}
	}
	if !foundSensitive {
		t.Fatal("expected sensitive exclusion")
	}
	// No tenant IDs in training rows
	all := append(append(append([]TrainingExample{}, res.Train...), res.Validation...), res.Test...)
	for _, te := range all {
		b, _ := jsonMarshal(te)
		if strings.Contains(string(b), `"tenant_id"`) || strings.Contains(string(b), `"chat_id"`) {
			t.Fatalf("production id leaked: %s", b)
		}
		if te.DatasetVersion != DatasetVersion {
			t.Fatal(te.DatasetVersion)
		}
	}
}

func TestCanonicalProtected_JPRO(t *testing.T) {
	t.Parallel()
	if got := canonicalProtectedField("jpro_meta_titles"); got != "meta_title" {
		t.Fatalf("got %q", got)
	}
	cs := canonicalizeConstraints([]string{"don't change JPRO meta titles", "keep existing meta titles"}, nil, nil)
	joined := strings.Join(cs, ",")
	if !strings.Contains(joined, "protect_field:meta_title") {
		t.Fatalf("constraints=%v", cs)
	}
}

func TestSplit_NoLeakage(t *testing.T) {
	t.Parallel()
	res := Transform(sampleSources())
	keys := map[string]string{}
	check := func(rows []TrainingExample, split string) {
		for _, r := range rows {
			if prev, ok := keys[r.SemanticKey]; ok && prev != split {
				t.Fatalf("semantic key %s in both %s and %s", r.SemanticKey, prev, split)
			}
			keys[r.SemanticKey] = split
		}
	}
	check(res.Train, "train")
	check(res.Validation, "validation")
	check(res.Test, "test")
}

func TestTransform_Reproducible(t *testing.T) {
	t.Parallel()
	a := Transform(sampleSources())
	b := Transform(sampleSources())
	if a.Report.Usable != b.Report.Usable || a.Report.TrainCount != b.Report.TrainCount {
		t.Fatalf("non-deterministic report: %+v vs %+v", a.Report, b.Report)
	}
	if len(a.Train) > 0 && len(b.Train) > 0 && a.Train[0].SemanticKey != b.Train[0].SemanticKey {
		t.Fatal("non-deterministic train order")
	}
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
