package documents

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const chunkPageSize = 20

type Chunk struct {
	Index       int     `json:"index"`
	Text        string  `json:"text"`
	Pages       []int   `json:"pages"`
	Description *string `json:"description"`
}

type ChunkPage struct {
	Version int     `json:"version"`
	Chunks  []Chunk `json:"chunks"`
	HasMore bool    `json:"has_more"`
}

type chunkReader interface {
	GetChunks(context.Context, uuid.UUID, int) (ChunkPage, error)
}

// GetChunks reads the current version and its chunks in one database snapshot.
func (repo *Repository) GetChunks(ctx context.Context, id uuid.UUID, offset int) (ChunkPage, error) {
	rows, err := repo.db.Query(ctx, `
		SELECT d.current_version, c.chunk_index, c.content, c.description, c.pages
		FROM documents d
		LEFT JOIN LATERAL (
			SELECT c.chunk_index, c.content, e.description,
			       ARRAY(SELECT DISTINCT s.page_number FROM document_chunk_sources s
			             WHERE s.document_id = c.document_id AND s.version = c.version
			               AND s.chunk_index = c.chunk_index ORDER BY s.page_number) AS pages
			FROM document_chunks c
			LEFT JOIN document_chunk_embeddings e
			  ON e.document_id = c.document_id AND e.version = c.version AND e.chunk_index = c.chunk_index
			WHERE c.document_id = d.id AND c.version = d.current_version
			ORDER BY c.chunk_index LIMIT $2 OFFSET $3
		) c ON true
		WHERE d.id = $1
		ORDER BY c.chunk_index
	`, id, chunkPageSize+1, offset)
	if err != nil {
		return ChunkPage{}, err
	}
	defer rows.Close()
	result := ChunkPage{Chunks: []Chunk{}}
	found := false
	for rows.Next() {
		found = true
		var index *int
		var text *string
		var description *string
		var pages []int
		if err := rows.Scan(&result.Version, &index, &text, &description, &pages); err != nil {
			return ChunkPage{}, err
		}
		if index != nil {
			result.Chunks = append(result.Chunks, Chunk{Index: *index, Text: *text, Pages: pages, Description: description})
		}
	}
	if err := rows.Err(); err != nil {
		return ChunkPage{}, err
	}
	if !found {
		return ChunkPage{}, ErrDocumentNotFound
	}
	if len(result.Chunks) > chunkPageSize {
		result.HasMore = true
		result.Chunks = result.Chunks[:chunkPageSize]
	}
	return result, nil
}

type ChunkHandler struct {
	reader chunkReader
	logger *log.Logger
}

func NewChunkHandler(reader chunkReader, logger *log.Logger) *ChunkHandler {
	if logger == nil {
		logger = log.Default()
	}
	return &ChunkHandler{reader: reader, logger: logger}
}

func (handler *ChunkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("documentID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "document ID must be a UUID")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			writeJSONError(w, http.StatusBadRequest, "offset must be an integer between 0 and 1000000")
			return
		}
	}
	result, err := handler.reader.GetChunks(r.Context(), id, offset)
	if errors.Is(err, ErrDocumentNotFound) {
		writeJSONError(w, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		handler.logger.Printf("get chunks for %s: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, "could not retrieve chunks")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		handler.logger.Printf("write chunks response: %v", err)
	}
}

func (repo *Repository) GetPDFKey(ctx context.Context, id uuid.UUID, version int) (string, error) {
	var key string
	err := repo.db.QueryRow(ctx, `SELECT object_key FROM document_versions WHERE document_id = $1 AND version = $2`, id, version).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrDocumentNotFound
	}
	return key, err
}
