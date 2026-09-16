package ai

import (
	"testing"
)

func TestToolsForContext_RepairNarrows(t *testing.T) {
	tools := toolsForContext(ThemeContext{Repair: true, GenerationMode: GenerationModePages})
	names := map[string]bool{}
	for _, tu := range tools {
		if tu.OfTool != nil {
			names[tu.OfTool.Name] = true
		}
	}
	if !names[toolNameReadThemeFile] || !names[toolNameValidateChanges] || !names[toolNameProposeChanges] {
		t.Fatalf("repair tools missing required set: %v", names)
	}
	if names[toolNameListThemeFiles] || names[toolNameGrepTheme] {
		t.Fatalf("repair must not expose list/grep: %v", names)
	}
}

func TestToolsForContext_BrandProposeOnly(t *testing.T) {
	tools := toolsForContext(ThemeContext{GenerationMode: GenerationModeBrand})
	if len(tools) != 1 || tools[0].OfTool == nil || tools[0].OfTool.Name != toolNameProposeChanges {
		t.Fatalf("brand should be propose-only, got %#v", tools)
	}
}

func TestToolsForContext_SimpleEditOneShot(t *testing.T) {
	tools := toolsForContext(ThemeContext{SimpleEditOneShot: true})
	if len(tools) != 1 || tools[0].OfTool == nil || tools[0].OfTool.Name != toolNameProposeChanges {
		t.Fatalf("simple-edit one-shot should be propose-only, got %#v", tools)
	}
	tools = toolsForContext(ThemeContext{SimpleEditOneShot: true, SimpleEditAllowRead: true})
	names := map[string]bool{}
	for _, tu := range tools {
		if tu.OfTool != nil {
			names[tu.OfTool.Name] = true
		}
	}
	if !names[toolNameProposeChanges] || !names[toolNameReadThemeFile] {
		t.Fatalf("allow-read should expose read+propose, got %v", names)
	}
	if names[toolNameGrepTheme] || names[toolNameListThemeFiles] {
		t.Fatalf("simple-edit must not expose list/grep: %v", names)
	}
}
