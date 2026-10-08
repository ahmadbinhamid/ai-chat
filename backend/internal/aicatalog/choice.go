package aicatalog

import (
	"errors"
	"fmt"
)

// Errors a merchant's request can trigger; handlers map them to 400.
var (
	ErrUnknownModel       = errors.New("unknown model")
	ErrEffortNotAllowed   = errors.New("effort not offered by this model")
	ErrEffortWithAuto     = errors.New("effort can't be set when the model is auto")
	ErrModelNotSelectable = errors.New("model is not selectable")
)

// Selection is what the merchant asked for, after validation: a selectable model id or AutoID, and an effort.
type Selection struct {
	ModelID string
	Effort  string
}

// Select validates a request's model and effort; empty fields fall back to default_model and its default_effort.
// Only catalogue ids are accepted, so an arbitrary provider model name can never reach a provider.
func (c *Catalog) Select(modelID, effort string) (Selection, error) {
	if modelID == "" {
		modelID = c.DefaultModel
	}
	if modelID == AutoID {
		if c.Auto == nil {
			return Selection{}, fmt.Errorf("%w: %q", ErrUnknownModel, modelID)
		}
		if effort != "" {
			return Selection{}, ErrEffortWithAuto
		}
		return Selection{ModelID: AutoID}, nil
	}
	m, ok := c.byID[modelID]
	if !ok {
		return Selection{}, fmt.Errorf("%w: %q", ErrUnknownModel, modelID)
	}
	if !m.IsSelectable() {
		return Selection{}, fmt.Errorf("%w: %q", ErrModelNotSelectable, modelID)
	}
	if effort == "" {
		effort = m.DefaultEffort
	}
	if effort != "" && !m.AllowsEffort(effort) {
		return Selection{}, fmt.Errorf("%w: %q for %q", ErrEffortNotAllowed, effort, modelID)
	}
	return Selection{ModelID: modelID, Effort: effort}, nil
}

// Resolve turns a selection into the concrete model this turn uses: auto picks fix_model for a fix-type turn and
// design_model otherwise.
func (c *Catalog) Resolve(s Selection, fixTurn bool) Choice {
	if s.ModelID != AutoID {
		return Choice(s)
	}
	id, effort := c.Auto.DesignModel, c.Auto.DesignEffort
	if fixTurn {
		id, effort = c.Auto.FixModel, c.Auto.FixEffort
	}
	if effort == "" {
		effort = c.byID[id].DefaultEffort
	}
	return Choice{ModelID: id, Effort: effort}
}

// Default is the choice for a turn with nothing stored (a row queued before choices existed, or a test).
func (c *Catalog) Default() Choice {
	return c.Resolve(Selection{ModelID: c.DefaultModel, Effort: c.byID[c.DefaultModel].DefaultEffort}, false)
}

// ForImages returns the choice an image turn uses: unchanged when the model sees images, else vision_model at the
// requested effort if it offers it, otherwise at its default. ok is false when no model can see images.
func (c *Catalog) ForImages(ch Choice) (out Choice, switched, ok bool) {
	if m, found := c.byID[ch.ModelID]; found && m.Images {
		return ch, false, true
	}
	v, found := c.byID[c.VisionModel]
	if !found {
		return ch, false, false
	}
	effort := ch.Effort
	if !v.AllowsEffort(effort) {
		effort = v.DefaultEffort
	}
	return Choice{ModelID: v.ID, Effort: effort}, true, true
}

// Summary is the choice for background history summaries: summary_model, never the merchant's pick.
func (c *Catalog) Summary() Choice {
	return Choice{ModelID: c.SummaryModel, Effort: c.byID[c.SummaryModel].DefaultEffort}
}

// SupportsImages reports whether any turn with an image can be served.
func (c *Catalog) SupportsImages() bool { return c.VisionModel != "" }

// Label returns a model's merchant-facing label, AutoID's included; "" for an unknown id.
func (c *Catalog) Label(id string) string {
	if id == AutoID && c.Auto != nil {
		return c.Auto.Label
	}
	return c.byID[id].Label
}

// PublicModel is one picker entry. It carries no provider, base URL or provider model name.
type PublicModel struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Images        bool     `json:"images"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}

// PublicAuto is the auto picker entry; it has no efforts because auto chooses them.
type PublicAuto struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Public is GET /models: only what the dashboard needs to offer a choice.
type Public struct {
	Models       []PublicModel `json:"models"`
	Auto         *PublicAuto   `json:"auto"`
	DefaultModel string        `json:"default_model"`
}

// Public lists the selectable models in catalogue order.
func (c *Catalog) Public() Public {
	out := Public{Models: []PublicModel{}, DefaultModel: c.DefaultModel}
	for _, m := range c.Models {
		if !m.IsSelectable() {
			continue
		}
		efforts := m.Efforts
		if efforts == nil {
			efforts = []string{}
		}
		out.Models = append(out.Models, PublicModel{ID: m.ID, Label: m.Label, Images: m.Images, Efforts: efforts, DefaultEffort: m.DefaultEffort})
	}
	if c.Auto != nil {
		out.Auto = &PublicAuto{ID: AutoID, Label: c.Auto.Label}
	}
	return out
}
