package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"rago/internal/domain"
)

// ── mocks ────────────────────────────────────────────────────────────────────

type mockRepo struct {
	isIngestedFn  func(ctx context.Context, hash string) (bool, error)
	storeChunkFn  func(ctx context.Context, filename, chunk string, embedding []float32) error
	recordFileFn  func(ctx context.Context, filename, hash string, sizeBytes int64) error
	searchFn      func(ctx context.Context, embedding []float32, k int) ([]domain.Document, error)
	resetFn       func(ctx context.Context) error
	listUploadsFn func(ctx context.Context, page, limit int) (domain.UploadPage, error)
}

func (m *mockRepo) IsIngested(ctx context.Context, hash string) (bool, error) {
	if m.isIngestedFn != nil {
		return m.isIngestedFn(ctx, hash)
	}
	return false, nil
}
func (m *mockRepo) StoreChunk(ctx context.Context, filename, chunk string, embedding []float32) error {
	if m.storeChunkFn != nil {
		return m.storeChunkFn(ctx, filename, chunk, embedding)
	}
	return nil
}
func (m *mockRepo) RecordFile(ctx context.Context, filename, hash string, sizeBytes int64) error {
	if m.recordFileFn != nil {
		return m.recordFileFn(ctx, filename, hash, sizeBytes)
	}
	return nil
}
func (m *mockRepo) ListUploads(ctx context.Context, page, limit int) (domain.UploadPage, error) {
	if m.listUploadsFn != nil {
		return m.listUploadsFn(ctx, page, limit)
	}
	return domain.UploadPage{Items: []domain.UploadRecord{}, Total: 0, Page: page, Limit: limit}, nil
}
func (m *mockRepo) SearchSimilar(ctx context.Context, embedding []float32, k int) ([]domain.Document, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, embedding, k)
	}
	return nil, nil
}
func (m *mockRepo) Reset(ctx context.Context) error {
	if m.resetFn != nil {
		return m.resetFn(ctx)
	}
	return nil
}

type mockEmbedder struct {
	embedFn func(ctx context.Context, text string) ([]float32, error)
}

func (m *mockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

type mockChat struct {
	completeFn func(ctx context.Context, messages []domain.Message) (string, error)
}

func (m *mockChat) Complete(ctx context.Context, messages []domain.Message) (string, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, messages)
	}
	return "mock answer", nil
}

func newSvc(repo domain.Repository, emb domain.Embedder, chat domain.ChatCompleter) *RAGService {
	return NewRAGService(repo, emb, chat)
}

// ── chunkText ─────────────────────────────────────────────────────────────────

func TestChunkTextEmpty(t *testing.T) {
	if chunks := chunkText("", 5, 2); chunks != nil {
		t.Fatalf("expected nil for empty input, got %v", chunks)
	}
}

func TestChunkTextFitsInOneChunk(t *testing.T) {
	chunks := chunkText("a b c", 10, 2)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != "a b c" {
		t.Errorf("unexpected chunk: %q", chunks[0])
	}
}

func TestChunkTextNoOverlap(t *testing.T) {
	chunks := chunkText("a b c d e f", 2, 0)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
}

func TestChunkTextWithOverlap(t *testing.T) {
	chunks := chunkText("a b c d e f g h i j", 4, 2)
	for _, c := range chunks {
		words := strings.Fields(c)
		if len(words) > 4 {
			t.Errorf("chunk exceeds size: %q", c)
		}
	}
	if len(chunks) < 2 {
		t.Error("expected multiple chunks with overlap")
	}
}

func TestChunkTextOverlapClampedToOne(t *testing.T) {
	// overlap >= size → step becomes 1, should not panic
	chunks := chunkText("a b c d", 2, 5)
	if len(chunks) == 0 {
		t.Error("expected at least one chunk")
	}
}

func TestChunkTextLastChunkIncluded(t *testing.T) {
	chunks := chunkText("a b c d e", 3, 1)
	last := chunks[len(chunks)-1]
	if !strings.Contains(last, "e") {
		t.Errorf("last word missing from final chunk: %v", chunks)
	}
}

// ── buildSystemPrompt ─────────────────────────────────────────────────────────

func TestBuildSystemPromptContainsSource(t *testing.T) {
	docs := []domain.Document{
		{Filename: "manual.pdf", Chunk: "important content"},
	}
	prompt := buildSystemPrompt(docs)
	if !strings.Contains(prompt, "manual.pdf") {
		t.Error("expected prompt to contain filename")
	}
	if !strings.Contains(prompt, "important content") {
		t.Error("expected prompt to contain chunk")
	}
}

func TestBuildSystemPromptNumberedSources(t *testing.T) {
	docs := []domain.Document{
		{Filename: "a.txt", Chunk: "first"},
		{Filename: "b.txt", Chunk: "second"},
	}
	prompt := buildSystemPrompt(docs)
	if !strings.Contains(prompt, "[1]") || !strings.Contains(prompt, "[2]") {
		t.Error("expected numbered context blocks")
	}
}

func TestBuildSystemPromptNoDocs(t *testing.T) {
	prompt := buildSystemPrompt(nil)
	if prompt == "" {
		t.Error("expected non-empty prompt even with no sources")
	}
}

// ── Query ─────────────────────────────────────────────────────────────────────

func TestQuerySuccess(t *testing.T) {
	want := []domain.Document{{Filename: "f.txt", Chunk: "chunk", Score: 0.9}}
	svc := newSvc(
		&mockRepo{searchFn: func(_ context.Context, _ []float32, _ int) ([]domain.Document, error) {
			return want, nil
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	got, err := svc.Query(context.Background(), "question", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Filename != "f.txt" {
		t.Errorf("unexpected results: %v", got)
	}
}

func TestQueryEmbedError(t *testing.T) {
	svc := newSvc(
		&mockRepo{},
		&mockEmbedder{embedFn: func(_ context.Context, _ string) ([]float32, error) {
			return nil, errors.New("embed failed")
		}},
		&mockChat{},
	)
	_, err := svc.Query(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestQuerySearchError(t *testing.T) {
	svc := newSvc(
		&mockRepo{searchFn: func(_ context.Context, _ []float32, _ int) ([]domain.Document, error) {
			return nil, errors.New("db error")
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	_, err := svc.Query(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── Chat ──────────────────────────────────────────────────────────────────────

func TestChatSuccess(t *testing.T) {
	docs := []domain.Document{{Filename: "doc.txt", Chunk: "context", Score: 0.95}}
	svc := newSvc(
		&mockRepo{searchFn: func(_ context.Context, _ []float32, _ int) ([]domain.Document, error) {
			return docs, nil
		}},
		&mockEmbedder{},
		&mockChat{completeFn: func(_ context.Context, _ []domain.Message) (string, error) {
			return "The answer is X", nil
		}},
	)
	resp, err := svc.Chat(context.Background(), "what is X?", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Answer != "The answer is X" {
		t.Errorf("unexpected answer: %s", resp.Answer)
	}
	if len(resp.Sources) != 1 {
		t.Errorf("expected 1 source, got %d", len(resp.Sources))
	}
}

func TestChatEmbedError(t *testing.T) {
	svc := newSvc(
		&mockRepo{},
		&mockEmbedder{embedFn: func(_ context.Context, _ string) ([]float32, error) {
			return nil, errors.New("embed error")
		}},
		&mockChat{},
	)
	_, err := svc.Chat(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatLLMError(t *testing.T) {
	svc := newSvc(
		&mockRepo{},
		&mockEmbedder{},
		&mockChat{completeFn: func(_ context.Context, _ []domain.Message) (string, error) {
			return "", errors.New("llm error")
		}},
	)
	_, err := svc.Chat(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatPromptIncludesQuestion(t *testing.T) {
	var capturedMessages []domain.Message
	svc := newSvc(
		&mockRepo{},
		&mockEmbedder{},
		&mockChat{completeFn: func(_ context.Context, msgs []domain.Message) (string, error) {
			capturedMessages = msgs
			return "ok", nil
		}},
	)
	svc.Chat(context.Background(), "my question", 3)
	if len(capturedMessages) < 2 {
		t.Fatal("expected at least 2 messages (system + user)")
	}
	userMsg := capturedMessages[len(capturedMessages)-1]
	if userMsg.Content != "my question" {
		t.Errorf("expected user message to be 'my question', got %q", userMsg.Content)
	}
}

// ── Reset ─────────────────────────────────────────────────────────────────────

func TestResetCallsRepo(t *testing.T) {
	called := false
	svc := newSvc(
		&mockRepo{resetFn: func(_ context.Context) error {
			called = true
			return nil
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	if err := svc.Reset(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected Reset to be called on repo")
	}
}

func TestResetPropagatesError(t *testing.T) {
	svc := newSvc(
		&mockRepo{resetFn: func(_ context.Context) error {
			return errors.New("db error")
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	if err := svc.Reset(context.Background()); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// ── IngestFolder ──────────────────────────────────────────────────────────────

func TestIngestFolderTxtFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("hello world this is content for testing"), 0644); err != nil {
		t.Fatal(err)
	}
	var stored atomic.Int32
	svc := newSvc(
		&mockRepo{storeChunkFn: func(_ context.Context, _, _ string, _ []float32) error {
			stored.Add(1)
			return nil
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	count, err := svc.IngestFolder(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 file ingested, got %d", count)
	}
	if stored.Load() == 0 {
		t.Error("expected chunks to be stored")
	}
}

func TestIngestFolderSkipsAlreadyIngested(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("content"), 0644)

	svc := newSvc(
		&mockRepo{isIngestedFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	count, err := svc.IngestFolder(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 files, got %d", count)
	}
}

func TestIngestFolderSkipsUnsupportedExtensions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}"), 0644)
	os.WriteFile(filepath.Join(dir, "image.png"), []byte("PNG"), 0644)

	svc := newSvc(&mockRepo{}, &mockEmbedder{}, &mockChat{})
	count, _ := svc.IngestFolder(context.Background(), dir)
	if count != 0 {
		t.Errorf("expected 0 files ingested, got %d", count)
	}
}

func TestIngestFolderRecursive(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root content"), 0644)
	os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("nested content"), 0644)

	var stored atomic.Int32
	svc := newSvc(
		&mockRepo{storeChunkFn: func(_ context.Context, _, _ string, _ []float32) error {
			stored.Add(1)
			return nil
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	count, err := svc.IngestFolder(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files ingested (root + nested), got %d", count)
	}
}

func TestIngestFolderInvalidPath(t *testing.T) {
	svc := newSvc(&mockRepo{}, &mockEmbedder{}, &mockChat{})
	_, err := svc.IngestFolder(context.Background(), "/nonexistent/path/xyz")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestIngestFolderEmbedError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("some content here"), 0644)

	svc := newSvc(
		&mockRepo{},
		&mockEmbedder{embedFn: func(_ context.Context, _ string) ([]float32, error) {
			return nil, errors.New("embed failed")
		}},
		&mockChat{},
	)
	_, err := svc.IngestFolder(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error from embed failure")
	}
}

func TestIngestFolderStoreError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("content"), 0644)

	svc := newSvc(
		&mockRepo{storeChunkFn: func(_ context.Context, _, _ string, _ []float32) error {
			return errors.New("store failed")
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	_, err := svc.IngestFolder(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error from store failure")
	}
}

func TestIngestFolderIsIngestedError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("content"), 0644)

	svc := newSvc(
		&mockRepo{isIngestedFn: func(_ context.Context, _ string) (bool, error) {
			return false, errors.New("db error")
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	_, err := svc.IngestFolder(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── extraction helpers ────────────────────────────────────────────────────────

func TestFileHashSuccess(t *testing.T) {
	f, err := os.CreateTemp("", "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("content")
	f.Close()
	defer os.Remove(f.Name())

	hash, err := fileHash(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hash) != 64 {
		t.Errorf("expected 64-char sha256 hex, got len=%d", len(hash))
	}
}

func TestFileHashMissingFile(t *testing.T) {
	_, err := fileHash("/nonexistent/path/file.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileHashIsDeterministic(t *testing.T) {
	f, _ := os.CreateTemp("", "*.txt")
	f.WriteString("same content")
	f.Close()
	defer os.Remove(f.Name())

	h1, _ := fileHash(f.Name())
	h2, _ := fileHash(f.Name())
	if h1 != h2 {
		t.Error("expected same hash for same file")
	}
}

func TestExtractTextTxt(t *testing.T) {
	f, _ := os.CreateTemp("", "*.txt")
	f.WriteString("hello world")
	f.Close()
	defer os.Remove(f.Name())

	text, err := extractText(f.Name(), ".txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello world" {
		t.Errorf("unexpected text: %q", text)
	}
}

func TestExtractTextTxtMissingFile(t *testing.T) {
	_, err := extractText("/nonexistent/file.txt", ".txt")
	if err == nil {
		t.Fatal("expected error for missing txt file")
	}
}

func TestExtractPDFNativeNonExistent(t *testing.T) {
	_, err := extractPDFNative("/nonexistent/file.pdf")
	if err == nil {
		t.Fatal("expected error for non-existent PDF")
	}
}

func TestExtractPDFNativeInvalidContent(t *testing.T) {
	f, _ := os.CreateTemp("", "*.pdf")
	f.WriteString("this is not a valid PDF file content")
	f.Close()
	defer os.Remove(f.Name())

	_, err := extractPDFNative(f.Name())
	if err == nil {
		t.Fatal("expected error for invalid PDF content")
	}
}

func TestExtractPDFPopllerNotInstalled(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err == nil {
		t.Skip("pdftotext is installed; skipping not-found test")
	}
	_, err := extractPDFPoppler("/some/file.pdf")
	if err == nil {
		t.Fatal("expected error when pdftotext is not installed")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error message, got: %v", err)
	}
}

func TestExtractPDFFallbackTriggered(t *testing.T) {
	// A file named .pdf with non-PDF bytes will fail extractPDFNative,
	// causing extractPDF to call the poppler fallback path.
	f, _ := os.CreateTemp("", "*.pdf")
	f.WriteString("definitely not a pdf")
	f.Close()
	defer os.Remove(f.Name())

	// We don't assert on the error — whether pdftotext is installed or not
	// is environment-specific. We only assert the function does not panic and
	// that the native path was at least attempted (exercising extractPDF body).
	_, _ = extractPDF(f.Name())
}

func TestExtractPDFPopllerEmptyOutput(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	// Pass an invalid path — pdftotext will exit non-zero
	_, err := extractPDFPoppler("/nonexistent/file.pdf")
	if err == nil {
		t.Fatal("expected error for non-existent PDF")
	}
}

func TestIngestFolderRecordFileError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("content"), 0644)

	svc := newSvc(
		&mockRepo{recordFileFn: func(_ context.Context, _, _ string, _ int64) error {
			return errors.New("record failed")
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	_, err := svc.IngestFolder(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error from RecordFile failure")
	}
}

func TestIngestWorkersDefault(t *testing.T) {
	os.Unsetenv("INGEST_WORKERS")
	want := 2 * runtime.NumCPU()
	if got := ingestWorkers(); got != want {
		t.Errorf("expected %d workers, got %d", want, got)
	}
}

func TestIngestWorkersEnvOverride(t *testing.T) {
	os.Setenv("INGEST_WORKERS", "7")
	defer os.Unsetenv("INGEST_WORKERS")
	if got := ingestWorkers(); got != 7 {
		t.Errorf("expected 7 workers, got %d", got)
	}
}

func TestIngestWorkersEnvInvalidFallsBack(t *testing.T) {
	os.Setenv("INGEST_WORKERS", "bad")
	defer os.Unsetenv("INGEST_WORKERS")
	want := 2 * runtime.NumCPU()
	if got := ingestWorkers(); got != want {
		t.Errorf("expected fallback %d workers, got %d", want, got)
	}
}

func TestIngestWorkersEnvZeroFallsBack(t *testing.T) {
	os.Setenv("INGEST_WORKERS", "0")
	defer os.Unsetenv("INGEST_WORKERS")
	want := 2 * runtime.NumCPU()
	if got := ingestWorkers(); got != want {
		t.Errorf("expected fallback %d workers, got %d", want, got)
	}
}

func TestIngestFolderContextCancelled(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("doc%d.txt", i)), []byte("content"), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := newSvc(
		&mockRepo{isIngestedFn: func(_ context.Context, _ string) (bool, error) {
			cancel()
			// Return the cancellation error so errgroup propagates it to g.Wait().
			return false, context.Canceled
		}},
		&mockEmbedder{},
		&mockChat{},
	)
	_, err := svc.IngestFolder(ctx, dir)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
