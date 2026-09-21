package buildershadow

import (
	"strings"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

// Comparison is a secret-free A/B summary (no raw prompts).
type Comparison struct {
	CandidateName string `json:"candidate_name"`

	ProductionProvider string `json:"production_provider,omitempty"`
	CandidateProvider  string `json:"candidate_provider,omitempty"`
	CandidateModel     string `json:"candidate_model,omitempty"`

	Skipped       bool   `json:"skipped"`
	SkipReason    string `json:"skip_reason,omitempty"`
	CandidateCalled bool `json:"candidate_called"`

	CandidateValid   bool `json:"candidate_valid"`   // ApplySemanticRefinement succeeded
	CandidateUnsafe  bool `json:"candidate_unsafe"`  // smuggled ops/paths or validation fail
	CandidateLatencyMS int64 `json:"candidate_latency_ms"`

	ProductionRefined bool `json:"production_refined"`
	CandidateRefined  bool `json:"candidate_refined"`

	ClarificationAgree bool    `json:"clarification_agree"`
	ConstraintsF1      float64 `json:"constraints_f1"`
	ProtectedFieldsF1  float64 `json:"protected_fields_f1"`
	PreferencesF1      float64 `json:"preferences_f1"`
	ExactMatch         bool    `json:"exact_structured_match"`

	Status string `json:"status"` // always discarded_shadow_only when ran
}

// Compare extracts semantic fields from production vs candidate plans and scores them.
func Compare(prodPlan, candPlan builderplan.BuilderPlan, prodMeta, candMeta builderintelligence.Meta, candidateValid, candidateUnsafe bool, latencyMS int64) Comparison {
	pCons, pProt, pPref, pClar := extractSemantics(prodPlan)
	cCons, cProt, cPref, cClar := extractSemantics(candPlan)

	return Comparison{
		CandidateName:        "shadow_candidate",
		ProductionProvider:   prodMeta.Provider,
		CandidateProvider:    candMeta.Provider,
		CandidateModel:       candMeta.Model,
		CandidateCalled:      candMeta.Called,
		CandidateValid:       candidateValid,
		CandidateUnsafe:      candidateUnsafe,
		CandidateLatencyMS:   latencyMS,
		ProductionRefined:    prodMeta.RefinementApplied,
		CandidateRefined:     candMeta.RefinementApplied,
		ClarificationAgree:   (pClar != "") == (cClar != "") || strings.EqualFold(pClar, cClar),
		ConstraintsF1:        f1(pCons, cCons),
		ProtectedFieldsF1:    f1(pProt, cProt),
		PreferencesF1:        f1(pPref, cPref),
		ExactMatch:           setEqual(pCons, cCons) && setEqual(pProt, cProt) && setEqual(pPref, cPref) && strings.EqualFold(pClar, cClar),
		Status:               StatusDiscarded,
	}
}

func extractSemantics(plan builderplan.BuilderPlan) (constraints, protected, prefs []string, clarification string) {
	for _, c := range plan.Constraints {
		c = strings.TrimSpace(c)
		low := strings.ToLower(c)
		switch {
		case strings.HasPrefix(low, "protect_field:"):
			protected = append(protected, strings.TrimSpace(c[len("protect_field:"):]))
		case strings.HasPrefix(low, "preference:"):
			prefs = append(prefs, strings.TrimSpace(c[len("preference:"):]))
		case strings.HasPrefix(low, "clarification:"):
			clarification = strings.TrimSpace(c[len("clarification:"):])
		default:
			// Skip planner baseline constraints from comparison noise.
			if isPlannerBaseline(low) {
				continue
			}
			constraints = append(constraints, c)
		}
	}
	return
}

func isPlannerBaseline(low string) bool {
	switch {
	case strings.HasPrefix(low, "never_"),
		strings.HasPrefix(low, "prefer_"),
		strings.HasPrefix(low, "planner_"):
		return true
	}
	return false
}

func f1(a, b []string) float64 {
	sa, sb := toSet(a), toSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	var tp int
	for k := range sa {
		if sb[k] {
			tp++
		}
	}
	fp := len(sb) - tp
	fn := len(sa) - tp
	var prec, rec float64
	if tp+fp > 0 {
		prec = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		rec = float64(tp) / float64(tp+fn)
	}
	if prec+rec == 0 {
		return 0
	}
	return 2 * prec * rec / (prec + rec)
}

func toSet(ss []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range ss {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out[s] = true
		}
	}
	return out
}

func setEqual(a, b []string) bool {
	sa, sb := toSet(a), toSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}
