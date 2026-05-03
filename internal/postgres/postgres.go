package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"rago/internal/domain"

	_ "github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
)

// Connect opens and verifies a PostgreSQL connection using environment variables.
func Connect() (*sql.DB, error) {
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
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		return nil, err
	}
	slog.Debug("database connected")
	return db, nil
}

// schemaVersion must be incremented whenever the DDL below changes.
const schemaVersion = 2

// Migrate creates or updates schema objects using an incremental version table.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current >= schemaVersion {
		slog.Debug("schema up to date", "version", current)
		return nil
	}

	slog.Info("applying schema migrations", "from", current, "to", schemaVersion)

	if current < 1 {
		dim := getEnvInt("EMBEDDING_DIM", 768)
		if _, err := db.Exec(fmt.Sprintf(`
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
			return fmt.Errorf("apply schema v1: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (1)`); err != nil {
			return fmt.Errorf("record schema v1: %w", err)
		}
		slog.Info("schema v1 applied")
	}

	if current < 2 {
		if _, err := db.Exec(`
			ALTER TABLE file_history
				ADD COLUMN IF NOT EXISTS size_bytes BIGINT NOT NULL DEFAULT 0;
		`); err != nil {
			return fmt.Errorf("apply schema v2: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2)`); err != nil {
			return fmt.Errorf("record schema v2: %w", err)
		}
		slog.Info("schema v2 applied")
	}

	return nil
}

// Repository implements domain.Repository against PostgreSQL.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) StoreChunk(ctx context.Context, filename, chunk string, embedding []float32) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO documents (filename, chunk, embedding) VALUES ($1, $2, $3)`,
		filename, chunk, pgvector.NewVector(embedding),
	)
	return err
}

func (r *Repository) SearchSimilar(ctx context.Context, embedding []float32, k int) ([]domain.Document, error) {
	rows, err := r.db.QueryContext(ctx,
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

	var results []domain.Document
	for rows.Next() {
		var d domain.Document
		if err := rows.Scan(&d.Filename, &d.Chunk, &d.Score); err != nil {
			return nil, err
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

func (r *Repository) IsIngested(ctx context.Context, hash string) (bool, error) {
	var id int
	err := r.db.QueryRowContext(ctx, `SELECT id FROM file_history WHERE hash = $1`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) RecordFile(ctx context.Context, filename, hash string, sizeBytes int64) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO file_history (filename, hash, size_bytes) VALUES ($1, $2, $3) ON CONFLICT (hash) DO NOTHING`,
		filename, hash, sizeBytes,
	)
	return err
}

func (r *Repository) ListUploads(ctx context.Context, page, limit int) (domain.UploadPage, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_history`).Scan(&total); err != nil {
		return domain.UploadPage{}, err
	}

	offset := (page - 1) * limit
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, filename, size_bytes, ingested_at
		 FROM file_history
		 ORDER BY ingested_at DESC
		 LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return domain.UploadPage{}, err
	}
	defer rows.Close()

	var items []domain.UploadRecord
	for rows.Next() {
		var rec domain.UploadRecord
		if err := rows.Scan(&rec.ID, &rec.Filename, &rec.SizeBytes, &rec.IngestedAt); err != nil {
			return domain.UploadPage{}, err
		}
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return domain.UploadPage{}, err
	}
	if items == nil {
		items = []domain.UploadRecord{}
	}

	return domain.UploadPage{Items: items, Total: total, Page: page, Limit: limit}, nil
}

func (r *Repository) Reset(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `TRUNCATE documents, file_history`)
	return err
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
