package builderplan

// SelectContextFiles returns the smallest relevant theme path set for a plan.
// Pure: callers pass nothing from disk; this only emits logical path hints
// the existing complex_page / simple_edit loaders can honor later.
//
// No theme-wide scan — simple intents get a tiny file set.
func SelectContextFiles(plan BuilderPlan) []string {
	files := make([]string, 0, 8)
	add := func(paths ...string) {
		for _, p := range paths {
			if p == "" {
				continue
			}
			exists := false
			for _, f := range files {
				if f == p {
					exists = true
					break
				}
			}
			if !exists {
				files = append(files, p)
			}
		}
	}

	for _, op := range plan.Operations {
		switch op.Kind {
		case OpSimpleStyleEdit:
			if op.Target == "button/style" {
				add("components/button.liquid", "css/theme.css")
			} else {
				add("css/theme.css")
			}
		case OpSectionEdit:
			add(op.Target)
			if op.Target == "components/footer.liquid" {
				add("pages/css/footer.css")
			}
			if op.Target == "components/header.liquid" {
				add("pages/css/header.css", "defaults.json")
			}
		case OpFullPageEdit, OpUpdatePageContent:
			add(op.Target)
			if op.Target == "pages/blog.liquid" {
				add("pages/css/blog.css", "pages.json")
			}
			if op.Target == "pages/home.liquid" {
				add("pages/css/home.css", "pages.json")
			}
		case OpCreatePage:
			add("pages.json") // registry shape only — not every liquid page
		case OpRegisterPage, OpRegisterExistingPage, OpUpdateSEOMeta:
			add("pages.json")
		case OpDiagnoseExistingPage, OpFixExistingPage:
			if op.Target != "" {
				add(op.Target)
			}
			add("pages.json")
		case OpAddToNavigation:
			add("defaults.json")
		case OpClarify:
			// no theme files
		}
	}

	// Hard cap: never dump a theme-wide tree from this foundation layer.
	const maxFiles = 8
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files
}

// NeedsDeepSeek reports whether expensive generation is still required.
// Ambiguous/clarify-only plans should not call DeepSeek until clarified.
// Deterministic local ops (e.g. register_existing_page) never need DeepSeek.
func NeedsDeepSeek(plan BuilderPlan) bool {
	if plan.Ambiguous || plan.Intent == IntentAmbiguous {
		return false
	}
	if len(plan.Operations) == 1 && plan.Operations[0].Kind == OpClarify {
		return false
	}
	if len(plan.Operations) == 1 && plan.Operations[0].Kind == OpRegisterExistingPage {
		return false
	}
	if len(plan.Operations) == 1 && (plan.Operations[0].Kind == OpDiagnoseExistingPage || plan.Operations[0].Kind == OpFixExistingPage) {
		return false
	}
	if plan.Intent == IntentPageTroubleshoot {
		return false
	}
	if plan.Intent == IntentNavigationRegistry && len(plan.Operations) == 1 &&
		plan.Operations[0].Kind == OpRegisterPage {
		return false
	}
	return true
}
