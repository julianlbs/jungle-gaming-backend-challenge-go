package postgres

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/event"
)

const maxLastErrorLen = 1000

// OutboxStore writes events inside business transactions and serves the relay. Lease
// timestamps use the database clock so that every instance agrees on expiry.
type OutboxStore struct {
	db DBTX
}

func NewOutboxStore(db DBTX) *OutboxStore {
	return &OutboxStore{db: db}
}

func (s *OutboxStore) Insert(ctx context.Context, events ...event.Outgoing) error {
	for _, e := range events {
		aggregateID, err := uuid.Parse(e.AggregateID)
		if err != nil {
			return fmt.Errorf("outbox event %s: aggregate id: %w", e.EventID, err)
		}
		_, err = s.db.Exec(ctx,
			`INSERT INTO outbox_events (id, aggregate_type, aggregate_id, partition_key, event_type,
				event_version, correlation_id, causation_id, payload, occurred_at, next_attempt_at, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())`,
			e.EventID, e.AggregateType, aggregateID, e.PartitionKey, e.EventType, e.EventVersion,
			e.CorrelationID, nullString(e.CausationID), string(e.Payload), e.OccurredAt,
		)
		if err != nil {
			return fmt.Errorf("insert outbox event %s: %w", e.EventID, err)
		}
	}
	return nil
}

// Claim leases up to limit due events to owner for the lease duration.
func (s *OutboxStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.ClaimedEvent, error) {
	rows, err := s.db.Query(ctx,
		`UPDATE outbox_events o
		    SET attempts = o.attempts + 1,
		        locked_by = $1,
		        next_attempt_at = now() + make_interval(secs => $2)
		  WHERE o.id IN (
		        SELECT id FROM outbox_events
		         WHERE published_at IS NULL AND next_attempt_at <= now()
		         ORDER BY seq
		         LIMIT $3
		         FOR UPDATE SKIP LOCKED)
		 RETURNING o.id, o.partition_key, o.event_type, o.event_version, o.correlation_id,
		           o.payload::text, o.attempts, o.seq`,
		owner, lease.Seconds(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()

	type claimed struct {
		app.ClaimedEvent
		seq int64
	}
	var batch []claimed
	for rows.Next() {
		var (
			c       claimed
			payload string
		)
		if err := rows.Scan(&c.ID, &c.PartitionKey, &c.EventType, &c.EventVersion, &c.CorrelationID,
			&payload, &c.Attempts, &c.seq); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		c.Payload = []byte(payload)
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read claimed outbox events: %w", err)
	}

	// RETURNING does not preserve the subquery order.
	out := make([]app.ClaimedEvent, len(batch))
	slices.SortFunc(batch, func(a, b claimed) int { return cmp.Compare(a.seq, b.seq) })
	for i, c := range batch {
		out[i] = c.ClaimedEvent
	}
	return out, nil
}

// MarkPublished reports false when the event was already confirmed by another publisher.
func (s *OutboxStore) MarkPublished(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE outbox_events SET published_at = now(), locked_by = NULL, last_error = NULL
		 WHERE id = $1 AND published_at IS NULL`, id)
	if err != nil {
		return false, fmt.Errorf("mark outbox event published: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Reschedule releases a failed event owned by owner for another attempt after delay.
func (s *OutboxStore) Reschedule(ctx context.Context, id uuid.UUID, owner string, delay time.Duration, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
		if len(msg) > maxLastErrorLen {
			msg = msg[:maxLastErrorLen]
		}
	}
	_, err := s.db.Exec(ctx,
		`UPDATE outbox_events
		    SET next_attempt_at = now() + make_interval(secs => $3), locked_by = NULL, last_error = $4
		  WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`,
		id, owner, delay.Seconds(), nullString(msg))
	if err != nil {
		return fmt.Errorf("reschedule outbox event: %w", err)
	}
	return nil
}

// ReleaseLeases makes every unpublished event held by owner immediately claimable.
func (s *OutboxStore) ReleaseLeases(ctx context.Context, owner string) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE outbox_events SET next_attempt_at = now(), locked_by = NULL
		 WHERE locked_by = $1 AND published_at IS NULL`, owner)
	if err != nil {
		return 0, fmt.Errorf("release outbox leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgePublished deletes up to limit events published before the cutoff.
func (s *OutboxStore) PurgePublished(ctx context.Context, before time.Time, limit int) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM outbox_events WHERE id IN (
			SELECT id FROM outbox_events WHERE published_at < $1 ORDER BY seq LIMIT $2)`,
		before, limit)
	if err != nil {
		return 0, fmt.Errorf("purge published outbox events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Backlog returns the number of unpublished events and the age of the oldest one.
func (s *OutboxStore) Backlog(ctx context.Context) (int64, time.Duration, error) {
	var (
		count int64
		age   float64
	)
	err := s.db.QueryRow(ctx,
		`SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)::float8
		 FROM outbox_events WHERE published_at IS NULL`).Scan(&count, &age)
	if err != nil {
		return 0, 0, fmt.Errorf("outbox backlog: %w", err)
	}
	return count, time.Duration(age * float64(time.Second)), nil
}
