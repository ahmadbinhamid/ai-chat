// Package aicatalog is the AI model catalogue: which models a merchant may pick, what each can do, and which provider
// serves it. Pure: callers read the file and pass the environment lookup, so nothing here touches disk or network.
package aicatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// AutoID is the reserved choice that routes each turn to the design or fix model.
const AutoID = "auto"

// validEfforts are the output_config.effort values the provider API accepts.
var validEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// Provider is where a model is served from. APIKeyEnv names the environment variable holding the key; keys never sit
// in the catalogue file.
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	// Options are extra request-body fields sent with every model of this provider (e.g. OpenRouter routing).
	Options json.RawMessage `json:"options,omitempty"`
	// ModelsURL, when set, is a model list ({"data":[{"id":...}]}) every model name is checked against at startup.
	ModelsURL string `json:"models_url,omitempty"`
}

// Model is one catalogue entry. ID is what the dashboard sends and sees; Model is the provider's own name, never exposed.
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Images        bool     `json:"images"`
	Thinking      bool     `json:"thinking"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
	// Selectable defaults to true; internal-only models (vision, a separate summary model) set it false.
	Selectable *bool `json:"selectable,omitempty"`
	// Options are extra request-body fields for this model, deep-merged over its provider's.
	Options json.RawMessage `json:"options,omitempty"`
}

// IsSelectable reports whether the model appears in the merchant's picker.
func (m Model) IsSelectable() bool { return m.Selectable == nil || *m.Selectable }

// AllowsEffort reports whether effort is one this model offers.
func (m Model) AllowsEffort(effort string) bool { return slices.Contains(m.Efforts, effort) }

// Auto routes fix-type turns to FixModel and everything else to DesignModel.
type Auto struct {
	Label        string `json:"label"`
	DesignModel  string `json:"design_model"`
	DesignEffort string `json:"design_effort"`
	FixModel     string `json:"fix_model"`
	FixEffort    string `json:"fix_effort"`
}

// Catalog is a validated catalogue; build it with Parse or FromEnv, never by hand.
type Catalog struct {
	Providers    map[string]Provider `json:"providers"`
	Models       []Model             `json:"models"`
	Auto         *Auto               `json:"auto,omitempty"`
	DefaultModel string              `json:"default_model"`
	VisionModel  string              `json:"vision_model,omitempty"`
	SummaryModel string              `json:"summary_model"`

	byID   map[string]Model
	fields map[string]map[string]any
}

// Choice is a resolved pick: a concrete catalogue model id (never AutoID) and an effort it allows ("" when it has none).
type Choice struct {
	ModelID string
	Effort  string
}

// Parse decodes and validates a catalogue file. lookupEnv resolves each provider's key variable; a provider whose key
// is unset is an error, so a misconfigured deploy fails at startup instead of on a merchant's request.
func Parse(data []byte, lookupEnv func(string) (string, bool)) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("model catalogue: %w", err)
	}
	if err := c.validate(lookupEnv); err != nil {
		return nil, fmt.Errorf("model catalogue: %w", err)
	}
	return &c, nil
}

// FromEnv builds the one-model catalogue a deploy without a catalogue file has always had: AI_MODEL at AI_EFFORT, with
// AI_VISION_MODEL (when set) for image turns. Behaviour matches the pre-catalogue generator exactly.
func FromEnv(apiKey, baseURL, model, effort, visionModel string) (*Catalog, error) {
	const keyEnv = "AI_API_KEY"
	c := Catalog{
		Providers: map[string]Provider{"default": {BaseURL: baseURL, APIKeyEnv: keyEnv}},
		Models: []Model{{
			ID: "default", Label: "Default", Provider: "default", Model: model,
			Thinking: true, Efforts: []string{effort}, DefaultEffort: effort,
		}},
		DefaultModel: "default",
		SummaryModel: "default",
	}
	if visionModel != "" {
		hidden := false
		c.Models = append(c.Models, Model{
			ID: "vision", Label: "Vision", Provider: "default", Model: visionModel, Images: true,
			Thinking: true, Efforts: []string{effort}, DefaultEffort: effort, Selectable: &hidden,
		})
		c.VisionModel = "vision"
	}
	lookup := func(name string) (string, bool) {
		if name == keyEnv && apiKey != "" {
			return apiKey, true
		}
		return "", false
	}
	if err := c.validate(lookup); err != nil {
		return nil, fmt.Errorf("model settings from environment: %w", err)
	}
	return &c, nil
}

func (c *Catalog) validate(lookupEnv func(string) (string, bool)) error {
	if len(c.Providers) == 0 {
		return errors.New("no providers")
	}
	for name, p := range c.Providers {
		if p.APIKeyEnv == "" {
			return fmt.Errorf("provider %q: api_key_env is required", name)
		}
		if key, ok := lookupEnv(p.APIKeyEnv); !ok || key == "" {
			return fmt.Errorf("provider %q: environment variable %s is not set", name, p.APIKeyEnv)
		}
	}
	if len(c.Models) == 0 {
		return errors.New("no models")
	}
	c.byID = make(map[string]Model, len(c.Models))
	for _, m := range c.Models {
		if err := validateModel(m, c.Providers); err != nil {
			return err
		}
		if _, dup := c.byID[m.ID]; dup {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		c.byID[m.ID] = m
	}

	if c.DefaultModel == AutoID {
		if c.Auto == nil {
			return errors.New(`default_model is "auto" but there is no auto entry`)
		}
	} else if m, ok := c.byID[c.DefaultModel]; !ok || !m.IsSelectable() {
		return fmt.Errorf("default_model %q is not a selectable model", c.DefaultModel)
	}
	if c.Auto != nil {
		if err := c.validateAutoTarget("design", c.Auto.DesignModel, c.Auto.DesignEffort); err != nil {
			return err
		}
		if err := c.validateAutoTarget("fix", c.Auto.FixModel, c.Auto.FixEffort); err != nil {
			return err
		}
	}
	if c.VisionModel != "" {
		m, ok := c.byID[c.VisionModel]
		if !ok {
			return fmt.Errorf("vision_model %q is not in the model list", c.VisionModel)
		}
		if !m.Images {
			return fmt.Errorf("vision_model %q does not have \"images\": true", c.VisionModel)
		}
	}
	if _, ok := c.byID[c.SummaryModel]; !ok {
		return fmt.Errorf("summary_model %q is not in the model list", c.SummaryModel)
	}
	return c.buildRequestFields()
}

func validateModel(m Model, providers map[string]Provider) error {
	switch {
	case m.ID == "":
		return errors.New("a model has no id")
	case m.ID == AutoID:
		return fmt.Errorf("model id %q is reserved", AutoID)
	case m.Label == "":
		return fmt.Errorf("model %q: label is required", m.ID)
	case m.Model == "":
		return fmt.Errorf("model %q: model is required", m.ID)
	}
	if _, ok := providers[m.Provider]; !ok {
		return fmt.Errorf("model %q: unknown provider %q", m.ID, m.Provider)
	}
	// Effort is a thinking control, so a model without thinking gets neither parameter.
	if !m.Thinking {
		if len(m.Efforts) > 0 || m.DefaultEffort != "" {
			return fmt.Errorf("model %q: efforts need \"thinking\": true", m.ID)
		}
		return nil
	}
	if len(m.Efforts) == 0 {
		return fmt.Errorf("model %q: a thinking model needs at least one effort", m.ID)
	}
	for _, e := range m.Efforts {
		if !slices.Contains(validEfforts, e) {
			return fmt.Errorf("model %q: unknown effort %q", m.ID, e)
		}
	}
	if !m.AllowsEffort(m.DefaultEffort) {
		return fmt.Errorf("model %q: default_effort %q is not in its efforts", m.ID, m.DefaultEffort)
	}
	return nil
}

func (c *Catalog) validateAutoTarget(role, modelID, effort string) error {
	m, ok := c.byID[modelID]
	if !ok {
		return fmt.Errorf("auto %s_model %q is not in the model list", role, modelID)
	}
	if effort != "" && !m.AllowsEffort(effort) {
		return fmt.Errorf("auto %s_effort %q is not offered by %q", role, effort, modelID)
	}
	return nil
}

// Model returns the entry for id.
func (c *Catalog) Model(id string) (Model, bool) {
	m, ok := c.byID[id]
	return m, ok
}

// Provider returns the provider a model is served from.
func (c *Catalog) Provider(name string) (Provider, bool) {
	p, ok := c.Providers[name]
	return p, ok
}
