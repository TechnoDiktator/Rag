package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
)

const idlePollInterval = time.Second
const errorPollInterval = 5 * time.Second

type messageWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type Publisher struct {
	db     *pgxpool.Pool
	writer messageWriter
	logger *log.Logger
}

type outboxEvent struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     json.RawMessage
}

func NewPublisher(db *pgxpool.Pool, writer messageWriter, logger *log.Logger) *Publisher {
	return &Publisher{db: db, writer: writer, logger: logger}
}

func (publisher *Publisher) Run(ctx context.Context) {
	for {
		published, err := publisher.publishOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			publisher.logf("publish outbox event: %v", err)
			if !wait(ctx, errorPollInterval) {
				return
			}
			continue
		}
		if !published && !wait(ctx, idlePollInterval) {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (publisher *Publisher) publishOne(ctx context.Context) (bool, error) {
	tx, err := publisher.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin outbox transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			publisher.logf("roll back outbox transaction: %v", err)
		}
	}()

	var event outboxEvent
	err = tx.QueryRow(ctx, `
		SELECT id, aggregate_id, event_type, payload
		FROM outbox_events
		WHERE published_at IS NULL
		ORDER BY created_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&event.ID, &event.AggregateID, &event.EventType, &event.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return false, fmt.Errorf("close empty outbox transaction: %w", rollbackErr)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select unpublished outbox event: %w", err)
	}

	if event.EventType != "document.uploaded" {
		return false, fmt.Errorf("unsupported outbox event type %q", event.EventType)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = publisher.writer.WriteMessages(writeCtx, kafka.Message{
		Key:   []byte(event.AggregateID),
		Value: event.Payload,
	})
	cancel()
	if err != nil {
		return false, fmt.Errorf("write event %s to Kafka: %w", event.ID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE outbox_events
		SET published_at = now()
		WHERE id = $1 AND published_at IS NULL
	`, event.ID); err != nil {
		return false, fmt.Errorf("mark outbox event %s published: %w", event.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit published outbox event %s: %w", event.ID, err)
	}
	return true, nil
}

func (publisher *Publisher) Close() error {
	if closer, ok := publisher.writer.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (publisher *Publisher) logf(format string, args ...any) {
	if publisher.logger != nil {
		publisher.logger.Printf(format, args...)
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
