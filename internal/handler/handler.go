package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"rago/internal/domain"
)

// Service is the port this handler depends on — defined here to keep transport
// decoupled from the concrete service package.
type Service interface {
	IngestFolder(ctx context.Context, folder string) (int, error)
	Query(ctx context.Context, text string, k int) ([]domain.Document, error)
	Chat(ctx context.Context, question string, k int) (domain.ChatResponse, error)
	Reset(ctx context.Context) error
}

type Handler struct {
	svc Service
}

func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/ingest", h.ingest)
	mux.HandleFunc("/query", h.query)
	mux.HandleFunc("/v1/chat", h.chat)
	mux.HandleFunc("/v1/reset", h.reset)
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "folder query param required", http.StatusBadRequest)
		return
	}
	slog.Debug("ingest request", "folder", folder)

	count, err := h.svc.IngestFolder(r.Context(), folder)
	if err != nil {
		slog.Error("ingest failed", "folder", folder, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("ingest complete", "folder", folder, "count", count)
	writeJSON(w, http.StatusOK, map[string]int{"ingested": count})
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Query == "" {
		http.Error(w, "query field required", http.StatusBadRequest)
		return
	}
	if req.K <= 0 {
		req.K = 5
	}
	slog.Debug("query request", "query", req.Query, "k", req.K)

	results, err := h.svc.Query(r.Context(), req.Query, req.K)
	if err != nil {
		slog.Error("query failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("query complete", "results", len(results))
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message string `json:"message"`
		K       int    `json:"k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message field required", http.StatusBadRequest)
		return
	}
	if req.K <= 0 {
		req.K = 5
	}
	slog.Debug("chat request", "message", req.Message, "k", req.K)

	resp, err := h.svc.Chat(r.Context(), req.Message, req.K)
	if err != nil {
		slog.Error("chat failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("chat complete", "sources", len(resp.Sources))
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.svc.Reset(r.Context()); err != nil {
		slog.Error("reset failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("database reset")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("encode response failed", "error", err)
	}
}
