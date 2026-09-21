package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxRepeatedPathReads forces propose_changes after the same theme path has
// been successfully returned by read_theme_file this many times in one
// Generate — re-reading the same file is not useful exploration.
const maxRepeatedPathReads = 2

// compactReadThemeFileResult records path read counts. When every path in
// this call was already returned earlier this Generate, the full file bodies
// are replaced with short stubs so the messages array does not re-embed the
// same large content. forceNext is set when any path exceeds
// maxRepeatedPathReads.
func compactReadThemeFileResult(output string, input json.RawMessage, counts map[string]int, forceNext bool) (string, bool) {
	var args struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(input, &args); err != nil || len(args.Paths) == 0 {
		return output, forceNext
	}

	allRepeats := true
	for _, p := range args.Paths {
		counts[p]++
		if counts[p] > maxRepeatedPathReads {
			forceNext = true
		}
		if counts[p] == 1 {
			allRepeats = false
		}
	}
	if !allRepeats {
		return output, forceNext
	}

	var b strings.Builder
	for _, p := range args.Paths {
		fmt.Fprintf(&b, "### %s\n(omitted — already returned earlier this turn; use that prior tool_result)\n\n", p)
	}
	return b.String(), forceNext
}
