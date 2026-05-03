package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"rago/internal/domain"

	"github.com/ledongthuc/pdf"
	"golang.org/x/sync/errgroup"
)

// RAGService implements the core use cases: ingest, query, chat, reset.
type RAGService struct {
	repo     domain.Repository
	embedder domain.Embedder
	chat     domain.ChatCompleter
}

func NewRAGService(repo domain.Repository, embedder domain.Embedder, chat domain.ChatCompleter) *RAGService {
	return &RAGService{repo: repo, embedder: embedder, chat: chat}
}

// ingestWorkers returns the concurrency level for IngestFolder.
// Reads INGEST_WORKERS from the environment; defaults to 2 × runtime.NumCPU().
func ingestWorkers() int {
	if v := os.Getenv("INGEST_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2 * runtime.NumCPU()
}

// IngestFolder scans folder recursively, then processes eligible files
// concurrently using a worker pool sized by INGEST_WORKERS (default 2×CPU).
func (s *RAGService) IngestFolder(ctx context.Context, folder string) (int, error) {
	// Phase 1 — collect eligible file paths (sequential, cheap).
	var files []string
	if err := filepath.WalkDir(folder, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext == ".txt" || ext == ".pdf" {
			files = append(files, path)
		} else {
			slog.Debug("skipping unsupported file", "path", path)
		}
		return nil
	}); err != nil {
		return 0, err
	}

	if len(files) == 0 {
		return 0, nil
	}

	// Phase 2 — process files concurrently.
	workers := ingestWorkers()
	slog.Debug("starting concurrent ingest", "files", len(files), "workers", workers)

	jobs := make(chan string, workers)
	var count atomic.Int32

	g, gctx := errgroup.WithContext(ctx)
	for i := 0; i < workers; i++ {
		g.Go(func() error {
			for path := range jobs {
				if err := gctx.Err(); err != nil {
					return err
				}
				ingested, err := s.processFile(gctx, folder, path)
				if err != nil {
					return err
				}
				if ingested {
					count.Add(1)
				}
			}
			return nil
		})
	}

	// Feed jobs; stop early when context is cancelled.
sendLoop:
	for _, path := range files {
		select {
		case jobs <- path:
		case <-gctx.Done():
			break sendLoop
		}
	}
	close(jobs)

	if err := g.Wait(); err != nil {
		return int(count.Load()), err
	}
	return int(count.Load()), nil
}

// processFile handles the full pipeline for a single file: hash → dedup →
// extract → chunk → embed → store. Called concurrently from IngestFolder.
func (s *RAGService) processFile(ctx context.Context, folder, path string) (bool, error) {
	relPath, _ := filepath.Rel(folder, path)
	ext := strings.ToLower(filepath.Ext(path))

	hash, err := fileHash(path)
	if err != nil {
		return false, fmt.Errorf("hash %s: %w", relPath, err)
	}

	ingested, err := s.repo.IsIngested(ctx, hash)
	if err != nil {
		return false, fmt.Errorf("check ingested %s: %w", relPath, err)
	}
	if ingested {
		slog.Debug("skipping already ingested file", "file", relPath)
		return false, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", relPath, err)
	}
	sizeBytes := info.Size()

	slog.Info("ingesting file", "file", relPath)
	text, err := extractText(path, ext)
	if err != nil {
		return false, fmt.Errorf("extract %s: %w", relPath, err)
	}

	chunks := chunkText(text, 500, 100)
	slog.Debug("chunked file", "file", relPath, "chunks", len(chunks))

	for i, chunk := range chunks {
		slog.Debug("embedding chunk", "file", relPath, "chunk", i+1, "of", len(chunks))
		emb, err := s.embedder.Embed(ctx, chunk)
		if err != nil {
			return false, fmt.Errorf("embed %s chunk %d: %w", relPath, i+1, err)
		}
		if err := s.repo.StoreChunk(ctx, relPath, chunk, emb); err != nil {
			return false, fmt.Errorf("store %s chunk %d: %w", relPath, i+1, err)
		}
	}

	if err := s.repo.RecordFile(ctx, relPath, hash, sizeBytes); err != nil {
		return false, err
	}
	slog.Info("file ingested", "file", relPath, "chunks", len(chunks))
	return true, nil
}

func (s *RAGService) Query(ctx context.Context, text string, k int) ([]domain.Document, error) {
	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return s.repo.SearchSimilar(ctx, emb, k)
}

func (s *RAGService) ListUploads(ctx context.Context, page, limit int) (domain.UploadPage, error) {
	return s.repo.ListUploads(ctx, page, limit)
}

func (s *RAGService) Reset(ctx context.Context) error {
	return s.repo.Reset(ctx)
}

// Chat retrieves the top-k relevant chunks for the question, builds a
// grounded prompt, and returns the LLM answer alongside the source documents.
func (s *RAGService) Chat(ctx context.Context, question string, k int) (domain.ChatResponse, error) {
	sources, err := s.Query(ctx, question, k)
	if err != nil {
		return domain.ChatResponse{}, fmt.Errorf("retrieve context: %w", err)
	}

	messages := []domain.Message{
		{Role: "system", Content: buildSystemPrompt(sources)},
		{Role: "user", Content: question},
	}

	slog.Debug("sending chat request", "sources", len(sources))
	answer, err := s.chat.Complete(ctx, messages)
	if err != nil {
		return domain.ChatResponse{}, fmt.Errorf("llm complete: %w", err)
	}

	return domain.ChatResponse{Answer: answer, Sources: sources}, nil
}

// buildSystemPrompt injects retrieved chunks as numbered context blocks.
func buildSystemPrompt(docs []domain.Document) string {
	var b strings.Builder
	b.WriteString("You are a helpful assistant. Answer the user's question using ONLY the context provided below.\n")
	b.WriteString("If the context does not contain enough information, say so clearly — do not make up facts.\n\n")
	b.WriteString("Context:\n")
	for i, d := range docs {
		fmt.Fprintf(&b, "\n[%d] (source: %s)\n%s\n", i+1, d.Filename, d.Chunk)
	}
	return b.String()
}

// -- text extraction --

func extractText(path, ext string) (string, error) {
	if ext == ".txt" {
		data, err := os.ReadFile(path)
		return string(data), err
	}
	return extractPDF(path)
}

func extractPDF(path string) (string, error) {
	text, err := extractPDFNative(path)
	if err == nil && strings.TrimSpace(text) != "" {
		return text, nil
	}
	if err != nil {
		slog.Debug("native PDF extraction failed, falling back to pdftotext", "path", path, "error", err)
	} else {
		slog.Debug("native PDF returned empty text, falling back to pdftotext", "path", path)
	}
	return extractPDFPoppler(path)
}

func extractPDFNative(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			slog.Debug("native extractor skipping page", "path", path, "page", i, "error", err)
			continue
		}
		buf.WriteString(text)
	}
	return buf.String(), nil
}

// extractPDFPoppler uses pdftotext (poppler-utils) which handles PDF 1.5+
// cross-reference streams and compressed objects.
func extractPDFPoppler(path string) (string, error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", fmt.Errorf("pdftotext not found — install poppler-utils (Windows: scoop install poppler | Linux: apt install poppler-utils): %w", err)
	}
	out, err := exec.Command("pdftotext", "-layout", path, "-").Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("pdftotext returned empty output for %s", path)
	}
	slog.Debug("pdftotext extraction succeeded", "path", path, "bytes", len(text))
	return text, nil
}

// -- helpers --

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func chunkText(text string, size, overlap int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	step := size - overlap
	if step <= 0 {
		step = 1
	}
	var chunks []string
	for i := 0; i < len(words); i += step {
		end := i + size
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}
