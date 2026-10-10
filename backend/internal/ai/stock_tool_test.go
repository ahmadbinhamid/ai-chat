package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-chat/internal/aicatalog"
)

func TestToolsForMode_StockImages(t *testing.T) {
	tests := []struct {
		mode  string
		stock bool
		want  bool
	}{
		{GenerationModeEdit, true, true},
		{"", true, true},
		{GenerationModePages, true, true},
		{GenerationModeEdit, false, false},
		{GenerationModeBrand, true, false},
		{GenerationModeCopy, true, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q stock=%v", tt.mode, tt.stock), func(t *testing.T) {
			got := false
			for _, tool := range toolsForMode(tt.mode, tt.stock) {
				if tool.OfTool.Name == ToolNameSearchStockImages {
					got = true
				}
			}
			if got != tt.want {
				t.Errorf("search_stock_images offered = %v, want %v", got, tt.want)
			}
		})
	}
}

// The tool reaches the provider only on a turn that offers it, and counts as exploration like the theme tools.
func TestGenerate_SendsTheStockToolOnlyWhenOffered(t *testing.T) {
	var names [][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		var n []string
		for _, tool := range req.Tools {
			n = append(n, tool.Name)
		}
		names = append(names, n)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("m", "t", toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	for _, stock := range []bool{false, true} {
		tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, StockImages: stock}
		if _, err := g.Generate(context.Background(), tc, nil, "x", nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	has := func(n []string) bool {
		for _, name := range n {
			if name == ToolNameSearchStockImages {
				return true
			}
		}
		return false
	}
	if has(names[0]) || !has(names[1]) {
		t.Errorf("want the stock tool only on the offering turn, got %v", names)
	}
	if !explorationToolNames[ToolNameSearchStockImages] {
		t.Error("a stock search must count as an exploration call")
	}
}
