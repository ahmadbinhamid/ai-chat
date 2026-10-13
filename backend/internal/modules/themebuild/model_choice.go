package themebuild

import (
	"context"
	"fmt"
	"log/slog"

	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/pageintent"
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

// classifyTurn reports whether a turn is a fix (spotted with the chat's earlier prompts, for a bare follow-up) or a
// redesign, never both. A merchant's words reporting a bug make it a fix; captured preview errors alone don't override
// words asking for a redesign, since the dashboard attaches them to nearly every message.
func (s *Service) classifyTurn(ctx context.Context, in GenerateInput, c chat.Chat, hasPreviewErrors, hasReference bool) (fixTurn, redesign bool, err error) {
	prior, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return false, false, fmt.Errorf("load chat history for turn routing: %w", err)
	}
	earlier := earlierUserPrompts(prior, "")
	if pageintent.DetectRedesign(in.Prompt, hasReference) && !previewerrors.IsFixTurn(in.Prompt, earlier, false) {
		return false, true, nil
	}
	return previewerrors.IsFixTurn(in.Prompt, earlier, hasPreviewErrors), false, nil
}

// resolveModel picks this turn's concrete model, and whether thinking is off for it.
func (s *Service) resolveModel(c chat.Chat, sel aicatalog.Selection, fixTurn, redesign bool) (aicatalog.Choice, bool) {
	if s.models == nil {
		return aicatalog.Choice{}, false
	}
	choice, thinkingOff := s.models.ResolveTurn(sel, fixTurn, redesign)
	if sel.ModelID == aicatalog.AutoID {
		slog.Info("ai: auto routed turn", "chat_id", c.ID, "fix_turn", fixTurn, "redesign", redesign, "model_id", choice.ModelID,
			"effort", choice.Effort, "thinking_off", thinkingOff)
	}
	return choice, thinkingOff
}
