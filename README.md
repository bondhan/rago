# rago

A minimal Retrieval-Augmented Generation (RAG) backend written in Go. It ingests `.txt` and `.pdf` files into a PostgreSQL + pgvector database using embeddings from a local LM Studio instance, then serves semantic search and grounded chat over the stored knowledge base.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         rago-web (React UI)                         │
│   Upload files ──▶ /v1/upload    Chat ──▶ /v1/chat                  │
└────────────┬────────────────────────────┬───────────────────────────┘
             │ multipart/form-data        │ {message, k}
             ▼                            ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        rago API  (Go HTTP :8080)                    │
│                                                                     │
│  /v1/upload ──▶ save to disk ──▶ IngestFolder ──▶ delete from disk  │
│  /v1/ingest ──▶ IngestFolder (folder path from query param)         │
│  /v1/query  ──▶ embed query  ──▶ cosine search ──▶ ranked chunks    │
│  /v1/chat   ──▶ embed query  ──▶ cosine search ──▶ build prompt     │
│                                              ──▶ LLM complete ──▶   │
│  /v1/uploads ──▶ paginated upload history                           │
│  /v1/reset   ──▶ truncate documents + file_history                  │
└──────┬──────────────────────────────────────────┬───────────────────┘
       │ store / search vectors                   │ embed + complete
       ▼                                          ▼
┌─────────────────┐                  ┌────────────────────────────────┐
│   PostgreSQL    │                  │         LM Studio (local)      │
│   + pgvector    │                  │                                │
│                 │                  │  POST /v1/embeddings           │
│  documents      │                  │    input text → []float32      │
│  file_history   │                  │                                │
│  schema_mig..   │                  │  POST /v1/chat/completions     │
└─────────────────┘                  │    messages → answer string    │
                                     └────────────────────────────────┘
```

---

## How rago talks to LM Studio

rago uses LM Studio as both an **embedding engine** and a **chat LLM** via its OpenAI-compatible local API.

### Embedding flow (ingest + query)

Every text chunk and every incoming query is converted to a vector by calling LM Studio's embeddings endpoint.

```
1.  rago extracts text from .txt / .pdf
2.  text is split into 500-word chunks (100-word overlap)
3.  for each chunk:
      POST http://localhost:1234/v1/embeddings
      { "input": "<chunk text>", "model": "<LM_STUDIO_MODEL>" }
      ← { "data": [{ "embedding": [0.023, -0.411, ...] }] }   // 768 floats (default)
4.  the float32 slice is stored in PostgreSQL as a pgvector column
```

At query time the same endpoint is called for the user's question, then PostgreSQL finds the closest stored vectors using cosine distance.

### Chat / RAG flow

```
1.  user sends: POST /v1/chat  { "message": "How do I deploy?", "k": 5 }
2.  rago embeds the question  →  LM Studio /v1/embeddings
3.  rago fetches top-5 chunks  →  PostgreSQL cosine search
4.  rago builds a grounded system prompt:
      "Answer using ONLY the context provided. Say when context is insufficient."
      [1] (source: deploy-guide.pdf)
      <chunk text>
      [2] (source: readme.txt)
      <chunk text>
      ...
5.  rago calls LM Studio chat:
      POST http://localhost:1234/v1/chat/completions
      {
        "model": "<LM_STUDIO_CHAT_MODEL>",
        "stream": false,
        "messages": [
          { "role": "system",    "content": "<grounded prompt>" },
          { "role": "user",      "content": "How do I deploy?" }
        ]
      }
      ← { "choices": [{ "message": { "content": "Based on the provided context..." } }] }
6.  rago returns: { "answer": "...", "sources": [ {filename, chunk, score}, ... ] }
```

The LLM never sees anything outside the retrieved chunks — it is explicitly instructed to refuse to answer when the context is insufficient, which prevents hallucination.

---

## Prerequisites

- [Go 1.21+](https://go.dev/dl/)
- [Docker](https://www.docker.com/) (for PostgreSQL + pgvector)
- [LM Studio](https://lmstudio.ai/) running locally with:
  - An **embedding model** loaded (e.g. `nomic-ai/nomic-embed-text`)
  - A **chat model** loaded (e.g. `meta-llama/Llama-3.2-3B-Instruct`)
  - Local server enabled (default port 1234)
- **poppler-utils** — required for complex PDFs (PDF 1.5+, compressed cross-reference streams)

  ```bash
  # Windows
  scoop install poppler
  # or: choco install poppler

  # Ubuntu / Debian
  apt install poppler-utils

  # macOS
  brew install poppler
  ```

---

## Quick start

### 1. Configure environment

Copy and edit the environment file:

```bash
cp .env.example .env   # or edit .env directly
```

Key settings:

| Variable | Default | Description |
|---|---|---|
| `DB_HOST` | `localhost` | Postgres host |
| `DB_PORT` | `5432` | Postgres port |
| `DB_USER` | `postgres` | Postgres user |
| `DB_PASSWORD` | `postgres` | Postgres password |
| `DB_NAME` | `ragodb` | Database name |
| `DATABASE_URL` | *(empty)* | Full DSN — overrides all `DB_*` vars if set |
| `LM_STUDIO_URL` | `http://localhost:1234` | LM Studio local server base URL |
| `LM_STUDIO_MODEL` | *(empty)* | Model name sent in embedding requests (optional) |
| `LM_STUDIO_CHAT_MODEL` | *(empty)* | Model name sent in chat requests (optional) |
| `EMBEDDING_DIM` | `768` | Vector dimension — **must match your embedding model** |
| `INGEST_WORKERS` | `2 × CPU` | Concurrent file processors during ingestion |
| `UPLOAD_DIR` | `$TEMP/rago-uploads` | Temporary directory for file uploads |
| `LOG_LEVEL` | `WARN` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |

Common `EMBEDDING_DIM` values by model:

| Embedding model | Dimension |
|---|---|
| `nomic-ai/nomic-embed-text` | 768 |
| `sentence-transformers/all-MiniLM-L6-v2` | 384 |
| `text-embedding-ada-002` | 1536 |

### 2. Start LM Studio

1. Open LM Studio and load your embedding model.
2. Load a chat/instruction model.
3. Go to **Local Server** and click **Start Server** (default port 1234).
4. Both models must be loaded simultaneously — LM Studio supports this in the server view.

### 3. Start the database

```bash
docker-compose up -d
```

This starts PostgreSQL 16 with the `pgvector` extension. Data is persisted in a named Docker volume (`pgdata`).

### 4. Run the application

```bash
go run .
```

The server starts on port `8080`. On first run it creates all tables and indexes, recording the schema version so subsequent restarts skip the DDL entirely.

---

## API reference

All routes are versioned under `/v1`. CORS is enabled for all origins.

### `POST /v1/upload`

Accepts one or more `.pdf` / `.txt` files via multipart upload, saves them temporarily, ingests the content into the knowledge base, then deletes the temporary files.

**Request** — `multipart/form-data`, field name `files` (repeatable)

**Response**

```json
{ "files": ["manual.pdf", "notes.txt"], "ingested": 47 }
```

`ingested` is the total number of chunks stored across all uploaded files.

---

### `POST /v1/ingest`

Recursively scans a folder on the **server's filesystem** for `.txt` and `.pdf` files and ingests them. Files already seen (matched by SHA-256 hash) are skipped.

**Query parameter** — `folder` (required): absolute path to the folder

**Response**

```json
{ "ingested": 5 }
```

---

### `POST /v1/query`

Embeds the query text and returns the most semantically similar stored chunks ranked by cosine similarity. No LLM is involved.

**Request body**

```json
{ "query": "how do I reset my password?", "k": 5 }
```

| Field | Required | Default | Description |
|---|---|---|---|
| `query` | yes | — | Natural language search text |
| `k` | no | `5` | Number of results to return |

**Response**

```json
{
  "results": [
    {
      "filename": "docs/user-guide.pdf",
      "chunk": "To reset your password, navigate to...",
      "score": 0.94
    }
  ]
}
```

`score` is cosine similarity in `[0, 1]` — higher is more relevant.

---

### `POST /v1/chat`

Full RAG pipeline: embeds the question, retrieves the top-k chunks, injects them as grounded context into the LLM, and returns the answer with sources.

**Request body**

```json
{ "message": "What are the deployment steps?", "k": 5 }
```

| Field | Required | Default | Description |
|---|---|---|---|
| `message` | yes | — | User question |
| `k` | no | `5` | Number of context chunks to retrieve |

**Response**

```json
{
  "answer": "Based on the provided context, the deployment steps are: 1. Build the Docker image...",
  "sources": [
    { "filename": "docs/deploy-guide.pdf", "chunk": "Step 1: Build...", "score": 0.96 }
  ]
}
```

---

### `GET /v1/uploads`

Returns a paginated list of files that have been ingested, ordered by ingestion time (newest first).

**Query parameters**

| Parameter | Default | Description |
|---|---|---|
| `page` | `1` | Page number (1-based) |
| `limit` | `5` | Items per page (max: 100) |

**Response**

```json
{
  "items": [
    { "id": 3, "filename": "report.pdf", "size_bytes": 204800, "ingested_at": "2026-05-01T10:30:00Z" }
  ],
  "total": 12,
  "page": 1,
  "limit": 5
}
```

---

### `DELETE /v1/reset`

Truncates all ingested data (`documents` and `file_history`) while keeping the schema intact. Files can be re-ingested afterwards.

**Response**

```json
{ "status": "ok" }
```

---

## curl examples

```bash
# Upload a file
curl -s -X POST http://localhost:8080/v1/upload \
  -F "files=@manual.pdf" -F "files=@notes.txt" | jq

# Ingest a folder on the server's disk
curl -s -X POST "http://localhost:8080/v1/ingest?folder=/home/user/documents" | jq

# Semantic search (no LLM)
curl -s -X POST http://localhost:8080/v1/query \
  -H "Content-Type: application/json" \
  -d '{"query": "how do I reset my password?"}' | jq

# Chat (full RAG pipeline)
curl -s -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What are the deployment steps?", "k": 5}' | jq

# List upload history (page 2, 10 per page)
curl -s "http://localhost:8080/v1/uploads?page=2&limit=10" | jq

# Reset all ingested data
curl -s -X DELETE http://localhost:8080/v1/reset | jq
```

---

## Ingestion pipeline

```
IngestFolder(folder)
│
├── walk folder recursively
├── filter .txt and .pdf files
│
└── worker pool (INGEST_WORKERS goroutines)
    │
    └── per file:
        ├── SHA-256 hash
        ├── IsIngested? → skip if already seen
        ├── extract text
        │     .txt → read file
        │     .pdf → native extraction (ledongthuc/pdf)
        │               ↳ fallback: pdftotext (poppler-utils)
        ├── chunk: 500 words, 100-word overlap
        ├── for each chunk:
        │     embed via LM Studio /v1/embeddings
        │     StoreChunk (filename + chunk + vector)
        └── RecordFile (filename + hash + size)
```

Deduplication is hash-based: uploading the same file twice (or re-ingesting after a server restart) is a no-op for that file.

---

## Database

The application manages its own schema. On startup it checks a `schema_migrations` table and applies DDL only for versions that have not yet been recorded — safe to restart at any time.

### Tables

| Table | Description |
|---|---|
| `schema_migrations` | Tracks applied schema versions |
| `file_history` | SHA-256 hashes + metadata of every ingested file |
| `documents` | Text chunks with their pgvector embeddings |

### Inspect stored data

```bash
# Count stored chunks
docker exec -it rago-db-1 psql -U postgres -d ragodb \
  -c "SELECT count(*) FROM documents;"

# Preview chunks
docker exec -it rago-db-1 psql -U postgres -d ragodb \
  -c "SELECT filename, left(chunk, 80) AS preview FROM documents LIMIT 10;"

# View ingestion history
docker exec -it rago-db-1 psql -U postgres -d ragodb \
  -c "SELECT filename, size_bytes, ingested_at FROM file_history ORDER BY ingested_at DESC;"
```

### Wipe everything

```bash
# Via API (schema stays intact)
curl -s -X DELETE http://localhost:8080/v1/reset

# Via Docker (removes the volume too)
docker-compose down -v && docker-compose up -d
```

---

## Project structure

```
rago/
├── cmd/server/
│   └── main.go                # Wiring, env loading, graceful shutdown
├── internal/
│   ├── domain/
│   │   └── rag.go             # Types + Repository / Embedder / ChatCompleter interfaces
│   ├── postgres/
│   │   └── postgres.go        # DB connect, versioned migrations, Repository impl
│   ├── lmstudio/
│   │   ├── embedder.go        # Embedder impl — calls /v1/embeddings
│   │   └── chat.go            # ChatCompleter impl — calls /v1/chat/completions
│   ├── service/
│   │   └── rag.go             # Use cases: IngestFolder, Query, Chat, ListUploads, Reset
│   └── handler/
│       └── handler.go         # HTTP handlers (depends only on domain interfaces)
├── docker-compose.yml
├── .env
├── go.mod
└── go.sum
```

### Dependency flow

```
handler → service → domain ← postgres
                  ↘ domain ← lmstudio (embedder)
                  ↘ domain ← lmstudio (chat)
```

Each layer depends only on `domain` interfaces — never on concrete implementations. The repository, embedder, and chat client can each be swapped (e.g. for testing) without touching any other package.

---

## Running tests

```bash
go test ./...
```

The handler and service packages use in-process mocks and require no external dependencies. The postgres package tests require a live database and are skipped when `DATABASE_URL` is not set.
