package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Zenyet/banyan/internal/i18n"
)

const commitSystem = "You write concise, accurate Git commit messages. You reply with the message alone, never with commentary."

// generateHTTP asks an OpenAI or Anthropic style API for the message.
func generateHTTP(ctx context.Context, cfg Config, prompt string) (string, error) {
	switch cfg.Provider {
	case ProviderAnthropic:
		return anthropic(ctx, cfg, commitSystem, prompt)
	default:
		return openAI(ctx, cfg, commitSystem, prompt)
	}
}

// checkHTTP sends a small request, to tell a wrong key, model or URL from a
// working setup.
func checkHTTP(ctx context.Context, cfg Config) (string, error) {
	const system = "You are a connection test. Reply with the single word OK."
	var text string
	var err error
	if cfg.Provider == ProviderAnthropic {
		text, err = anthropic(ctx, cfg, system, "Reply with OK.")
	} else {
		text, err = openAI(ctx, cfg, system, "Reply with OK.")
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New(i18n.T("The provider returned no message"))
	}
	return cfg.Provider.Label() + " · " + cfg.Model, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func openAI(ctx context.Context, cfg Config, system, prompt string) (string, error) {
	base := cfg.BaseURL
	if strings.TrimSpace(base) == "" {
		base = "https://api.openai.com/v1"
	}
	body := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}{
		Model: cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	headers := map[string]string{}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	if err := postJSON(ctx, endpoint(base, "/chat/completions"), body, headers, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New(i18n.T("The provider returned no message"))
	}
	return result.Choices[0].Message.Content, nil
}

func anthropic(ctx context.Context, cfg Config, system, prompt string) (string, error) {
	base := cfg.BaseURL
	if strings.TrimSpace(base) == "" {
		base = "https://api.anthropic.com"
	}
	body := struct {
		Model     string        `json:"model"`
		MaxTokens int           `json:"max_tokens"`
		System    string        `json:"system"`
		Messages  []chatMessage `json:"messages"`
	}{
		Model:     cfg.Model,
		MaxTokens: 1024,
		System:    system,
		Messages:  []chatMessage{{Role: "user", Content: prompt}},
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		headers["x-api-key"] = key
	}
	if err := postJSON(ctx, endpoint(base, "/v1/messages"), body, headers, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New(i18n.T("The provider returned no message"))
	}
	return b.String(), nil
}

// postJSON posts body and decodes the response into result, turning an
// error response into the provider's own words.
func postJSON(ctx context.Context, url string, body any, headers map[string]string, result any) error {
	if headers == nil {
		headers = map[string]string{}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return providerError(resp.StatusCode, text)
	}
	if err := json.Unmarshal(text, result); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("The provider's response could not be read"), err)
	}
	return nil
}

// providerError is an error response as the provider describes it.
func providerError(status int, body []byte) error {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &parsed)
	message := strings.TrimSpace(parsed.Error.Message)
	if message == "" {
		message = strings.TrimSpace(parsed.Message)
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if len(message) > 300 {
		message = message[:300] + "…"
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("%s (HTTP %d)", message, status)
}

// endpoint is base with suffix, unless base already is the full endpoint.
func endpoint(base, suffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, suffix) {
		return base
	}
	return base + suffix
}
