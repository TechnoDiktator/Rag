package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDuplicateContent = errors.New("document content already exists")
var ErrCommitOutcomeUnknown = errors.New("document registration commit outcome is unknown")

type Registration struct {
	DocumentID    uuid.UUID
	EventID       uuid.UUID
	CorrelationID uuid.UUID
	Filename      string
	ObjectKey     string
	ContentHash   string
	FileSizeBytes int64
	OccurredAt    time.Time
}

type Repository struct {
	db *pgxpool.Pool
}

type uploadedEvent struct {
	EventID         uuid.UUID       `json:"event_id"`
	EventType       string          `json:"event_type"`
	EventVersion    int             `json:"event_version"`
	OccurredAt      time.Time       `json:"occurred_at"`
	CorrelationID   uuid.UUID       `json:"correlation_id"`
	DocumentID      uuid.UUID       `json:"document_id"`
	DocumentVersion int             `json:"document_version"`
	Payload         uploadedPayload `json:"payload"`
}

type uploadedPayload struct {
	Filename      string `json:"filename"`
	ObjectKey     string `json:"object_key"`
	ContentHash   string `json:"content_hash"`
	MIMEType      string `json:"mime_type"`
	FileSizeBytes int64  `json:"file_size_bytes"`
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (repo *Repository) RegisterUploaded(ctx context.Context, registration Registration) (resultErr error) {
	tx, err := repo.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin document registration: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("roll back document registration: %w", rollbackErr))
		}
	}()

	_, err = tx.Exec(ctx, `
		INSERT INTO documents (
			id, filename, object_key, content_hash, mime_type,
			file_size_bytes, current_version, status
		) VALUES ($1, $2, $3, $4, 'application/pdf', $5, 1, 'QUEUED')
	`, registration.DocumentID, registration.Filename, registration.ObjectKey,
		registration.ContentHash, registration.FileSizeBytes)
	if err != nil {
		return classifyDatabaseError("insert document", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO document_versions (
			document_id, version, object_key, content_hash, status
		) VALUES ($1, 1, $2, $3, 'QUEUED')
	`, registration.DocumentID, registration.ObjectKey, registration.ContentHash)
	if err != nil {
		return fmt.Errorf("insert document version: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (document_id, version, stage, status)
		VALUES ($1, 1, 'PARSE', 'QUEUED')
	`, registration.DocumentID)
	if err != nil {
		return fmt.Errorf("insert parse job: %w", err)
	}

	event := uploadedEvent{
		EventID:         registration.EventID,
		EventType:       "document.uploaded",
		EventVersion:    1,
		OccurredAt:      registration.OccurredAt.UTC(),
		CorrelationID:   registration.CorrelationID,
		DocumentID:      registration.DocumentID,
		DocumentVersion: 1,
		Payload: uploadedPayload{
			Filename:      registration.Filename,
			ObjectKey:     registration.ObjectKey,
			ContentHash:   registration.ContentHash,
			MIMEType:      "application/pdf",
			FileSizeBytes: registration.FileSizeBytes,
		},
	}
	eventPayload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode upload event: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (id, aggregate_id, event_type, event_version, payload)
		VALUES ($1, $2, 'document.uploaded', 1, $3::jsonb)
	`, registration.EventID, registration.DocumentID, string(eventPayload))
	if err != nil {
		return fmt.Errorf("insert upload event into outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitOutcomeUnknown, err)
	}
	return nil
}

func classifyDatabaseError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrDuplicateContent, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
