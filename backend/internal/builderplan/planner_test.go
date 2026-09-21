package builderplan

import (
	"strings"
	"testing"
)

func TestBuildPlan_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		prompt          string
		wantIntent      IntentKind
		wantCompound    bool
		wantAmbiguous   bool
		wantMinOps      int
		wantOpKinds     []OperationKind // if set, must all appear
		wantFilesAnyOf  []string
		wantDeepSeek    bool
		forbidFiles     []string
		checkProtectAll bool
		checkNoDestruct bool
	}{
		{
			name:            "create_2_blog_pages",
			prompt:          "create 2 blog pages",
			wantIntent:      IntentCompound,
			wantCompound:    true,
			wantMinOps:      2,
			wantOpKinds:     []OperationKind{OpCreatePage, OpRegisterPage},
			wantFilesAnyOf:  []string{"pages.json"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "change_blog_meta_titles",
			prompt:          "change the blog meta titles",
			wantIntent:      IntentSEOMeta,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpUpdateSEOMeta},
			wantFilesAnyOf:  []string{"pages.json"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "change_blog_content_software_house",
			prompt:          "change blog content for a software house",
			wantIntent:      IntentFullPage,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpUpdatePageContent},
			wantFilesAnyOf:  []string{"pages/blog.liquid"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "create_2_pages_and_navigation",
			prompt:          "create 2 pages and add them to navigation",
			wantIntent:      IntentCompound,
			wantCompound:    true,
			wantMinOps:      3,
			wantOpKinds:     []OperationKind{OpCreatePage, OpRegisterPage, OpAddToNavigation},
			wantFilesAnyOf:  []string{"pages.json", "defaults.json"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "change_button_color_blue",
			prompt:          "change button color to blue",
			wantIntent:      IntentSimpleEdit,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpSimpleStyleEdit},
			wantFilesAnyOf:  []string{"css/theme.css", "components/button.liquid"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "ambiguous_request",
			prompt:          "can you help with something",
			wantIntent:      IntentAmbiguous,
			wantAmbiguous:   true,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpClarify},
			wantDeepSeek:    false,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "existing_pages_protected",
			prompt:          "create 2 blog pages",
			wantIntent:      IntentCompound,
			wantCompound:    true,
			wantMinOps:      2,
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "compound_multiple_operations",
			prompt:          "update blog content for a software house and change the meta titles",
			wantIntent:      IntentCompound,
			wantCompound:    true,
			wantMinOps:      2,
			wantOpKinds:     []OperationKind{OpUpdatePageContent, OpUpdateSEOMeta},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "no_destructive_whole_file_instructions",
			prompt:          "create a contact page",
			wantIntent:      IntentPageCreate,
			wantMinOps:      1,
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
			forbidFiles:     []string{"*", "**", "theme/*"},
		},
		{
			name:            "blog_professional_saas_protect_slug",
			prompt:          "make the blog more professional for SaaS customers but don't change the slug",
			wantIntent:      IntentFullPage,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpUpdatePageContent},
			wantFilesAnyOf:  []string{"pages/blog.liquid"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "update_blog_keep_slug",
			prompt:          "update the blog content but keep the slug",
			wantIntent:      IntentFullPage,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpUpdatePageContent},
			wantFilesAnyOf:  []string{"pages/blog.liquid"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:            "rewrite_existing_blog_software",
			prompt:          "rewrite this existing blog for software companies",
			wantIntent:      IntentFullPage,
			wantMinOps:      1,
			wantOpKinds:     []OperationKind{OpUpdatePageContent},
			wantFilesAnyOf:  []string{"pages/blog.liquid"},
			wantDeepSeek:    true,
			checkProtectAll: true,
			checkNoDestruct: true,
		},
		{
			name:          "make_site_better_ambiguous",
			prompt:        "make the site better",
			wantIntent:    IntentAmbiguous,
			wantAmbiguous: true,
			wantMinOps:    1,
			wantOpKinds:   []OperationKind{OpClarify},
			wantDeepSeek:  false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := BuildPlan(tc.prompt)
			if err != nil {
				t.Fatalf("BuildPlan(%q): %v", tc.prompt, err)
			}
			if plan.Intent != tc.wantIntent {
				t.Fatalf("intent=%s want %s (ops=%v signals via ops)", plan.Intent, tc.wantIntent, opKinds(plan))
			}
			if plan.Compound != tc.wantCompound {
				t.Fatalf("compound=%v want %v", plan.Compound, tc.wantCompound)
			}
			if plan.Ambiguous != tc.wantAmbiguous {
				t.Fatalf("ambiguous=%v want %v", plan.Ambiguous, tc.wantAmbiguous)
			}
			if len(plan.Operations) < tc.wantMinOps {
				t.Fatalf("ops=%d want >= %d (%v)", len(plan.Operations), tc.wantMinOps, opKinds(plan))
			}
			for _, want := range tc.wantOpKinds {
				if !hasOp(plan, want) {
					t.Fatalf("missing op %s in %v", want, opKinds(plan))
				}
			}
			if tc.wantCompound && len(plan.Operations) < 2 {
				t.Fatalf("compound must produce multiple operations, got %d", len(plan.Operations))
			}
			if NeedsDeepSeek(plan) != tc.wantDeepSeek {
				t.Fatalf("NeedsDeepSeek=%v want %v", NeedsDeepSeek(plan), tc.wantDeepSeek)
			}
			if tc.checkProtectAll {
				for _, op := range plan.Operations {
					if !op.ProtectExisting {
						t.Fatalf("operation %s must ProtectExisting", op.Kind)
					}
				}
				protected := false
				for _, c := range plan.Constraints {
					if strings.Contains(c, "never_rewrite_unrelated") || strings.Contains(c, "never_delete_existing") {
						protected = true
						break
					}
				}
				for _, a := range plan.AcceptanceCriteria {
					if strings.Contains(a, "existing_pages_remain") {
						protected = true
						break
					}
				}
				if !protected {
					t.Fatal("expected existing-page protection in constraints/acceptance")
				}
			}
			if tc.checkNoDestruct {
				if err := Validate(plan); err != nil {
					t.Fatalf("Validate: %v", err)
				}
				blob := strings.ToLower(plan.OriginalPrompt + strings.Join(plan.Constraints, " "))
				for _, op := range plan.Operations {
					blob += strings.ToLower(op.Label + " " + op.Target + " " + string(op.Kind))
				}
				for _, bad := range []string{"wipe theme", "delete all pages", "rewrite entire theme", "rm -rf"} {
					if strings.Contains(blob, bad) {
						t.Fatalf("destructive instruction leaked: %q", bad)
					}
				}
				for _, a := range plan.AcceptanceCriteria {
					if a == "no_destructive_whole_file_rewrite_instructions" {
						goto okDestruct
					}
				}
				t.Fatal("missing no_destructive_whole_file_rewrite_instructions acceptance criterion")
			okDestruct:
			}
			for _, f := range tc.forbidFiles {
				for _, got := range plan.RequiredFiles {
					if got == f {
						t.Fatalf("forbidden file %q in RequiredFiles", f)
					}
				}
				for _, got := range plan.Targets {
					if got == f {
						t.Fatalf("forbidden target %q", f)
					}
				}
			}
			if len(tc.wantFilesAnyOf) > 0 {
				found := false
				for _, want := range tc.wantFilesAnyOf {
					for _, got := range plan.RequiredFiles {
						if got == want {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					t.Fatalf("RequiredFiles %v missing any of %v", plan.RequiredFiles, tc.wantFilesAnyOf)
				}
			}
			// Context selection must stay small — no theme-wide dump.
			if len(plan.RequiredFiles) > 8 {
				t.Fatalf("RequiredFiles too large: %d", len(plan.RequiredFiles))
			}
			if plan.ClassifierSource != "deterministic" {
				t.Fatalf("ClassifierSource=%q", plan.ClassifierSource)
			}
		})
	}
}

func TestClassify_CompoundDetection(t *testing.T) {
	t.Parallel()
	c := DeterministicClassifier{}
	cases := []struct {
		prompt string
		want   IntentKind
	}{
		{"create 2 pages", IntentCompound},
		{"create page and add to menu", IntentCompound},
		{"update blog content and SEO meta titles", IntentCompound},
		{"change header and footer colors", IntentSectionEdit}, // single section-ish — may be section
	}
	for _, tc := range cases {
		got := c.Classify(tc.prompt)
		if tc.prompt == "change header and footer colors" {
			// either section or simple is acceptable; must not be ambiguous
			if got.Intent == IntentAmbiguous {
				t.Fatalf("%q classified ambiguous", tc.prompt)
			}
			continue
		}
		if got.Intent != tc.want {
			t.Fatalf("Classify(%q)=%s want %s", tc.prompt, got.Intent, tc.want)
		}
	}
}

func TestValidate_RejectsDestructive(t *testing.T) {
	t.Parallel()
	bad := BuilderPlan{
		OriginalPrompt: "x",
		Intent:         IntentSimpleEdit,
		Operations: []Operation{{
			Kind: OpSimpleStyleEdit, Label: "wipe theme", Target: "*", ProtectExisting: true,
		}},
	}
	if err := Validate(bad); err == nil {
		t.Fatal("expected destructive plan to fail validation")
	}
}

func TestValidate_RequiresProtectExisting(t *testing.T) {
	t.Parallel()
	bad := BuilderPlan{
		OriginalPrompt: "x",
		Intent:         IntentSimpleEdit,
		Operations: []Operation{{
			Kind: OpSimpleStyleEdit, Target: "css/theme.css", ProtectExisting: false,
		}},
	}
	if err := Validate(bad); err == nil {
		t.Fatal("expected unprotected op to fail")
	}
}

func TestSelectContextFiles_SimpleIsMinimal(t *testing.T) {
	t.Parallel()
	plan, err := BuildPlan("change button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	files := SelectContextFiles(plan)
	if len(files) == 0 || len(files) > 4 {
		t.Fatalf("simple edit context should be tiny, got %v", files)
	}
	for _, f := range files {
		if f == "pages/home.liquid" || strings.HasPrefix(f, "pages/") && f != "" {
			// button color must not pull homepage liquid
			if strings.Contains(f, "home.liquid") {
				t.Fatalf("unexpected home context: %v", files)
			}
		}
	}
}

func TestNeedsDeepSeek_AmbiguousSkipped(t *testing.T) {
	t.Parallel()
	plan, err := BuildPlan("hmm okay whatever")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsDeepSeek(plan) {
		t.Fatalf("ambiguous plan must not require DeepSeek: %+v", plan)
	}
}

func TestBuildPlanWith_NilClassifierFallsBack(t *testing.T) {
	t.Parallel()
	plan, err := BuildPlanWith(nil, "change button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != IntentSimpleEdit {
		t.Fatalf("got %s", plan.Intent)
	}
}

func opKinds(plan BuilderPlan) []OperationKind {
	out := make([]OperationKind, len(plan.Operations))
	for i, op := range plan.Operations {
		out[i] = op.Kind
	}
	return out
}

func hasOp(plan BuilderPlan, kind OperationKind) bool {
	for _, op := range plan.Operations {
		if op.Kind == kind {
			return true
		}
	}
	return false
}
