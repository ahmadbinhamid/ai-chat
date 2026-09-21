package buildercontext

// HistoryPolicy controls how much prior chat is replayed to the model.
// Persisted conversation history is never modified — only the generation
// request payload is reduced.
type HistoryPolicy struct {
	// Focused means use a recent window instead of replaying (or summarizing)
	// a long chat history for this generation.
	Focused bool `json:"focused"`
	// MaxRecentTurns caps verbatim prior turns when Focused is true.
	MaxRecentTurns int `json:"max_recent_turns"`
	// SkipSummarize avoids an extra Summarize model call when Focused.
	SkipSummarize bool `json:"skip_summarize"`
}

// ContextPlan is the validated contract for what may reach DeepSeek.
type ContextPlan struct {
	PrimaryFiles    []string      `json:"primary_files"`
	DependencyFiles []string      `json:"dependency_files"`
	SchemaFiles     []string      `json:"schema_files"`
	ReferenceFiles  []string      `json:"reference_files"`
	History         HistoryPolicy `json:"history"`
	MaxFiles        int           `json:"max_files"`
	MaxBytes        int           `json:"max_bytes"`
	MaxReference    int           `json:"max_reference"`
	// OmitManifest skips embedding the component param index (and its build).
	OmitManifest bool `json:"omit_manifest"`
	// OmitFullPagesJSON means inject only a tiny pages.json stub (or empty).
	OmitFullPagesJSON bool `json:"omit_full_pages_json"`
	// OmitFullDefaultsJSON means inject only a tiny defaults.json stub.
	OmitFullDefaultsJSON bool `json:"omit_full_defaults_json"`
	// IncludeFileTree when false clears the dynamic file-tree section.
	IncludeFileTree bool   `json:"include_file_tree"`
	Source          string `json:"source"` // "builderplan" | "fallback"
	Valid           bool   `json:"valid"`
	FallbackReason  string `json:"fallback_reason,omitempty"`
}

// Metrics are structured observability fields (no prompt contents).
type Metrics struct {
	ElapsedMs         int64
	Candidates        int
	Selected          int
	PrimaryCount      int
	DependencyCount   int
	SchemaCount       int
	ReferenceCount    int
	HistoryMessages   int
	BytesBefore       int
	BytesAfter        int
	DuplicatesRemoved int
	ThemeReadsSaved   int
	UsedFallback      bool
	OmitManifest      bool
	FocusedHistory    bool
}

// Default caps — aligned with existing builderplan / complex_page limits.
const (
	DefaultMaxFiles     = 8
	DefaultMaxReference = 2
	DefaultMaxBytes     = 48_000
	MaxHistoryFocused   = 6
	MinHistoryFocused   = 2
)

// AllFiles returns de-duplicated selected paths in priority order:
// primary → dependency → schema → reference (capped).
func (p ContextPlan) AllFiles() []string {
	maxFiles := p.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	refLimit := p.MaxReference
	if refLimit <= 0 {
		refLimit = DefaultMaxReference
	}

	out := make([]string, 0, maxFiles)
	seen := map[string]bool{}
	add := func(paths []string, limit int) {
		added := 0
		for _, path := range paths {
			if path == "" || seen[path] {
				continue
			}
			if len(out) >= maxFiles {
				return
			}
			if limit > 0 && added >= limit {
				return
			}
			seen[path] = true
			out = append(out, path)
			added++
		}
	}
	add(p.PrimaryFiles, 0)
	add(p.DependencyFiles, 0)
	add(p.SchemaFiles, 0)
	add(p.ReferenceFiles, refLimit)
	return out
}

// SelectedSet is a set for O(1) membership checks.
func (p ContextPlan) SelectedSet() map[string]bool {
	files := p.AllFiles()
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f] = true
	}
	return out
}
