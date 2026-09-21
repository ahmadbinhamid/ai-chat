package providers

import (
	"context"
	"strings"
)

// Heuristic is a CPU-only semantic extractor used when no neural local LM
// runtime is available. It never invents operations or file paths.
type Heuristic struct{}

func (Heuristic) Name() string { return "heuristic" }

func (Heuristic) Extract(ctx context.Context, in Input) (SemanticRefinement, error) {
	if err := ctx.Err(); err != nil {
		return SemanticRefinement{}, err
	}
	p := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(in.Prompt)), " "))

	if isVague(p) {
		return SemanticRefinement{
			NeedsClarification: true,
			Clarification:      "Which page or section should improve, and what should change?",
			Confidence:         0.4,
			Source:             "local_heuristic",
		}, nil
	}

	ref := SemanticRefinement{Source: "local_heuristic", Confidence: 0.9}

	if strings.Contains(p, "jpro") ||
		((strings.Contains(p, "keep") || strings.Contains(p, "preserve") || strings.Contains(p, "still not change") ||
			strings.Contains(p, "don't change") || strings.Contains(p, "do not change")) &&
			(strings.Contains(p, "meta") || strings.Contains(p, "seo") || strings.Contains(p, "title"))) {
		ref.Constraints = append(ref.Constraints, "preserve existing JPRO meta titles")
		ref.ProtectedFields = append(ref.ProtectedFields, "meta_title")
	}
	if strings.Contains(p, "software house") || strings.Contains(p, "software company") ||
		(strings.Contains(p, "according to") && strings.Contains(p, "software")) {
		ref.Constraints = append(ref.Constraints, "adapt blog content for software house audience")
		ref.Preferences = append(ref.Preferences, "software house")
	}
	if strings.Contains(p, "saas") {
		ref.Preferences = append(ref.Preferences, "saas audience")
	}
	if strings.Contains(p, "professional") {
		ref.Preferences = append(ref.Preferences, "professional")
		ref.Constraints = append(ref.Constraints, "make tone more professional")
	}
	if strings.Contains(p, "modern") {
		ref.Preferences = append(ref.Preferences, "modern")
	}
	if strings.Contains(p, "slug") &&
		(strings.Contains(p, "don't change") || strings.Contains(p, "do not change") ||
			strings.Contains(p, "dont change") || strings.Contains(p, "leave") || strings.Contains(p, "keep")) {
		ref.ProtectedFields = append(ref.ProtectedFields, "slug")
	}
	if strings.Contains(p, "meta description") &&
		(strings.Contains(p, "leave") || strings.Contains(p, "keep") || strings.Contains(p, "alone") ||
			strings.Contains(p, "don't change") || strings.Contains(p, "do not change")) {
		ref.ProtectedFields = append(ref.ProtectedFields, "meta_description")
	}
	if strings.Contains(p, "pricing") &&
		(strings.Contains(p, "keep") || strings.Contains(p, "preserve") || strings.Contains(p, "current")) {
		ref.ProtectedFields = append(ref.ProtectedFields, "pricing")
		ref.Constraints = append(ref.Constraints, "preserve current pricing")
	}
	if strings.Contains(p, "title") && strings.Contains(p, "change") &&
		!strings.Contains(p, "meta title") && !strings.Contains(p, "jpro") {
		ref.Constraints = append(ref.Constraints, "update blog title only")
	}

	ref.ProtectedFields = uniq(ref.ProtectedFields)
	ref.Constraints = uniq(ref.Constraints)
	ref.Preferences = uniq(ref.Preferences)
	if len(ref.Constraints) == 0 && len(ref.ProtectedFields) == 0 && len(ref.Preferences) == 0 {
		ref.Confidence = 0.5
	}
	return ref, nil
}

func isVague(p string) bool {
	return strings.Contains(p, "make the site better") ||
		strings.Contains(p, "improve the site") ||
		strings.Contains(p, "make it nicer")
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
