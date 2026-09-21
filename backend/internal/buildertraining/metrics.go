package buildertraining

import (
	"math"
	"sort"
	"strings"
	"time"

	"ai-chat/internal/builderdataset"
	"ai-chat/internal/builderintelligence/providers"
)

// FieldMetrics is precision/recall for a multi-label string field.
type FieldMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
}

// PerfMetrics captures latency and resource samples.
type PerfMetrics struct {
	ColdLatencyMS float64 `json:"cold_latency_ms"`
	WarmLatencyMS float64 `json:"warm_latency_ms"`
	P50MS         float64 `json:"p50_ms"`
	P95MS         float64 `json:"p95_ms"`
	RAMMB         float64 `json:"ram_mb,omitempty"`
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	Samples       int     `json:"samples"`
}

// ModelMetrics aggregates field-level and safety scores for one model.
type ModelMetrics struct {
	ModelName                    string      `json:"model_name"`
	ExamplesEvaluated            int         `json:"examples_evaluated"`
	Constraints                  FieldMetrics `json:"constraints"`
	ProtectedFields              FieldMetrics `json:"protected_fields"`
	Preferences                  FieldMetrics `json:"preferences"`
	ClarificationAccuracy        float64     `json:"clarification_accuracy"`
	ExactStructuredOutputRate    float64     `json:"exact_structured_output_rate"`
	InvalidJSONRate              float64     `json:"invalid_json_rate"`
	UnsupportedFieldRate         float64     `json:"unsupported_field_rate"`
	UnsafeOutputRate             float64     `json:"unsafe_output_rate"`
	SemanticAccuracy             float64     `json:"semantic_accuracy"` // mean F1 across fields
	ProtectedFieldAccuracy       float64     `json:"protected_field_accuracy"`
	ValidJSONPercent             float64     `json:"valid_json_percent"`
	Perf                         PerfMetrics `json:"perf"`
	Errors                       int         `json:"errors"`
}

// Prediction is one model output for scoring.
type Prediction struct {
	Refinement         providers.SemanticRefinement
	InvalidJSON        bool
	UnsupportedFields  bool
	Unsafe             bool // failed BuilderPlan validation or invented ops/paths
	Latency            time.Duration
	Err                error
}

// ScorePredictions compares predictions to gold TrainingExamples.
func ScorePredictions(modelName string, gold []builderdataset.TrainingExample, preds []Prediction, perf PerfMetrics) ModelMetrics {
	m := ModelMetrics{ModelName: modelName, Perf: perf}
	n := len(gold)
	if len(preds) < n {
		n = len(preds)
	}
	m.ExamplesEvaluated = n
	if n == 0 {
		return m
	}

	var cTP, cFP, cFN, pTP, pFP, pFN, rTP, rFP, rFN int
	var clarOK, exact, invalidJSON, unsupported, unsafe int
	for i := 0; i < n; i++ {
		g := gold[i].Target
		pr := preds[i]
		if pr.Err != nil {
			m.Errors++
		}
		if pr.InvalidJSON {
			invalidJSON++
		}
		if pr.UnsupportedFields {
			unsupported++
		}
		if pr.Unsafe {
			unsafe++
		}
		got := pr.Refinement
		ctp, cfp, cfn := setPR(g.Constraints, got.Constraints)
		cTP += ctp
		cFP += cfp
		cFN += cfn
		ptp, pfp, pfn := setPR(g.ProtectedFields, got.ProtectedFields)
		pTP += ptp
		pFP += pfp
		pFN += pfn
		rtp, rfp, rfn := setPR(g.Preferences, got.Preferences)
		rTP += rtp
		rFP += rfp
		rFN += rfn
		if g.NeedsClarification == got.NeedsClarification {
			clarOK++
		}
		if exactMatch(g, got) {
			exact++
		}
	}
	m.Constraints = fieldFromCounts(cTP, cFP, cFN)
	m.ProtectedFields = fieldFromCounts(pTP, pFP, pFN)
	m.Preferences = fieldFromCounts(rTP, rFP, rFN)
	m.ClarificationAccuracy = float64(clarOK) / float64(n)
	m.ExactStructuredOutputRate = float64(exact) / float64(n)
	m.InvalidJSONRate = float64(invalidJSON) / float64(n)
	m.UnsupportedFieldRate = float64(unsupported) / float64(n)
	m.UnsafeOutputRate = float64(unsafe) / float64(n)
	m.ValidJSONPercent = 100 * (1 - m.InvalidJSONRate)
	m.ProtectedFieldAccuracy = m.ProtectedFields.F1
	m.SemanticAccuracy = meanF1(m.Constraints, m.ProtectedFields, m.Preferences)
	return m
}

func setPR(gold, pred []string) (tp, fp, fn int) {
	g := normSet(gold)
	p := normSet(pred)
	for k := range g {
		if p[k] {
			tp++
		} else {
			fn++
		}
	}
	for k := range p {
		if !g[k] {
			fp++
		}
	}
	return
}

func normSet(ss []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range ss {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out[s] = true
		}
	}
	return out
}

func fieldFromCounts(tp, fp, fn int) FieldMetrics {
	fm := FieldMetrics{TP: tp, FP: fp, FN: fn}
	if tp+fp > 0 {
		fm.Precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		fm.Recall = float64(tp) / float64(tp+fn)
	}
	if fm.Precision+fm.Recall > 0 {
		fm.F1 = 2 * fm.Precision * fm.Recall / (fm.Precision + fm.Recall)
	}
	return fm
}

func meanF1(fs ...FieldMetrics) float64 {
	if len(fs) == 0 {
		return 0
	}
	var s float64
	for _, f := range fs {
		s += f.F1
	}
	return s / float64(len(fs))
}

func exactMatch(g builderdataset.TrainingTarget, got providers.SemanticRefinement) bool {
	return setEqual(g.Constraints, got.Constraints) &&
		setEqual(g.ProtectedFields, got.ProtectedFields) &&
		setEqual(g.Preferences, got.Preferences) &&
		g.NeedsClarification == got.NeedsClarification
}

func setEqual(a, b []string) bool {
	sa, sb := normSet(a), normSet(b)
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

// LatencyStats computes p50/p95 from durations.
func LatencyStats(durs []time.Duration) PerfMetrics {
	pm := PerfMetrics{Samples: len(durs)}
	if len(durs) == 0 {
		return pm
	}
	ms := make([]float64, len(durs))
	for i, d := range durs {
		ms[i] = float64(d.Microseconds()) / 1000.0
	}
	sort.Float64s(ms)
	pm.ColdLatencyMS = ms[0]
	pm.WarmLatencyMS = ms[len(ms)-1]
	if len(ms) > 1 {
		pm.WarmLatencyMS = ms[1] // second call approx warm
	}
	pm.P50MS = percentile(ms, 0.50)
	pm.P95MS = percentile(ms, 0.95)
	return pm
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	w := idx - float64(lo)
	return sorted[lo]*(1-w) + sorted[hi]*w
}
