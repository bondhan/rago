package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rago/internal/domain"
	"rago/internal/handler"
)

// ── mock service ──────────────────────────────────────────────────────────────

type mockService struct {
	ingestFn      func(ctx context.Context, folder string) (int, error)
	queryFn       func(ctx context.Context, text string, k int) ([]domain.Document, error)
	chatFn        func(ctx context.Context, question string, k int) (domain.ChatResponse, error)
	listUploadsFn func(ctx context.Context, page, limit int) (domain.UploadPage, error)
	resetFn       func(ctx context.Context) error
}

func (m *mockService) IngestFolder(ctx context.Context, folder string) (int, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, folder)
	}
	return 0, nil
}
func (m *mockService) Query(ctx context.Context, text string, k int) ([]domain.Document, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, text, k)
	}
	return nil, nil
}
func (m *mockService) Chat(ctx context.Context, question string, k int) (domain.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, question, k)
	}
	return domain.ChatResponse{}, nil
}
func (m *mockService) ListUploads(ctx context.Context, page, limit int) (domain.UploadPage, error) {
	if m.listUploadsFn != nil {
		return m.listUploadsFn(ctx, page, limit)
	}
	return domain.UploadPage{Items: []domain.UploadRecord{}, Total: 0, Page: page, Limit: limit}, nil
}
func (m *mockService) Reset(ctx context.Context) error {
	if m.resetFn != nil {
		return m.resetFn(ctx)
	}
	return nil
}

func newServer(svc handler.Service) *httptest.Server {
	mux := http.NewServeMux()
	handler.New(svc).Register(mux)
	return httptest.NewServer(mux)
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	return resp
}

// ── /ingest ───────────────────────────────────────────────────────────────────

func TestIngestMethodNotAllowed(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/v1/ingest?folder=/tmp")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestIngestMissingFolder(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/ingest", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestIngestSuccess(t *testing.T) {
	srv := newServer(&mockService{
		ingestFn: func(_ context.Context, folder string) (int, error) {
			if folder != "/docs" {
				t.Errorf("unexpected folder: %s", folder)
			}
			return 3, nil
		},
	})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/ingest?folder=/docs", "application/json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]int
	json.NewDecoder(resp.Body).Decode(&body)
	if body["ingested"] != 3 {
		t.Errorf("expected ingested=3, got %d", body["ingested"])
	}
}

func TestIngestServiceError(t *testing.T) {
	srv := newServer(&mockService{
		ingestFn: func(_ context.Context, _ string) (int, error) {
			return 0, errors.New("disk error")
		},
	})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/ingest?folder=/docs", "application/json", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// ── /query ────────────────────────────────────────────────────────────────────

func TestQueryMethodNotAllowed(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/v1/query")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestQueryInvalidJSON(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/query", "application/json", strings.NewReader("not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestQueryMissingQueryField(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/query", map[string]any{"k": 5})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestQuerySuccess(t *testing.T) {
	docs := []domain.Document{{Filename: "f.txt", Chunk: "chunk", Score: 0.9}}
	srv := newServer(&mockService{
		queryFn: func(_ context.Context, _ string, _ int) ([]domain.Document, error) {
			return docs, nil
		},
	})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/query", map[string]any{"query": "test question", "k": 3})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string][]domain.Document
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result["results"]) != 1 {
		t.Errorf("expected 1 result, got %d", len(result["results"]))
	}
}

func TestQueryDefaultKIsFive(t *testing.T) {
	var receivedK int
	srv := newServer(&mockService{
		queryFn: func(_ context.Context, _ string, k int) ([]domain.Document, error) {
			receivedK = k
			return nil, nil
		},
	})
	defer srv.Close()

	postJSON(t, srv.URL+"/v1/query", map[string]any{"query": "test"})
	if receivedK != 5 {
		t.Errorf("expected default k=5, got %d", receivedK)
	}
}

func TestQueryServiceError(t *testing.T) {
	srv := newServer(&mockService{
		queryFn: func(_ context.Context, _ string, _ int) ([]domain.Document, error) {
			return nil, errors.New("db error")
		},
	})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/query", map[string]any{"query": "test"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// ── /v1/chat ──────────────────────────────────────────────────────────────────

func TestChatMethodNotAllowed(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/v1/chat")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestChatInvalidJSON(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/chat", "application/json", strings.NewReader("bad json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestChatMissingMessageField(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/chat", map[string]any{"k": 3})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestChatSuccess(t *testing.T) {
	chatResp := domain.ChatResponse{
		Answer:  "The answer",
		Sources: []domain.Document{{Filename: "doc.pdf", Chunk: "ctx", Score: 0.9}},
	}
	srv := newServer(&mockService{
		chatFn: func(_ context.Context, _ string, _ int) (domain.ChatResponse, error) {
			return chatResp, nil
		},
	})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/chat", map[string]any{"message": "question?", "k": 3})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var result domain.ChatResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Answer != "The answer" {
		t.Errorf("unexpected answer: %s", result.Answer)
	}
	if len(result.Sources) != 1 {
		t.Errorf("expected 1 source, got %d", len(result.Sources))
	}
}

func TestChatDefaultKIsFive(t *testing.T) {
	var receivedK int
	srv := newServer(&mockService{
		chatFn: func(_ context.Context, _ string, k int) (domain.ChatResponse, error) {
			receivedK = k
			return domain.ChatResponse{}, nil
		},
	})
	defer srv.Close()

	postJSON(t, srv.URL+"/v1/chat", map[string]any{"message": "q"})
	if receivedK != 5 {
		t.Errorf("expected default k=5, got %d", receivedK)
	}
}

func TestChatServiceError(t *testing.T) {
	srv := newServer(&mockService{
		chatFn: func(_ context.Context, _ string, _ int) (domain.ChatResponse, error) {
			return domain.ChatResponse{}, errors.New("llm error")
		},
	})
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/chat", map[string]any{"message": "q"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// ── /v1/reset ─────────────────────────────────────────────────────────────────

func TestResetMethodNotAllowed(t *testing.T) {
	srv := newServer(&mockService{})
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/reset", "application/json", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestResetSuccess(t *testing.T) {
	called := false
	srv := newServer(&mockService{
		resetFn: func(_ context.Context) error {
			called = true
			return nil
		},
	})
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/reset", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !called {
		t.Error("expected Reset to be called on service")
	}
}

func TestResetServiceError(t *testing.T) {
	srv := newServer(&mockService{
		resetFn: func(_ context.Context) error {
			return errors.New("db error")
		},
	})
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/reset", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}
