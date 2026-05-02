package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"rago/internal/domain"
)

// ChatClient implements domain.ChatCompleter against the LM Studio
// /v1/chat/completions API.
type ChatClient struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewChatClient(baseURL, model string) *ChatClient {
	slog.Debug("chat client initialised", "url", baseURL, "model", model)
	return &ChatClient{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

func (c *ChatClient) Complete(ctx context.Context, messages []domain.Message) (string, error) {
	type reqBody struct {
		Model    string           `json:"model,omitempty"`
		Messages []domain.Message `json:"messages"`
		Stream   bool             `json:"stream"`
	}
	body, err := json.Marshal(reqBody{
		Model:    c.model,
		Messages: messages,
		Stream:   false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("chat API status %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from chat API")
	}
	return result.Choices[0].Message.Content, nil
}
