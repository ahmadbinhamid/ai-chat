package ai

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// VerifyModels checks each catalogue model whose provider sets verify_models against that provider's model list
// (GET {base_url}/v1/models), so a typo fails startup instead of routing somewhere unexpected. A list that can't be
// fetched is only logged: a provider outage must not stop ai-chat from starting.
func (g *Generator) VerifyModels(ctx context.Context) error {
	if g.fake {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(g.catalog.Providers)) {
		if !g.catalog.Providers[name].VerifyModels {
			continue
		}
		listed, err := g.listModels(ctx, name)
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

func (g *Generator) listModels(ctx context.Context, provider string) (map[string]bool, error) {
	client, ok := g.clients[provider]
	if !ok {
		return nil, fmt.Errorf("no client for provider %q", provider)
	}
	page, err := client.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(page.Data))
	for _, m := range page.Data {
		listed[m.ID] = true
	}
	// An empty list says nothing about the catalogue, so it counts as unavailable rather than as every model unknown.
	if len(listed) == 0 {
		return nil, fmt.Errorf("the model list is empty")
	}
	return listed, nil
}
