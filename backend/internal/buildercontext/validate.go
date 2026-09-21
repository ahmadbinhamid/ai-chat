package buildercontext

import (
	"fmt"
	"strings"
)

// Validate checks a ContextPlan before themebuild loads any files.
func Validate(p ContextPlan) error {
	if !p.Valid {
		return fmt.Errorf("buildercontext: plan not valid")
	}
	if p.MaxFiles < 0 {
		return fmt.Errorf("buildercontext: negative max files")
	}
	if p.MaxFiles > DefaultMaxFiles*2 {
		return fmt.Errorf("buildercontext: max files too large")
	}
	check := func(label string, paths []string) error {
		for _, path := range paths {
			if err := validatePath(path); err != nil {
				return fmt.Errorf("buildercontext: %s: %w", label, err)
			}
		}
		return nil
	}
	if err := check("primary", p.PrimaryFiles); err != nil {
		return err
	}
	if err := check("dependency", p.DependencyFiles); err != nil {
		return err
	}
	if err := check("schema", p.SchemaFiles); err != nil {
		return err
	}
	if err := check("reference", p.ReferenceFiles); err != nil {
		return err
	}
	if p.History.Focused && p.History.MaxRecentTurns > MaxHistoryFocused {
		return fmt.Errorf("buildercontext: history window too large")
	}
	return nil
}

func validatePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("unsafe path %q", path)
	}
	// Allow only theme-relative logical paths.
	okPrefix := strings.HasPrefix(path, "pages/") ||
		strings.HasPrefix(path, "components/") ||
		strings.HasPrefix(path, "css/") ||
		strings.HasPrefix(path, "liquid/") ||
		path == "pages.json" ||
		path == "defaults.json"
	if !okPrefix {
		return fmt.Errorf("disallowed path %q", path)
	}
	return nil
}

// Deduplicate collapses overlapping categories (primary wins) and returns
// how many duplicate path entries were dropped.
func Deduplicate(p ContextPlan) (ContextPlan, int) {
	seen := map[string]bool{}
	take := func(in []string) ([]string, int) {
		out := make([]string, 0, len(in))
		removed := 0
		for _, path := range in {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if seen[path] {
				removed++
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
		return out, removed
	}
	var removed int
	var n int
	p.PrimaryFiles, n = take(p.PrimaryFiles)
	removed += n
	p.DependencyFiles, n = take(p.DependencyFiles)
	removed += n
	p.SchemaFiles, n = take(p.SchemaFiles)
	removed += n
	p.ReferenceFiles, n = take(p.ReferenceFiles)
	removed += n

	// Cap totals.
	all := p.AllFiles()
	if p.MaxFiles > 0 && len(all) > p.MaxFiles {
		keep := map[string]bool{}
		for _, f := range all[:p.MaxFiles] {
			keep[f] = true
		}
		filter := func(in []string) []string {
			out := make([]string, 0, len(in))
			for _, pth := range in {
				if keep[pth] {
					out = append(out, pth)
				}
			}
			return out
		}
		p.PrimaryFiles = filter(p.PrimaryFiles)
		p.DependencyFiles = filter(p.DependencyFiles)
		p.SchemaFiles = filter(p.SchemaFiles)
		p.ReferenceFiles = filter(p.ReferenceFiles)
	}
	return p, removed
}

// NarrowExisting returns existing paths restricted to preferred when every
// preferred path is already in existing (safe subset). Otherwise returns
// existing unchanged — never expands, never invents paths.
func NarrowExisting(existing, preferred []string) (out []string, narrowed bool) {
	if len(existing) == 0 || len(preferred) == 0 {
		return existing, false
	}
	have := map[string]bool{}
	for _, p := range existing {
		have[p] = true
	}
	var keep []string
	for _, p := range preferred {
		if have[p] {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return existing, false
	}
	// Prefer order of existing ranking, filtered to keep set.
	keepSet := map[string]bool{}
	for _, p := range keep {
		keepSet[p] = true
	}
	out = make([]string, 0, len(keep))
	for _, p := range existing {
		if keepSet[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 || len(out) >= len(existing) {
		return existing, false
	}
	return out, true
}

// EstimateBytes is a rough pre-load size hint (UTF-8 byte length of path list
// + policy flags). Real before/after bytes are measured by themebuild.
func EstimateBytes(p ContextPlan) int {
	n := 0
	for _, f := range p.AllFiles() {
		n += len(f) + 16
	}
	if !p.OmitFullPagesJSON {
		n += 2048
	}
	if !p.OmitFullDefaultsJSON {
		n += 1024
	}
	if !p.OmitManifest {
		n += 4096
	}
	return n
}
