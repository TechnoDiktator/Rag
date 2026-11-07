package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	Model          = "text-embedding-3-small"
	Dimensions     = 1536
	maxQueryRunes  = 2000
	maxResults     = 20
	candidateLimit = 50
)

type Result struct {
	DocumentID  string  `json:"document_id"`
	Filename    string  `json:"filename"`
	Version     int     `json:"version"`
	ChunkIndex  int     `json:"chunk_index"`
	Text        string  `json:"text"`
	Description *string `json:"description"`
	Pages       []int   `json:"pages"`
	Score       float64 `json:"score"`
	VectorRank  *int    `json:"vector_rank"`
	KeywordRank *int    `json:"keyword_rank"`
}

type Embedder interface {
	Embed(context.Context, string) ([]float64, error)
}

type Reader interface {
	Search(context.Context, string, []float64, *uuid.UUID, int) ([]Result, error)
}

type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Repository struct{ db Querier }

func NewRepository(db Querier) *Repository { return &Repository{db: db} }

// BM25 uses k1=1.2 and b=0.75 over lowercased alphanumeric tokens. The
// corpus is the selected set of current-version chunks with compatible vectors.
// RRF combines independent top-50 rank lists using the notebook's 0.7/0.3 weights.
const searchSQL = `
WITH corpus AS MATERIALIZED (
  SELECT e.document_id, e.version, e.chunk_index, e.searchable_text, e.description,
         e.embedding, c.content, d.filename
  FROM document_chunk_embeddings e
  JOIN documents d ON d.id = e.document_id AND d.current_version = e.version
  JOIN document_chunks c ON c.document_id = e.document_id AND c.version = e.version AND c.chunk_index = e.chunk_index
  WHERE e.embedding_model = $4 AND d.status = 'COMPLETED'
    AND ($3::uuid IS NULL OR e.document_id = $3::uuid)
),
query_terms AS (
  SELECT DISTINCT term FROM regexp_split_to_table(lower($1), '[^[:alnum:]]+') term WHERE term <> ''
),
tokenized AS MATERIALIZED (
  SELECT c.document_id, c.version, c.chunk_index,
         ARRAY(SELECT term FROM regexp_split_to_table(lower(c.searchable_text), '[^[:alnum:]]+') term WHERE term <> '') AS tokens
  FROM corpus c
),
corpus_stats AS (
  SELECT count(*)::float8 AS n, avg(cardinality(tokens))::float8 AS avg_len FROM tokenized
),
frequencies AS MATERIALIZED (
  SELECT t.document_id, t.version, t.chunk_index, cardinality(t.tokens) AS doc_len, q.term,
         (SELECT count(*) FROM unnest(t.tokens) token WHERE token = q.term)::float8 AS tf
  FROM tokenized t CROSS JOIN query_terms q
),
term_stats AS (
  SELECT term, count(*) FILTER (WHERE tf > 0)::float8 AS df FROM frequencies GROUP BY term
),
keyword_scores AS (
  SELECT f.document_id, f.version, f.chunk_index,
         sum(ln(1 + (s.n - ts.df + 0.5) / (ts.df + 0.5)) *
           ((f.tf * 2.2) / (f.tf + 1.2 * (0.25 + 0.75 * f.doc_len / NULLIF(s.avg_len, 0))))) AS score
  FROM frequencies f JOIN term_stats ts ON ts.term = f.term CROSS JOIN corpus_stats s
  WHERE f.tf > 0
  GROUP BY f.document_id, f.version, f.chunk_index
),
keyword_top AS (
  SELECT *, row_number() OVER (ORDER BY score DESC, document_id, version, chunk_index) AS rank
  FROM keyword_scores ORDER BY score DESC, document_id, version, chunk_index LIMIT $5
),
vector_top AS (
  SELECT document_id, version, chunk_index,
         row_number() OVER (ORDER BY embedding <=> $2::vector, document_id, version, chunk_index) AS rank
  FROM corpus ORDER BY embedding <=> $2::vector, document_id, version, chunk_index LIMIT $5
),
combined AS (
  SELECT COALESCE(v.document_id, k.document_id) AS document_id,
         COALESCE(v.version, k.version) AS version, COALESCE(v.chunk_index, k.chunk_index) AS chunk_index,
         v.rank AS vector_rank, k.rank AS keyword_rank,
         COALESCE(0.7 / (60.0 + v.rank), 0) + COALESCE(0.3 / (60.0 + k.rank), 0) AS score
  FROM vector_top v FULL JOIN keyword_top k USING (document_id, version, chunk_index)
)
SELECT c.document_id::text, c.filename, c.version, c.chunk_index, c.content, c.description,
       ARRAY(SELECT DISTINCT s.page_number FROM document_chunk_sources s
             WHERE s.document_id = c.document_id AND s.version = c.version AND s.chunk_index = c.chunk_index
             ORDER BY s.page_number), combined.score::float8, combined.vector_rank, combined.keyword_rank
FROM combined JOIN corpus c USING (document_id, version, chunk_index)
ORDER BY combined.score DESC, c.document_id, c.version, c.chunk_index LIMIT $6`

func (repo *Repository) Search(ctx context.Context, query string, vector []float64, documentID *uuid.UUID, limit int) ([]Result, error) {
	encoded, err := json.Marshal(vector)
	if err != nil {
		return nil, fmt.Errorf("encode query vector: %w", err)
	}
	rows, err := repo.db.Query(ctx, searchSQL, query, string(encoded), documentID, Model, candidateLimit, limit)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()
	results := []Result{}
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.DocumentID, &result.Filename, &result.Version, &result.ChunkIndex,
			&result.Text, &result.Description, &result.Pages, &result.Score, &result.VectorRank, &result.KeywordRank); err != nil {
			return nil, fmt.Errorf("read search result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return results, nil
}

type Handler struct {
	reader   Reader
	embedder Embedder
	logger   *log.Logger
}

func NewHandler(reader Reader, embedder Embedder, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{reader, embedder, logger}
}

type request struct {
	Query      string  `json:"query"`
	TopK       *int    `json:"top_k"`
	DocumentID *string `json:"document_id"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) (string, []Result, bool) {
	var input request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, "invalid search request")
		return "", nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid search request")
		return "", nil, false
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || !utf8.ValidString(input.Query) || utf8.RuneCountInString(input.Query) > maxQueryRunes {
		writeError(w, 400, "query must contain 1 to 2000 characters")
		return "", nil, false
	}
	limit := 3
	if input.TopK != nil {
		limit = *input.TopK
	}
	if limit < 1 || limit > maxResults {
		writeError(w, 400, "top_k must be between 1 and 20")
		return "", nil, false
	}
	var documentID *uuid.UUID
	if input.DocumentID != nil {
		id, err := uuid.Parse(*input.DocumentID)
		if err != nil {
			writeError(w, 400, "document_id must be a UUID")
			return "", nil, false
		}
		documentID = &id
	}
	if h.embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "search requires OPENAI_API_KEY on the API server")
		return "", nil, false
	}
	vector, err := h.embedder.Embed(r.Context(), input.Query)
	if err != nil {
		h.logger.Printf("query embedding failed: %v", err)
		writeError(w, http.StatusBadGateway, "could not generate query embedding")
		return "", nil, false
	}
	if len(vector) != Dimensions {
		h.logger.Printf("query embedding has incorrect dimensions: %d", len(vector))
		writeError(w, http.StatusBadGateway, "query embedding has incorrect dimensions")
		return "", nil, false
	}
	nonzero := false
	for _, v := range vector {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			h.logger.Print("query embedding contains non-finite values")
			writeError(w, http.StatusBadGateway, "query embedding contains invalid values")
			return "", nil, false
		}
		nonzero = nonzero || v != 0
	}
	if !nonzero {
		h.logger.Print("query embedding is a zero vector")
		writeError(w, http.StatusBadGateway, "query embedding contains invalid values")
		return "", nil, false
	}
	results, err := h.reader.Search(r.Context(), input.Query, vector, documentID, limit)
	if err != nil {
		h.logger.Printf("hybrid search failed: %v", err)
		writeError(w, http.StatusInternalServerError, "could not search chunks")
		return "", nil, false
	}
	return input.Query, results, true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	_, results, ok := h.retrieve(w, r.WithContext(ctx))
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(struct {
		Results []Result `json:"results"`
	}{results}); err != nil {
		h.logger.Printf("write search response: %v", err)
	}
}
