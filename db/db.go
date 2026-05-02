package db

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"

	_ "github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
)

var DB *sql.DB

func Init() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			getEnv("DB_HOST", "localhost"),
			getEnv("DB_PORT", "5432"),
			getEnv("DB_USER", "postgres"),
			getEnv("DB_PASSWORD", "postgres"),
			getEnv("DB_NAME", "ragodb"),
		)
	}

	var err error
	DB, err = sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	if err = DB.Ping(); err != nil {
		return err
	}
	return migrate()
}

// schemaVersion must be incremented whenever the DDL below changes.
const schemaVersion = 1

func migrate() error {
	if _, err := DB.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var current int
	if err := DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current >= schemaVersion {
		return nil
	}

	dim := getEnvInt("EMBEDDING_DIM", 768)
	if _, err := DB.Exec(fmt.Sprintf(`
		CREATE EXTENSION IF NOT EXISTS vector;

		CREATE TABLE IF NOT EXISTS file_history (
			id          SERIAL PRIMARY KEY,
			filename    TEXT NOT NULL,
			hash        TEXT NOT NULL UNIQUE,
			ingested_at TIMESTAMPTZ DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS documents (
			id        SERIAL PRIMARY KEY,
			filename  TEXT NOT NULL,
			chunk     TEXT NOT NULL,
			embedding vector(%d)
		);

		CREATE INDEX IF NOT EXISTS documents_embedding_idx
			ON documents USING hnsw (embedding vector_cosine_ops);

		CREATE INDEX IF NOT EXISTS documents_filename_idx
			ON documents (filename);

		CREATE INDEX IF NOT EXISTS file_history_filename_idx
			ON file_history (filename);
	`, dim)); err != nil {
		return fmt.Errorf("apply schema v%d: %w", schemaVersion, err)
	}

	if _, err := DB.Exec(`INSERT INTO schema_migrations (version) VALUES ($1)`, schemaVersion); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

// Reset deletes all ingested data while preserving the schema.
func Reset() error {
	_, err := DB.Exec(`TRUNCATE documents, file_history`)
	return err
}

// SearchResult holds a single similarity-search hit.
type SearchResult struct {
	Filename string  `json:"filename"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

// SearchSimilar returns the k most similar chunks to the given embedding.
func SearchSimilar(embedding []float32, k int) ([]SearchResult, error) {
	rows, err := DB.Query(
		`SELECT filename, chunk, 1 - (embedding <=> $1) AS score
		 FROM documents
		 ORDER BY embedding <=> $1
		 LIMIT $2`,
		pgvector.NewVector(embedding), k,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Filename, &r.Chunk, &r.Score); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
