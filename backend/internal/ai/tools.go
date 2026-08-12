package ai

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// ToolExecutor executes one read-only tool call against the theme
// filesystem, returning the tool's result as a string (its own shape —
// JSON for list_theme_files/grep_theme, plain concatenated content for
// read_theme_file) or an error. Generate calls this for every tool_use
// block except propose_changes, which it handles itself. Defined here
// (not in themebuild) so this package never imports themefs — themebuild
// supplies the closure, keeping ai on the caller side of that boundary the
// same way it already is for themefs.Store today.
type ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, error)

// ToolProgress is called just before a tool executes (ToolStarted) and just
// after it returns (ToolFinished). This package has no idea what a caller
// does with these calls — it never touches an event, a bus, or a
// WebSocket; see themebuild, which implements this interface to turn tool
// calls into stream events the merchant watches live (Cursor/Claude-Code-
// style step narration). name is one of the toolName* constants above.
// input is the tool's raw arguments, exactly as the model sent them — a
// caller that wants a human-readable detail (a file path, a grep pattern)
// unmarshals it itself; this package deliberately doesn't pre-parse it
// into a friendlier shape, since the friendly shape is entirely a display
// concern this package has no business owning.
//
// summary in ToolFinished is a short, best-effort description of the
// outcome (see summarizeToolResult) — Generate computes it, not the
// caller, because Generate is the one place that already knows each
// toolName*'s shape (it owns the tool definitions in this same file) and
// has the raw output/error in hand; forcing every caller to re-derive
// "3 matches" from a raw grep_theme result string would just be the same
// logic duplicated at every call site.
//
// Both methods are called synchronously from inside Generate's tool loop —
// an implementation must not block or do anything slow.
type ToolProgress interface {
	ToolStarted(name string, input json.RawMessage)
	ToolFinished(name string, summary string, err error)
}

// toolNameListThemeFiles etc. are the four tool names the generation loop
// knows about — shared between the schema table below and Generate's
// switch on which one terminates the loop.
const (
	toolNameListThemeFiles = "list_theme_files"
	toolNameReadThemeFile  = "read_theme_file"
	toolNameGrepTheme      = "grep_theme"
	toolNameProposeChanges = "propose_changes"
)

func listThemeFilesTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name: toolNameListThemeFiles,
		Description: param.NewOpt(
			"Returns the active theme's full file tree (directories and files, no content) as JSON. Call this " +
				"first if you don't already know what's in the theme, or again if a prior read/list result feels stale.",
		),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	}}
}

func readThemeFileTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name: toolNameReadThemeFile,
		Description: param.NewOpt(
			"Reads one or more existing theme files by their theme-relative path (e.g. 'components/testimonials.liquid'). " +
				"Max 10 paths per call. Returns each file's real current content — read a file before you propose changing " +
				"it, never guess its current contents.",
		),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"paths": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Theme-relative file paths to read, e.g. ['pages/home.liquid', 'components/testimonials.liquid']. Max 10.",
				},
			},
			Required: []string{"paths"},
		},
	}}
}

func grepThemeTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name: toolNameGrepTheme,
		Description: param.NewOpt(
			"Searches the theme's text files (.liquid/.css/.js/.json) for a regular expression (RE2 syntax, e.g. Go's " +
				"regexp — not a plain substring), returning matching file paths with line numbers and the matching line " +
				"text. Use this to find where a component is rendered, where a class/token is used, or whether a page " +
				"already exists, without reading every file one at a time.",
		),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "RE2 regular expression to search for, e.g. \"render 'components/testimonials'\".",
				},
				"path_glob": map[string]any{
					"type":        "string",
					"description": "Optional glob restricting which files are searched, e.g. 'components/*.liquid'. Omit to search the whole theme.",
				},
			},
			Required: []string{"pattern"},
		},
	}}
}

// proposeChangesTool reuses resultSchema (the exact shape Structured
// Outputs used to enforce) as this tool's input_schema — the model now
// finalizes a turn by calling this tool instead of the response itself
// being schema-constrained, but the shape it must produce is unchanged.
func proposeChangesTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name: toolNameProposeChanges,
		Description: param.NewOpt(
			"Finalizes this turn: the complete set of file changes, page registration, and layout link/script " +
				"registrations for the merchant's request. Call this exactly once, when you're done exploring/editing — " +
				"never before you've read every existing file you're about to modify.",
		),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties:  resultSchema["properties"],
			Required:    resultSchema["required"].([]string),
			ExtraFields: map[string]any{"additionalProperties": false},
		},
		Strict: param.NewOpt(true),
	}}
}

// toolsForMode returns the tool set offered for a given GenerationMode.
// Brand mode is the one restriction: only propose_changes is offered, so
// the model can't wander off exploring/editing arbitrary theme files when
// this turn is only ever supposed to touch defaults.json (see
// checkGenerationMode in themebuild for the matching write-side restriction).
func toolsForMode(mode string) []anthropic.ToolUnionParam {
	if mode == GenerationModeBrand {
		return []anthropic.ToolUnionParam{proposeChangesTool()}
	}
	return []anthropic.ToolUnionParam{listThemeFilesTool(), readThemeFileTool(), grepThemeTool(), proposeChangesTool()}
}
