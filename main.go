package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"rago/db"
	"rago/ingester"

	"github.com/joho/godotenv"
)

func init() {
	// Load .env when present; silently ignore if missing (e.g. production).
	_ = godotenv.Load()
}

func main() {
	if err := db.Init(); err != nil {
		log.Fatalf("db init: %v", err)
	}

	lmStudioURL := os.Getenv("LM_STUDIO_URL")
	if lmStudioURL == "" {
		lmStudioURL = "http://localhost:1234"
	}
	ingester.Init(lmStudioURL, os.Getenv("LM_STUDIO_MODEL"))

	http.HandleFunc("/ingest", handleIngest)
	http.HandleFunc("/query", handleQuery)
	http.HandleFunc("/v1/reset", handleReset)
	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "folder query param required", http.StatusBadRequest)
		return
	}

	count, err := ingester.IngestFolder(folder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"ingested": count}); err != nil {
		log.Printf("encode ingest response: %v", err)
	}
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := db.Reset(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		log.Printf("encode reset response: %v", err)
	}
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
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

	results, err := ingester.Query(req.Query, req.K)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"results": results}); err != nil {
		log.Printf("encode query response: %v", err)
	}
}
