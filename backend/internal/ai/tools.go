package ai

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// ToolExecutor executes one read-only tool call against the theme filesystem, returning its
// result as a string or an error. Defined here (not themebuild) so this package never imports themefs.
type ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, error)

// ToolProgress is called just before a tool executes (ToolStarted) and just after it returns
// (ToolFinished), synchronously inside Generate's tool loop — an implementation must not block.
// input is raw, unparsed model arguments; summary in ToolFinished is computed by Generate,
// which already knows each tool's shape, so callers don't duplicate that logic.
type ToolProgress interface {
	ToolStarted(name string, input json.RawMessage)
	ToolFinished(name string, summary string, err error)
}

// toolNameListThemeFiles etc. are the four tool names the generation loop knows about.
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

// proposeChangesTool reuses resultSchema as this tool's input_schema; the shape the model
// must produce is unchanged from when Structured Outputs enforced it directly.
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

// toolsForMode returns the tool set for a GenerationMode. Brand mode offers only
// propose_changes, so the model can't wander off editing files beyond defaults.json.
func toolsForMode(mode string) []anthropic.ToolUnionParam {
	if mode == GenerationModeBrand {
		return []anthropic.ToolUnionParam{proposeChangesTool()}
	}
	return []anthropic.ToolUnionParam{listThemeFilesTool(), readThemeFileTool(), grepThemeTool(), proposeChangesTool()}
}
