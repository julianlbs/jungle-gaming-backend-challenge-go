package app

import (
	"time"

	"github.com/google/uuid"
)

type InboxOutcome string

const (
	InboxProcessed        InboxOutcome = "PROCESSED"
	InboxRejected         InboxOutcome = "REJECTED"
	InboxPendingReference InboxOutcome = "PENDING_REFERENCE"
	InboxReplayed         InboxOutcome = "REPLAYED"
)

// InboxEntry records that a consumer finished handling a broker message.
type InboxEntry struct {
	Consumer      string
	MessageID     string
	PayloadHash   string
	TransactionID uuid.UUID
	Outcome       InboxOutcome
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

// ClaimedEvent is an outbox row leased to a publisher.
type ClaimedEvent struct {
	ID            uuid.UUID
	PartitionKey  string
	EventType     string
	EventVersion  int
	CorrelationID string
	Payload       []byte
	Attempts      int
}
