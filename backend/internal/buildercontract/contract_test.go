package buildercontract_test

import (
	"testing"

	"ai-chat/internal/buildercontract"
	"ai-chat/internal/builderplan"
)

func TestContractVersion(t *testing.T) {
	if buildercontract.ContractVersion != "1" {
		t.Fatalf("ContractVersion=%q want 1", buildercontract.ContractVersion)
	}
}

func TestVocabularyClosedAndUnique(t *testing.T) {
	seen := map[buildercontract.PageOperation]bool{}
	for _, r := range buildercontract.AllLifecycleRules() {
		if r.Operation == "" {
			t.Fatal("empty operation in rules")
		}
		if seen[r.Operation] {
			t.Fatalf("duplicate op %q", r.Operation)
		}
		seen[r.Operation] = true
		if r.Status == "" {
			t.Fatalf("%s missing status", r.Operation)
		}
		if r.Summary == "" {
			t.Fatalf("%s missing summary", r.Operation)
		}
	}
	if len(seen) < 15 {
		t.Fatalf("expected full vocabulary, got %d", len(seen))
	}
}

func TestNotImplementedMarked(t *testing.T) {
	want := map[buildercontract.PageOperation]bool{
		buildercontract.OpRegeneratePageContent: true,
		buildercontract.OpRecreatePage:          true,
		buildercontract.OpUnregisterPage:        true,
		buildercontract.OpRemoveFromNavigation:  true,
		buildercontract.OpDuplicatePage:         true,
	}
	for _, op := range buildercontract.NotImplementedOps() {
		if !want[op] {
			t.Fatalf("unexpected not-implemented %q", op)
		}
		delete(want, op)
	}
	for op := range want {
		t.Fatalf("missing not-implemented mark for %q", op)
	}
}

func TestBuilderPlanAllowedOpsSubsetOfContract(t *testing.T) {
	allowed := builderplan.DefaultAllowedOpKinds()
	if len(allowed) == 0 {
		t.Fatal("empty DefaultAllowedOpKinds")
	}
	for _, k := range allowed {
		op := buildercontract.PageOperation(k)
		rule, ok := buildercontract.RuleFor(op)
		if !ok {
			t.Fatalf("BuilderPlan op %q not in buildercontract vocabulary", k)
		}
		if rule.Status == buildercontract.StatusContractDefinedNotImplemented {
			t.Fatalf("BuilderPlan must not allow not-implemented op %q", k)
		}
		if rule.BuilderPlanOp != "" && rule.BuilderPlanOp != string(k) {
			t.Fatalf("mismatch BuilderPlanOp %q vs kind %q", rule.BuilderPlanOp, k)
		}
	}
}

func TestImplementedOpsHaveBuilderPlanWire(t *testing.T) {
	for _, r := range buildercontract.AllLifecycleRules() {
		if r.Status != buildercontract.StatusImplemented && r.Status != buildercontract.StatusPartial {
			continue
		}
		if r.BuilderPlanOp == "" {
			t.Fatalf("%s implemented but missing BuilderPlanOp", r.Operation)
		}
		found := false
		for _, k := range builderplan.DefaultAllowedOpKinds() {
			if string(k) == r.BuilderPlanOp {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s BuilderPlanOp %q not in DefaultAllowedOpKinds", r.Operation, r.BuilderPlanOp)
		}
	}
}

func TestRegistryAndMenuFieldsClosed(t *testing.T) {
	if len(buildercontract.RegistryFields()) != 14 {
		t.Fatalf("registry fields=%d want 14", len(buildercontract.RegistryFields()))
	}
	if len(buildercontract.MenuItemFields()) != 5 {
		t.Fatalf("menu fields=%d want 5", len(buildercontract.MenuItemFields()))
	}
	if len(buildercontract.RenderVariables()) < 15 {
		t.Fatalf("render vars too few: %d", len(buildercontract.RenderVariables()))
	}
}

func TestDeepSeekSlicesCoverKeys(t *testing.T) {
	keys := map[buildercontract.DeepSeekContextKey]bool{}
	for _, s := range buildercontract.DeepSeekSlices() {
		if len(s.Rules) == 0 {
			t.Fatalf("empty slice %q", s.Key)
		}
		keys[s.Key] = true
	}
	for _, want := range []buildercontract.DeepSeekContextKey{
		buildercontract.DeepSeekSliceCreate,
		buildercontract.DeepSeekSliceUpdate,
		buildercontract.DeepSeekSliceRegenerate,
		buildercontract.DeepSeekSliceRegister,
		buildercontract.DeepSeekSliceNavigation,
		buildercontract.DeepSeekSliceDiagnose,
		buildercontract.DeepSeekSliceFix,
		buildercontract.DeepSeekSliceSEO,
	} {
		if !keys[want] {
			t.Fatalf("missing DeepSeek slice %q", want)
		}
	}
}

func TestMLBoundaryNonEmpty(t *testing.T) {
	b := buildercontract.LocalMLBoundary()
	if len(b.MustNot) < 5 || len(b.MayAssist) < 3 {
		t.Fatalf("ML boundary incomplete: %+v", b)
	}
}

func TestCompoundWordQuantities(t *testing.T) {
	c := buildercontract.DefaultCompoundContract()
	if c.WordQuantities["pair"] != 2 || c.WordQuantities["two"] != 2 {
		t.Fatalf("pair/two must be 2: %+v", c.WordQuantities)
	}
	if c.MinPages != 2 || !c.ForbidSingleEntry {
		t.Fatalf("compound contract invalid: %+v", c)
	}
}

func TestParsePageOperation(t *testing.T) {
	if _, ok := buildercontract.ParsePageOperation("create_page"); !ok {
		t.Fatal("create_page should parse")
	}
	if _, ok := buildercontract.ParsePageOperation("invented_op"); ok {
		t.Fatal("invented_op must not parse")
	}
}

func TestActiveTargetNotPersistedInV1(t *testing.T) {
	c := buildercontract.DefaultActiveTargetContract()
	if c.Persisted {
		t.Fatal("v1 active target must not claim persistence")
	}
	if !c.TenantScoped || !c.ChatScoped || !c.Bounded {
		t.Fatalf("scoping broken: %+v", c)
	}
}
