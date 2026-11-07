package documents

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Status struct {
	DocumentID string    `json:"document_id"`
	Version    int       `json:"version"`
	Status     string    `json:"status"`
	PageCount  *int      `json:"page_count"`
	JobStage   string    `json:"job_stage"`
	JobStatus  string    `json:"job_status"`
	Error      *string   `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type statusReader interface {
	GetStatus(context.Context, uuid.UUID) (Status, error)
}

func (repo *Repository) GetStatus(ctx context.Context, id uuid.UUID) (Status, error) {
	var status Status
	status.DocumentID = id.String()
	err := repo.db.QueryRow(ctx, `
		SELECT d.current_version, d.status, d.page_count, d.created_at, d.updated_at,
		       COALESCE(j.stage, ''), COALESCE(j.status, ''), j.error_message
		FROM documents AS d
		LEFT JOIN ingestion_jobs AS j
		  ON j.document_id = d.id AND j.version = d.current_version
		WHERE d.id = $1
		ORDER BY j.created_at DESC NULLS LAST
		LIMIT 1
	`, id).Scan(
		&status.Version,
		&status.Status,
		&status.PageCount,
		&status.CreatedAt,
		&status.UpdatedAt,
		&status.JobStage,
		&status.JobStatus,
		&status.Error,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrDocumentNotFound
	}
	if err != nil {
		return Status{}, err
	}
	return status, nil
}

type StatusHandler struct {
	reader statusReader
	logger *log.Logger
}

func NewStatusHandler(reader statusReader, logger *log.Logger) *StatusHandler {
	if logger == nil {
		logger = log.Default()
	}
	return &StatusHandler{reader: reader, logger: logger}
}

func (handler *StatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("documentID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "document ID must be a UUID")
		return
	}
	status, err := handler.reader.GetStatus(r.Context(), id)
	if errors.Is(err, ErrDocumentNotFound) {
		writeJSONError(w, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		handler.logger.Printf("get document status for %s: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, "could not retrieve document status")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		handler.logger.Printf("write document status response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message})
}
