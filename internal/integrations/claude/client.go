package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const apiURL = "https://api.anthropic.com/v1/messages"

type Client struct {
	apiKey string
	http   *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 90 * time.Second},
	}
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []apiMessage `json:"messages"`
}

type apiResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(apiRequest{
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 2048,
		Messages:  []apiMessage{{Role: "user", Content: prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var r apiResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("claude parse: %w", err)
	}
	if r.Error != nil {
		return "", fmt.Errorf("claude API: %s", r.Error.Message)
	}
	if len(r.Content) == 0 {
		return "", fmt.Errorf("claude: empty response")
	}
	return r.Content[0].Text, nil
}
