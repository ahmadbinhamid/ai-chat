package themebuild

import (
	"context"
	"fmt"
	"log/slog"

	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/previewerrors"
)

// SetModelCatalog enables per-message model choice; without it every turn runs on the generator's default.
func (s *Service) SetModelCatalog(c *aicatalog.Catalog) { s.models = c }

// ModelChoices is what the dashboard may offer; it carries no provider details.
func (s *Service) ModelChoices() aicatalog.Public {
	if s.models == nil {
		return aicatalog.Public{Models: []aicatalog.PublicModel{}}
	}
	return s.models.Public()
}

// ModelLabel is a model's merchant-facing label, for showing which model answered; "" when unknown.
func (s *Service) ModelLabel(id string) string {
	if s.models == nil {
		return ""
	}
	return s.models.Label(id)
}

// selectModel validates the requested model and effort against the catalogue; only catalogue ids are ever accepted.
func (s *Service) selectModel(in GenerateInput) (aicatalog.Selection, error) {
	if s.models == nil {
		return aicatalog.Selection{}, nil
	}
	sel, err := s.models.Select(in.ModelID, in.Effort)
	if err != nil {
		return aicatalog.Selection{}, fmt.Errorf("%w: %w", ErrInvalidModelChoice, err)
	}
	return sel, nil
}

// resolveModel picks this turn's concrete model; auto needs the chat's earlier prompts to spot a fix follow-up.
func (s *Service) resolveModel(ctx context.Context, in GenerateInput, c chat.Chat, sel aicatalog.Selection, hasPreviewErrors bool) (aicatalog.Choice, error) {
	if s.models == nil {
		return aicatalog.Choice{}, nil
	}
	if sel.ModelID != aicatalog.AutoID {
		return s.models.Resolve(sel, false), nil
	}
	prior, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return aicatalog.Choice{}, fmt.Errorf("load chat history for model routing: %w", err)
	}
	fixTurn := previewerrors.IsFixTurn(in.Prompt, earlierUserPrompts(prior, ""), hasPreviewErrors)
	choice := s.models.Resolve(sel, fixTurn)
	slog.Info("ai: auto routed turn", "chat_id", c.ID, "fix_turn", fixTurn, "model_id", choice.ModelID, "effort", choice.Effort)
	return choice, nil
}
