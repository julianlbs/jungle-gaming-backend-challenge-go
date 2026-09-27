package wagering

import (
	"fmt"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// Snapshot carries persisted transaction state for rehydration.
type Snapshot struct {
	ID            ID
	Origin        Origin
	Kind          Kind
	Status        Status
	Channel       Channel
	WalletID      wallet.ID
	PlayerID      wallet.PlayerID
	Amount        money.Money
	External      *External
	ReferenceID   *ID
	FailureCode   FailureCode
	FailureDetail string
	Result        *Result
	Attempts      int
	NextAttemptAt time.Time
	Deadline      time.Time
	CorrelationID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   time.Time
}

func (t *Transaction) Snapshot() Snapshot {
	s := Snapshot{
		ID:            t.id,
		Origin:        t.origin,
		Kind:          t.kind,
		Status:        t.status,
		Channel:       t.channel,
		WalletID:      t.walletID,
		PlayerID:      t.playerID,
		Amount:        t.amount,
		FailureCode:   t.failureCode,
		FailureDetail: t.failureDetail,
		Attempts:      t.attempts,
		NextAttemptAt: t.nextAttemptAt,
		Deadline:      t.deadline,
		CorrelationID: t.correlationID,
		CreatedAt:     t.createdAt,
		UpdatedAt:     t.updatedAt,
		CompletedAt:   t.completedAt,
	}
	if t.external != nil {
		e := *t.external
		s.External = &e
	}
	if t.referenceID != nil {
		r := *t.referenceID
		s.ReferenceID = &r
	}
	if t.result != nil {
		r := *t.result
		s.Result = &r
	}
	return s
}

// Rehydrate restores a persisted transaction without replaying transitions.
func Rehydrate(s Snapshot) (*Transaction, error) {
	if err := validateSnapshot(s); err != nil {
		return nil, err
	}
	t := &Transaction{
		id:            s.ID,
		origin:        s.Origin,
		kind:          s.Kind,
		status:        s.Status,
		channel:       s.Channel,
		walletID:      s.WalletID,
		playerID:      s.PlayerID,
		amount:        s.Amount,
		failureCode:   s.FailureCode,
		failureDetail: s.FailureDetail,
		attempts:      s.Attempts,
		nextAttemptAt: s.NextAttemptAt,
		deadline:      s.Deadline,
		correlationID: s.CorrelationID,
		createdAt:     s.CreatedAt,
		updatedAt:     s.UpdatedAt,
		completedAt:   s.CompletedAt,
	}
	if s.External != nil {
		e := *s.External
		t.external = &e
	}
	if s.ReferenceID != nil {
		r := *s.ReferenceID
		t.referenceID = &r
	}
	if s.Result != nil {
		r := *s.Result
		t.result = &r
	}
	return t, nil
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidTransaction, reason)
}

func validateSnapshot(s Snapshot) error {
	if s.ID.IsZero() || s.WalletID.IsZero() || s.PlayerID.IsZero() {
		return invalid("missing identifiers")
	}
	if _, err := ParseKind(string(s.Kind)); err != nil {
		return invalid("kind")
	}
	if _, err := ParseStatus(string(s.Status)); err != nil {
		return invalid("status")
	}
	if !s.Amount.IsValid() || s.Amount.IsNegative() {
		return invalid("amount")
	}
	if s.Kind.AllowsZeroAmount() != s.Amount.IsZero() {
		return invalid("amount does not match kind")
	}
	if s.CorrelationID == "" || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || s.Attempts < 0 {
		return invalid("metadata")
	}

	switch s.Origin {
	case OriginInternal:
		if s.Kind != KindOpening || s.Channel != ChannelInternal || s.External != nil || s.ReferenceID != nil {
			return invalid("internal transaction shape")
		}
	case OriginExternal:
		if s.Kind == KindOpening || (s.Channel != ChannelHTTP && s.Channel != ChannelSQS) || s.External == nil {
			return invalid("external transaction shape")
		}
		e := s.External
		if e.ProviderID == "" || e.ExternalID == "" || e.IdempotencyKey == "" || e.PayloadHash == "" || e.RoundID == "" || e.GameID == "" {
			return invalid("external metadata")
		}
		if (e.ReferenceExternalID != "" && !s.Kind.AllowsReference()) ||
			(e.ReferenceExternalID == "" && s.Kind.RequiresReference()) {
			return invalid("reference does not match kind")
		}
	default:
		return invalid("origin")
	}

	if s.ReferenceID != nil && (!s.Kind.AllowsReference() || s.ReferenceID.IsZero()) {
		return invalid("resolved reference")
	}

	switch s.Status {
	case StatusProcessed:
		if s.Result == nil || s.FailureCode != "" || s.CompletedAt.IsZero() {
			return invalid("processed shape")
		}
		if s.Kind.RequiresReference() && s.ReferenceID == nil {
			return invalid("processed reversal without reference")
		}
	case StatusRejected:
		if s.Result == nil || !s.FailureCode.IsRejection() || s.CompletedAt.IsZero() {
			return invalid("rejected shape")
		}
	case StatusFailed:
		if !s.FailureCode.IsFailure() || s.CompletedAt.IsZero() {
			return invalid("failed shape")
		}
	case StatusPendingReference:
		if s.NextAttemptAt.IsZero() || s.Deadline.IsZero() || s.FailureCode != "" || s.Result != nil {
			return invalid("pending reference shape")
		}
	case StatusPending:
		if s.FailureCode != "" || s.Result != nil {
			return invalid("pending shape")
		}
	}
	if s.Result != nil && (!s.Result.Balance.IsValid() || s.Result.Balance.IsNegative() ||
		!s.Result.Balance.Currency().Equal(s.Amount.Currency()) || s.Result.WalletVersion < wallet.InitialVersion) {
		return invalid("result")
	}
	return nil
}
