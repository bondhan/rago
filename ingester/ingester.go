package ingester

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"rago/db"

	"github.com/ledongthuc/pdf"
	"github.com/pgvector/pgvector-go"
)

var (
	lmStudioURL   string
	lmStudioModel string
)

func Init(url, model string) {
	lmStudioURL = url
	lmStudioModel = model
}

// Query embeds text and returns the k most similar stored chunks.
func Query(text string, k int) ([]db.SearchResult, error) {
	emb, err := embed(text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return db.SearchSimilar(emb, k)
}

func IngestFolder(folder string) (int, error) {
	count := 0
	err := filepath.WalkDir(folder, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".txt" && ext != ".pdf" {
			return nil
		}

		relPath, _ := filepath.Rel(folder, path)

		hash, err := fileHash(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", relPath, err)
		}
		if alreadyIngested(hash) {
			return nil
		}

		text, err := extractText(path, ext)
		if err != nil {
			return fmt.Errorf("extract %s: %w", relPath, err)
		}

		chunks := chunkText(text, 500, 100)
		for _, chunk := range chunks {
			emb, err := embed(chunk)
			if err != nil {
				return fmt.Errorf("embed %s: %w", relPath, err)
			}
			if err := storeChunk(relPath, chunk, emb); err != nil {
				return fmt.Errorf("store %s: %w", relPath, err)
			}
		}

		if err := recordHistory(relPath, hash); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

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

func alreadyIngested(hash string) bool {
	var id int
	err := db.DB.QueryRow(`SELECT id FROM file_history WHERE hash = $1`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return err == nil
}

func extractText(path, ext string) (string, error) {
	if ext == ".txt" {
		data, err := os.ReadFile(path)
		return string(data), err
	}
	return extractPDF(path)
}

func extractPDF(path string) (string, error) {
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
			continue
		}
		buf.WriteString(text)
	}
	return buf.String(), nil
}

// chunkText splits text into overlapping windows of `size` words with `overlap` words of context carry-over.
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

func embed(text string) ([]float32, error) {
	payload := map[string]any{"input": text}
	if lmStudioModel != "" {
		payload["model"] = lmStudioModel
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	resp, err := http.Post(lmStudioURL+"/v1/embeddings", "application/json", bytes.NewReader(body))
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

func storeChunk(filename, chunk string, embedding []float32) error {
	_, err := db.DB.Exec(
		`INSERT INTO documents (filename, chunk, embedding) VALUES ($1, $2, $3)`,
		filename, chunk, pgvector.NewVector(embedding),
	)
	return err
}

func recordHistory(filename, hash string) error {
	_, err := db.DB.Exec(
		`INSERT INTO file_history (filename, hash) VALUES ($1, $2) ON CONFLICT (hash) DO NOTHING`,
		filename, hash,
	)
	return err
}
