package domain

import "context"

// Document is a stored text chunk returned from a similarity search.
type Document struct {
	Filename string  `json:"filename"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

// Repository is the storage port. Implementations live in internal/postgres.
type Repository interface {
	StoreChunk(ctx context.Context, filename, chunk string, embedding []float32) error
	SearchSimilar(ctx context.Context, embedding []float32, k int) ([]Document, error)
	IsIngested(ctx context.Context, hash string) (bool, error)
	RecordFile(ctx context.Context, filename, hash string) error
	Reset(ctx context.Context) error
}

// Embedder is the embedding port. Implementations live in internal/lmstudio.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Message is a single turn in a chat conversation.
type Message struct {
	Role    string `json:"role"`    // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// ChatResponse is the result of a RAG-augmented chat turn.
type ChatResponse struct {
	Answer  string     `json:"answer"`
	Sources []Document `json:"sources"`
}

// ChatCompleter is the LLM chat port. Implementations live in internal/lmstudio.
type ChatCompleter interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}
