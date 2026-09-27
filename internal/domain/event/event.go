// Package event defines the integration events published through the outbox.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
)

const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"

	AggregateWagerTransaction = "WagerTransaction"
	AggregateWallet           = "Wallet"
)

var ErrInvalidEvent = errors.New("event: invalid event")

// Meta carries the identity and tracing fields shared by every event.
type Meta struct {
	EventID       uuid.UUID
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

func (m Meta) validate() error {
	if m.EventID == uuid.Nil || m.CorrelationID == "" || m.OccurredAt.IsZero() {
		return fmt.Errorf("%w: incomplete metadata", ErrInvalidEvent)
	}
	return nil
}

// Envelope is the wire format of every event. Type and version are set by constructors only.
type Envelope[T any] struct {
	EventID       string  `json:"eventId"`
	EventType     string  `json:"eventType"`
	AggregateType string  `json:"aggregateType"`
	AggregateID   string  `json:"aggregateId"`
	CorrelationID string  `json:"correlationId"`
	CausationID   *string `json:"causationId,omitempty"`
	OccurredAt    string  `json:"occurredAt"`
	Version       int     `json:"version"`
	Data          T       `json:"data"`

	partitionKey string
	occurredAt   time.Time
}

func newEnvelope[T any](meta Meta, eventType string, version int, aggregateType, aggregateID, partitionKey string, data T) (Envelope[T], error) {
	if err := meta.validate(); err != nil {
		return Envelope[T]{}, err
	}
	occurred := meta.OccurredAt.UTC()
	env := Envelope[T]{
		EventID:       meta.EventID.String(),
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		CorrelationID: meta.CorrelationID,
		OccurredAt:    occurred.Format(time.RFC3339Nano),
		Version:       version,
		Data:          data,
		partitionKey:  partitionKey,
		occurredAt:    occurred,
	}
	if meta.CausationID != "" {
		c := meta.CausationID
		env.CausationID = &c
	}
	return env, nil
}

// Outgoing is an encoded event ready to be stored in the outbox. Payload is the
// immutable snapshot that is published byte for byte on every attempt.
type Outgoing struct {
	EventID       uuid.UUID
	EventType     string
	EventVersion  int
	AggregateType string
	AggregateID   string
	PartitionKey  string
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
	Payload       []byte
}

func (e Envelope[T]) Encode() (Outgoing, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return Outgoing{}, fmt.Errorf("encode %s: %w", e.EventType, err)
	}
	id, err := uuid.Parse(e.EventID)
	if err != nil {
		return Outgoing{}, fmt.Errorf("%w: event id", ErrInvalidEvent)
	}
	causation := ""
	if e.CausationID != nil {
		causation = *e.CausationID
	}
	return Outgoing{
		EventID:       id,
		EventType:     e.EventType,
		EventVersion:  e.Version,
		AggregateType: e.AggregateType,
		AggregateID:   e.AggregateID,
		PartitionKey:  e.partitionKey,
		CorrelationID: e.CorrelationID,
		CausationID:   causation,
		OccurredAt:    e.occurredAt,
		Payload:       payload,
	}, nil
}

// MoneyView is the decimal-string representation used in event payloads.
type MoneyView struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func viewOf(m money.Money) MoneyView {
	return MoneyView{Amount: m.String(), Currency: m.Currency().String()}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
