package builderplan

import "strings"

// ApplySemanticRefinement merges local semantic extraction onto a
// deterministic BuilderPlan without changing intent, operations, targets,
// required files, or counts. Invalid/empty refinements are no-ops.
func ApplySemanticRefinement(base BuilderPlan, constraints, protectedFields, preferences []string, clarification string, needsClarification bool, confidence float64, source string) (BuilderPlan, error) {
	out := base
	extra := make([]string, 0, len(constraints)+len(protectedFields)+len(preferences)+1)
	for _, c := range constraints {
		c = strings.TrimSpace(c)
		if c == "" || looksDestructiveConstraint(c) {
			continue
		}
		// Reject attempts to smuggle operations/paths.
		if looksLikeOperationSmuggle(c) {
			continue
		}
		extra = append(extra, c)
	}
	for _, f := range protectedFields {
		f = normalizeProtectedField(f)
		if f == "" {
			continue
		}
		extra = append(extra, "protect_field:"+f)
	}
	for _, p := range preferences {
		p = strings.TrimSpace(p)
		if p == "" || looksLikeOperationSmuggle(p) {
			continue
		}
		extra = append(extra, "preference:"+p)
	}
	if s := strings.TrimSpace(clarification); s != "" && !looksLikeOperationSmuggle(s) {
		extra = append(extra, "clarification:"+s)
	}
	if len(extra) > 0 {
		out.Constraints = uniqueStrings(append(append([]string{}, base.Constraints...), extra...))
	}
	// Clarification flag only reinforces ambiguity when the plan is already
	// ambiguous — never upgrades/downgrades deterministic intent.
	if needsClarification && base.Ambiguous {
		out.AcceptanceCriteria = uniqueStrings(append(append([]string{}, base.AcceptanceCriteria...),
			"require_clarification_before_generation"))
	}
	if confidence > 0 && confidence <= 1 {
		// Soft bump only — never overwrite a higher deterministic confidence downward below 0.5
		// when we successfully extracted semantics; keep deterministic confidence otherwise.
		if confidence >= 0.5 {
			out.Confidence = confidence
		}
	}
	if strings.TrimSpace(source) != "" {
		out.ClassifierSource = source
	} else if len(extra) > 0 {
		out.ClassifierSource = "local_semantic"
	}
	if err := Validate(out); err != nil {
		return base, err
	}
	return out, nil
}

func normalizeProtectedField(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	f = strings.ReplaceAll(f, " ", "_")
	switch f {
	case "meta_title", "seo_title", "title", "meta_description", "seo_description",
		"description", "slug", "path", "status", "og_title", "og_description",
		"og_image", "keywords", "seo_keywords", "jpro_meta_title", "jpro_meta_titles":
		return f
	default:
		// Allow short snake_case field names only.
		for _, r := range f {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				return ""
			}
		}
		if len(f) == 0 || len(f) > 40 {
			return ""
		}
		return f
	}
}

func looksLikeOperationSmuggle(s string) bool {
	low := strings.ToLower(s)
	if strings.Contains(low, "delete ") || strings.Contains(low, "wipe ") ||
		strings.Contains(low, "rm -rf") || strings.Contains(low, "..") {
		return true
	}
	if strings.HasPrefix(low, "pages/") || strings.HasPrefix(low, "create_page") ||
		strings.HasPrefix(low, "register_page") || strings.Contains(low, "operation:") {
		return true
	}
	switch low {
	case "simple_style_edit", "section_edit", "full_page_edit", "create_page",
		"register_page", "update_seo_meta", "update_page_content", "add_to_navigation", "clarify",
		"simple_edit", "compound", "page_create", "navigation_registry", "seo_meta", "full_page":
		return true
	}
	return false
}
