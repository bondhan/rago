# rago

A minimal RAG (Retrieval-Augmented Generation) backend written in Go. It ingests `.txt` and `.pdf` files into a PostgreSQL + pgvector database using embeddings from a local LM Studio instance, then serves semantic search over the stored chunks.

---

## Architecture

```
┌─────────────┐     POST /ingest     ┌──────────────┐     embed      ┌─────────────┐
│  Your files │ ──────────────────▶  │   rago API   │ ─────────────▶ │  LM Studio  │
│ .txt / .pdf │                      │   (Go HTTP)  │ ◀─────────────  │ /v1/embeddings│
└─────────────┘                      └──────┬───────┘   []float32    └─────────────┘
                                            │ store chunks + vectors
                                            ▼
                                     ┌──────────────┐
                                     │  PostgreSQL  │
                                     │  + pgvector  │
                                     └──────────────┘
                                            ▲
                                            │ similarity search
┌─────────────┐     POST /query      ┌──────┴───────┐
│   Client    │ ──────────────────▶  │   rago API   │
│             │ ◀──────────────────  │              │
└─────────────┘   ranked chunks      └──────────────┘
```

---

## Prerequisites

- [Go 1.21+](https://go.dev/dl/)
- [Docker](https://www.docker.com/) (for PostgreSQL + pgvector)
- [LM Studio](https://lmstudio.ai/) running locally with an embedding model loaded

---

## Quick start

### 1. Configure environment

Copy and edit the environment file:

```bash
cp .env .env.local   # optional — .env is loaded automatically
```

Key settings in [`.env`](.env):

| Variable | Default | Description |
|---|---|---|
| `DB_HOST` | `localhost` | Postgres host |
| `DB_PORT` | `5432` | Postgres port |
| `DB_USER` | `postgres` | Postgres user |
| `DB_PASSWORD` | `postgres` | Postgres password |
| `DB_NAME` | `ragodb` | Database name |
| `LM_STUDIO_URL` | `http://localhost:1234` | LM Studio base URL |
| `LM_STUDIO_MODEL` | *(empty)* | Model identifier sent in embed requests (optional) |
| `EMBEDDING_DIM` | `768` | Must match your embedding model's output size |

Common `EMBEDDING_DIM` values:

| Model | Dimension |
|---|---|
| `nomic-ai/nomic-embed-text` | 768 |
| `sentence-transformers/all-MiniLM-L6-v2` | 384 |
| `text-embedding-ada-002` | 1536 |

### 2. Start the database

```bash
docker-compose up -d
```

This starts a PostgreSQL 16 instance with the `pgvector` extension pre-installed. Data is persisted in a named Docker volume (`pgdata`).

### 3. Run the application

```bash
go run .
```

The server starts on port `8080`. On first run it creates the schema and records the schema version — subsequent restarts skip the DDL entirely.

```
2024/01/15 10:00:00 Listening on :8080
```

---

## API

### `POST /ingest`

Recursively scans a folder for `.txt` and `.pdf` files, chunks them, generates embeddings, and stores them in the database. Files are identified by SHA-256 hash — re-ingesting the same file is a no-op.

**Query parameters**

| Parameter | Required | Description |
|---|---|---|
| `folder` | yes | Absolute path to the folder to ingest |

**Response**

```json
{ "ingested": 3 }
```

---

### `POST /query`

Embeds the query text and returns the most semantically similar stored chunks ranked by cosine similarity.

**Request body**

```json
{
  "query": "your question here",
  "k": 5
}
```

| Field | Required | Description |
|---|---|---|
| `query` | yes | Natural language question |
| `k` | no | Number of results to return (default: `5`) |

**Response**

```json
{
  "results": [
    {
      "filename": "docs/manual.pdf",
      "chunk": "The system supports three authentication modes...",
      "score": 0.94
    }
  ]
}
```

`score` is cosine similarity in the range `[0, 1]` — higher means more similar.

---

### `DELETE /v1/reset`

Truncates all ingested data (`documents` and `file_history`) while keeping the schema intact. Files can be re-ingested afterwards.

**Response**

```json
{ "status": "ok" }
```

---

## curl examples

### Ingest a folder

```bash
curl -s -X POST "http://localhost:8080/ingest?folder=/home/user/documents" | jq
```

```json
{ "ingested": 5 }
```

### Ingest and watch progress

```bash
curl -X POST "http://localhost:8080/ingest?folder=/home/user/documents"
```

Files already seen in a previous run are skipped automatically.

### Query — top 5 results (default)

```bash
curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"query": "how do I reset my password?"}' | jq
```

```json
{
  "results": [
    {
      "filename": "docs/user-guide.pdf",
      "chunk": "To reset your password, navigate to the login page and click Forgot Password...",
      "score": 0.91
    },
    {
      "filename": "docs/faq.txt",
      "chunk": "Password resets are sent to the email address registered on your account...",
      "score": 0.87
    }
  ]
}
```

### Query — return top 3 results

```bash
curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"query": "what are the system requirements?", "k": 3}' | jq
```

### Reset all ingested data

```bash
curl -s -X DELETE http://localhost:8080/v1/reset | jq
```

```json
{ "status": "ok" }
```

### Query — pipe chunk text only

```bash
curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"query": "deployment steps"}' | jq -r '.results[].chunk'
```

---

## Database

The application manages its own schema. On startup it checks a `schema_migrations` table and applies DDL only when the schema version has not yet been recorded — safe to restart as many times as needed.

### Tables

| Table | Description |
|---|---|
| `schema_migrations` | Tracks applied schema versions |
| `file_history` | SHA-256 hashes of ingested files to prevent re-processing |
| `documents` | Stored text chunks with their vector embeddings |

### Inspect stored chunks

```bash
docker exec -it rago-db-1 psql -U postgres -d ragodb \
  -c "SELECT filename, left(chunk, 80) AS preview FROM documents LIMIT 10;"
```

### Reset all data

```bash
docker-compose down -v   # removes the pgdata volume
docker-compose up -d
```

---

## Project structure

```
rago/
├── main.go            # HTTP server, /ingest and /query handlers
├── db/
│   └── db.go          # Connection, versioned migrations, similarity search
├── ingester/
│   └── ingester.go    # File walking, chunking, embedding, storage
├── docker-compose.yml # PostgreSQL + pgvector service
├── .env               # Environment variables (loaded automatically)
├── go.mod
└── go.sum
```
