package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"rago/internal/domain"
)

// Service is the port this handler depends on — defined here to keep transport
// decoupled from the concrete service package.
type Service interface {
	IngestFolder(ctx context.Context, folder string) (int, error)
	Query(ctx context.Context, text string, k int) ([]domain.Document, error)
	Chat(ctx context.Context, question string, k int) (domain.ChatResponse, error)
	ListUploads(ctx context.Context, page, limit int) (domain.UploadPage, error)
	Reset(ctx context.Context) error
}

type Handler struct {
	svc       Service
	uploadDir string
}

func New(svc Service) *Handler {
	dir := os.Getenv("UPLOAD_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "rago-uploads")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("could not create upload dir", "dir", dir, "error", err)
	}
	return &Handler{svc: svc, uploadDir: dir}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v1/ingest", h.ingest)
	mux.HandleFunc("/v1/upload", h.upload)
	mux.HandleFunc("/v1/uploads", h.listUploads)
	mux.HandleFunc("/v1/query", h.query)
	mux.HandleFunc("/v1/chat", h.chat)
	mux.HandleFunc("/v1/reset", h.reset)
}

// CORS wraps the mux so every route gets the headers.
func (h *Handler) CORS(mux *http.ServeMux) http.Handler {
	return corsMiddleware(mux)
}

func (h *Handler) listUploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	limit := queryInt(r, "limit", 5)
	if limit < 1 || limit > 100 {
		limit = 5
	}

	result, err := h.svc.ListUploads(r.Context(), page, limit)
	if err != nil {
		slog.Error("list uploads failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "failed to parse multipart form", http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "no files provided", http.StatusBadRequest)
		return
	}

	var saved []string
	for _, fh := range files {
		ext := filepath.Ext(fh.Filename)
		if ext != ".pdf" && ext != ".txt" {
			http.Error(w, fmt.Sprintf("unsupported file type: %s", ext), http.StatusBadRequest)
			return
		}
		src, err := fh.Open()
		if err != nil {
			http.Error(w, "failed to open uploaded file", http.StatusInternalServerError)
			return
		}
		dst, err := os.Create(filepath.Join(h.uploadDir, fh.Filename))
		if err != nil {
			src.Close()
			http.Error(w, "failed to save file", http.StatusInternalServerError)
			return
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			http.Error(w, "failed to write file", http.StatusInternalServerError)
			return
		}
		saved = append(saved, fh.Filename)
		slog.Info("file saved", "filename", fh.Filename, "dir", h.uploadDir)
	}

	count, err := h.svc.IngestFolder(r.Context(), h.uploadDir)

	// Remove uploaded files regardless of ingest outcome so that a subsequent
	// upload (or post-reset upload) never re-processes stale files.
	for _, name := range saved {
		if rmErr := os.Remove(filepath.Join(h.uploadDir, name)); rmErr != nil {
			slog.Warn("could not remove uploaded file", "filename", name, "error", rmErr)
		}
	}

	if err != nil {
		slog.Error("ingest after upload failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("upload+ingest complete", "files", saved, "ingested", count)
	writeJSON(w, http.StatusOK, map[string]any{"files": saved, "ingested": count})
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

func queryInt(r *http.Request, key string, fallback int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("encode response failed", "error", err)
	}
}
