package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dylan-demolder/factory-/internal/config"
)

// OpenAI calls an OpenAI-compatible chat completions endpoint. It cannot
// touch the repository, so it is suited to interview, review and roundtables.
type OpenAI struct {
	name    string
	baseURL string
	model   string
	keyEnv  string
	client  *http.Client
}

func newOpenAI(name string, c config.Agent, timeout time.Duration) *OpenAI {
	return &OpenAI{
		name:    name,
		baseURL: strings.TrimRight(c.BaseURL, "/"),
		model:   c.Model,
		keyEnv:  c.APIKeyEnv,
		client:  &http.Client{Timeout: timeout},
	}
}

func (o *OpenAI) Name() string { return o.name }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (o *OpenAI) Run(ctx context.Context, req Request) (string, error) {
	var msgs []chatMessage
	if req.System != "" {
		msgs = append(msgs, chatMessage{"system", req.System})
	}
	msgs = append(msgs, chatMessage{"user", req.Prompt})
	body, _ := json.Marshal(map[string]any{"model": o.model, "messages": msgs})

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.keyEnv != "" {
		if key := os.Getenv(o.keyEnv); key != "" {
			hreq.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := o.client.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", o.name, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("agent %s: HTTP %d: %s", o.name, resp.StatusCode, Tail(string(data), 1000))
	}
	var parsed struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("agent %s: bad response: %w", o.name, err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("agent %s: empty response", o.name)
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}
