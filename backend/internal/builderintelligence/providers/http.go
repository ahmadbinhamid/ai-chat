package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-chat/internal/builderintelligence/prompts"
)

// HTTP calls an OpenAI-compatible local endpoint (llama.cpp server).
// Extracts SemanticRefinement only — tiny input, tiny max_tokens.
type HTTP struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (p HTTP) Name() string { return "http_local_lm" }

func (p HTTP) Extract(ctx context.Context, in Input) (SemanticRefinement, error) {
	if strings.TrimSpace(p.BaseURL) == "" {
		return SemanticRefinement{}, fmt.Errorf("%w: empty base URL", ErrUnavailable)
	}
	client := p.HTTPClient
	if client == nil {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = 1500 * time.Millisecond
		}
		client = &http.Client{Timeout: timeout}
	}
	model := p.Model
	if model == "" {
		model = "qwen2.5-0.5b-instruct"
	}

	userPayload, _ := json.Marshal(map[string]any{
		"prompt": in.Prompt,
		"plan":   in.Plan,
	})

	bodyMap := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": prompts.SystemSemanticExtract},
			{"role": "user", "content": string(userPayload)},
		},
		"temperature": 0,
		"max_tokens":  160,
		"json_schema": json.RawMessage(prompts.SemanticJSONSchema),
	}
	body, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return SemanticRefinement{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return SemanticRefinement{}, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return SemanticRefinement{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 400 || resp.StatusCode == 422 {
			return p.extractPlain(ctx, client, model, userPayload)
		}
		return SemanticRefinement{}, fmt.Errorf("%w: http %d", ErrUnavailable, resp.StatusCode)
	}

	ref, err := parseSemantic(raw)
	if err != nil {
		return SemanticRefinement{}, err
	}
	ref.Source = "http_local_lm"
	return ref, nil
}

func (p HTTP) extractPlain(ctx context.Context, client *http.Client, model string, userPayload []byte) (SemanticRefinement, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": prompts.SystemSemanticExtract},
			{"role": "user", "content": string(userPayload)},
		},
		"temperature": 0,
		"max_tokens":  160,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return SemanticRefinement{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return SemanticRefinement{}, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return SemanticRefinement{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SemanticRefinement{}, fmt.Errorf("%w: http %d", ErrUnavailable, resp.StatusCode)
	}
	ref, err := parseSemantic(raw)
	if err != nil {
		return SemanticRefinement{}, err
	}
	ref.Source = "http_local_lm"
	return ref, nil
}

func parseSemantic(raw []byte) (SemanticRefinement, error) {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return SemanticRefinement{}, fmt.Errorf("builderintelligence: local LM envelope: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return SemanticRefinement{}, fmt.Errorf("builderintelligence: local LM empty choices")
	}
	content := stripJSONFence(strings.TrimSpace(envelope.Choices[0].Message.Content))
	var ref SemanticRefinement
	if err := json.Unmarshal([]byte(content), &ref); err != nil {
		return SemanticRefinement{}, fmt.Errorf("builderintelligence: invalid local LM JSON: %w", err)
	}
	return ref, nil
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```JSON")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}
