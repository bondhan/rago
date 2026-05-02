package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rago/internal/domain"
)

func chatResponse(content string) any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	}
}

func TestCompleteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(chatResponse("hello from LLM"))
	}))
	defer srv.Close()

	answer, err := NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "hello from LLM" {
		t.Errorf("unexpected answer: %q", answer)
	}
}

func TestCompleteSendsModelWhenSet(t *testing.T) {
	var receivedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		receivedModel, _ = body["model"].(string)
		json.NewEncoder(w).Encode(chatResponse("ok"))
	}))
	defer srv.Close()

	NewChatClient(srv.URL, "qwen-7b").Complete(context.Background(), []domain.Message{
		{Role: "user", Content: "test"},
	})
	if receivedModel != "qwen-7b" {
		t.Errorf("expected model 'qwen-7b', got %q", receivedModel)
	}
}

func TestCompleteSendsMessages(t *testing.T) {
	var receivedMessages []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if msgs, ok := body["messages"].([]any); ok {
			for _, m := range msgs {
				if msg, ok := m.(map[string]any); ok {
					receivedMessages = append(receivedMessages, msg)
				}
			}
		}
		json.NewEncoder(w).Encode(chatResponse("ok"))
	}))
	defer srv.Close()

	NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{
		{Role: "system", Content: "be helpful"},
		{Role: "user", Content: "what is go?"},
	})
	if len(receivedMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(receivedMessages))
	}
}

func TestCompleteStreamIsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if stream, _ := body["stream"].(bool); stream {
			t.Error("stream should be false")
		}
		json.NewEncoder(w).Encode(chatResponse("ok"))
	}))
	defer srv.Close()

	NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{{Role: "user", Content: "test"}})
}

func TestCompleteNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"model not loaded"}`))
	}))
	defer srv.Close()

	_, err := NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestCompleteEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer srv.Close()

	_, err := NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestCompleteInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	_, err := NewChatClient(srv.URL, "").Complete(context.Background(), []domain.Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCompleteContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewChatClient(srv.URL, "").Complete(ctx, []domain.Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
