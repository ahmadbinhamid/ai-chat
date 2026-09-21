package themebuild

import (
	"context"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderoperations"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

// isRegisterExistingPagePrompt reports whether the prompt is a deterministic
// register-existing-page request (delegates to builderoperations).
func isRegisterExistingPagePrompt(prompt string) bool {
	return builderoperations.MatchesRegisterExistingPrompt(prompt)
}

// buildDeterministicRegisterExisting runs the local register_existing_page
// operation — no DeepSeek, no local ML, no repair, no retry.
// ok=false means fall through to the model.
func buildDeterministicRegisterExisting(
	ctx context.Context,
	store themefs.ThemeStore,
	auth themefs.RequestAuth,
	prompt string,
) (result *ai.Result, ok bool, err error) {
	aiRes, _, handled, err := runLocalRegisterExisting(ctx, store, auth, prompt, nil)
	return aiRes, handled, err
}

func runLocalRegisterExisting(
	ctx context.Context,
	store themefs.ThemeStore,
	auth themefs.RequestAuth,
	prompt string,
	plan *builderplan.BuilderPlan,
) (aiResult *ai.Result, opResult builderoperations.Result, handled bool, err error) {
	name, resolved := builderoperations.Resolve(prompt, plan)
	if !resolved {
		return nil, opResult, false, nil
	}
	out, err := builderoperations.Run(ctx, name, builderoperations.Input{
		Prompt: prompt,
		Plan:   plan,
		Store:  store,
		Auth:   auth,
	})
	if err != nil {
		return nil, out, false, err
	}
	if out.Outcome == builderoperations.OutcomeNotApplicable {
		return nil, out, false, nil
	}
	return localOpToAIResult(out), out, true, nil
}

func localOpToAIResult(out builderoperations.Result) *ai.Result {
	r := &ai.Result{
		Summary:            out.UserMessage,
		NeedsClarification: out.NeedsClarification,
		PageRegistryEntry:  out.PageEntry,
	}
	if len(out.Files) > 0 {
		r.Files = make([]ai.GeneratedFile, 0, len(out.Files))
		for _, f := range out.Files {
			r.Files = append(r.Files, ai.GeneratedFile{
				Path: f.Path, Action: f.Action, Content: f.Content,
			})
		}
	}
	return r
}
