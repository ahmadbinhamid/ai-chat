package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// maxModelListBytes bounds the startup model list read; OpenRouter's whole list is a few MB at most.
const maxModelListBytes = 16 << 20

// VerifyModels checks each catalogue model whose provider sets models_url against that list, so a typo fails startup
// instead of routing somewhere unexpected. A list that can't be fetched is only logged: a provider outage must not
// stop ai-chat from starting.
func (g *Generator) VerifyModels(ctx context.Context) error {
	if g.fake {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(g.catalog.Providers)) {
		url := g.catalog.Providers[name].ModelsURL
		if url == "" {
			continue
		}
		listed, err := fetchModelList(ctx, url)
		if err != nil {
			slog.Warn("ai: couldn't fetch the provider's model list to check the catalogue; starting anyway",
				"provider", name, "error", err)
			continue
		}
		var unknown []string
		checked := 0
		for _, m := range g.catalog.Models {
			if m.Provider != name {
				continue
			}
			checked++
			if !listed[m.Model] {
				unknown = append(unknown, fmt.Sprintf("%s (%q)", m.ID, m.Model))
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("provider %q does not list catalogue model(s) %s: check config for a typo",
				name, strings.Join(unknown, ", "))
		}
		slog.Info("ai: catalogue models found in the provider's list", "provider", name, "models", checked)
	}
	return nil
}

// fetchModelList reads a public model list. No key is sent: the list needs none, and the URL comes from config.
func fetchModelList(ctx context.Context, url string) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model list returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelListBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	listed := make(map[string]bool, len(body.Data))
	for _, m := range body.Data {
		listed[m.ID] = true
	}
	// An empty list says nothing about the catalogue, so it counts as unavailable rather than as every model unknown.
	if len(listed) == 0 {
		return nil, fmt.Errorf("the model list is empty")
	}
	return listed, nil
}
