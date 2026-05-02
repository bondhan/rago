package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// Embedder implements domain.Embedder against the LM Studio /v1/embeddings API.
type Embedder struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewEmbedder(baseURL, model string) *Embedder {
	slog.Debug("embedder initialised", "url", baseURL, "model", model)
	return &Embedder{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := map[string]any{"input": text}
	if e.model != "" {
		payload["model"] = e.model
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings API status %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return result.Data[0].Embedding, nil
}
