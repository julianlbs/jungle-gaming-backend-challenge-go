package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
)

type InboxStore struct {
	db DBTX
}

func NewInboxStore(db DBTX) *InboxStore {
	return &InboxStore{db: db}
}

func (s *InboxStore) Find(ctx context.Context, consumer, messageID string) (app.InboxEntry, error) {
	var (
		e       app.InboxEntry
		txID    *uuid.UUID
		outcome string
		recv    time.Time
		done    time.Time
	)
	err := s.db.QueryRow(ctx,
		`SELECT consumer_name, message_id, payload_hash, transaction_id, outcome, received_at, completed_at
		 FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`,
		consumer, messageID,
	).Scan(&e.Consumer, &e.MessageID, &e.PayloadHash, &txID, &outcome, &recv, &done)
	if err != nil {
		return app.InboxEntry{}, notFound(err)
	}
	if txID != nil {
		e.TransactionID = *txID
	}
	e.Outcome = app.InboxOutcome(outcome)
	e.ReceivedAt, e.CompletedAt = recv.UTC(), done.UTC()
	return e, nil
}

// Insert records a handled message. A concurrent insert of the same message blocks until the
// other transaction ends and then fails as a retryable race.
func (s *InboxStore) Insert(ctx context.Context, e app.InboxEntry) error {
	var txID *uuid.UUID
	if e.TransactionID != uuid.Nil {
		txID = &e.TransactionID
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO inbox_messages
			(consumer_name, message_id, payload_hash, transaction_id, outcome, received_at, completed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Consumer, e.MessageID, e.PayloadHash, txID, string(e.Outcome), e.ReceivedAt, e.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("insert inbox message: %w", err)
	}
	return nil
}
