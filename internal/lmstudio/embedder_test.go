package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func embeddingResponse(vectors []float32) any {
	return map[string]any{
		"data": []map[string]any{
			{"embedding": vectors},
		},
	}
}

func TestEmbedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected JSON content-type")
		}
		json.NewEncoder(w).Encode(embeddingResponse([]float32{0.1, 0.2, 0.3}))
	}))
	defer srv.Close()

	e := NewEmbedder(srv.URL, "")
	emb, err := e.Embed(context.Background(), "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emb) != 3 {
		t.Errorf("expected 3 floats, got %d", len(emb))
	}
}

func TestEmbedSendsModelWhenSet(t *testing.T) {
	var receivedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		receivedModel, _ = body["model"].(string)
		json.NewEncoder(w).Encode(embeddingResponse([]float32{0.1}))
	}))
	defer srv.Close()

	NewEmbedder(srv.URL, "nomic-embed").Embed(context.Background(), "test")
	if receivedModel != "nomic-embed" {
		t.Errorf("expected model 'nomic-embed', got %q", receivedModel)
	}
}

func TestEmbedOmitsModelWhenEmpty(t *testing.T) {
	var bodyKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for k := range body {
			bodyKeys = append(bodyKeys, k)
		}
		json.NewEncoder(w).Encode(embeddingResponse([]float32{0.1}))
	}))
	defer srv.Close()

	NewEmbedder(srv.URL, "").Embed(context.Background(), "test")
	for _, k := range bodyKeys {
		if k == "model" {
			t.Error("model key should not be present when model is empty")
		}
	}
}

func TestEmbedNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"no model loaded"}`))
	}))
	defer srv.Close()

	_, err := NewEmbedder(srv.URL, "").Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestEmbedEmptyDataArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	_, err := NewEmbedder(srv.URL, "").Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty data array")
	}
}

func TestEmbedInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json at all"))
	}))
	defer srv.Close()

	_, err := NewEmbedder(srv.URL, "").Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestEmbedContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewEmbedder(srv.URL, "").Embed(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
