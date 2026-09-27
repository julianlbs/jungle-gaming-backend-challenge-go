package wagering

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// ID identifies a wager transaction. The zero value is invalid.
type ID struct{ value uuid.UUID }

func NewID(u uuid.UUID) (ID, error) {
	if u == uuid.Nil {
		return ID{}, fmt.Errorf("%w: transaction id", ErrInvalidField)
	}
	return ID{u}, nil
}

func ParseID(s string) (ID, error) {
	if len(s) != 36 {
		return ID{}, fieldError("transactionId", "must be a canonical UUID")
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return ID{}, fieldError("transactionId", "must be a canonical UUID")
	}
	return NewID(u)
}

func (id ID) UUID() uuid.UUID { return id.value }
func (id ID) String() string  { return id.value.String() }
func (id ID) IsZero() bool    { return id.value == uuid.Nil }

var (
	providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	tokenPattern      = regexp.MustCompile(`^[\x21-\x7E]+$`)
)

const (
	maxTokenLength          = 128
	maxIdempotencyKeyLength = 255
	maxCorrelationIDLength  = 128
)

// External holds provider metadata; it is absent for internal transactions.
type External struct {
	ProviderID          string
	ExternalID          string
	IdempotencyKey      string
	PayloadHash         string
	RoundID             string
	GameID              string
	ReferenceExternalID string
}

// Result is the financial outcome returned to the provider and reused on replay.
type Result struct {
	Balance       money.Money
	WalletVersion int64
}

// Transaction is a wallet operation and its processing state.
type Transaction struct {
	id            ID
	origin        Origin
	kind          Kind
	status        Status
	channel       Channel
	walletID      wallet.ID
	playerID      wallet.PlayerID
	amount        money.Money
	external      *External
	referenceID   *ID
	failureCode   FailureCode
	failureDetail string
	result        *Result
	attempts      int
	nextAttemptAt time.Time
	deadline      time.Time
	correlationID string
	createdAt     time.Time
	updatedAt     time.Time
	completedAt   time.Time
}

// ExternalParams are the raw provider inputs for a new operation.
type ExternalParams struct {
	ID                  ID
	Channel             Channel
	WalletID            wallet.ID
	PlayerID            wallet.PlayerID
	Kind                Kind
	Amount              money.Money
	ProviderID          string
	ExternalID          string
	IdempotencyKey      string
	RoundID             string
	GameID              string
	ReferenceExternalID string
	CorrelationID       string
	Now                 time.Time
}

// NewExternal validates a provider operation and creates it in PENDING.
func NewExternal(p ExternalParams) (*Transaction, error) {
	if p.ID.IsZero() {
		return nil, fmt.Errorf("%w: missing transaction id", ErrInvalidTransaction)
	}
	if p.Channel != ChannelHTTP && p.Channel != ChannelSQS {
		return nil, fieldError("channel", "must be HTTP or SQS")
	}
	if p.Kind == KindOpening {
		return nil, ErrReservedKind
	}
	if _, err := ParseKind(string(p.Kind)); err != nil {
		return nil, fieldError("kind", "unknown kind")
	}
	if p.WalletID.IsZero() {
		return nil, fieldError("walletId", "is required")
	}
	if p.PlayerID.IsZero() {
		return nil, fieldError("playerId", "is required")
	}
	if err := validateAmount(p.Kind, p.Amount); err != nil {
		return nil, err
	}
	if !providerIDPattern.MatchString(p.ProviderID) {
		return nil, fieldError("providerId", "must match ^[a-z0-9][a-z0-9-]{0,63}$")
	}
	if err := validateToken("externalTransactionId", p.ExternalID, maxTokenLength); err != nil {
		return nil, err
	}
	if err := validateToken("idempotencyKey", p.IdempotencyKey, maxIdempotencyKeyLength); err != nil {
		return nil, err
	}
	if err := validateToken("roundId", p.RoundID, maxTokenLength); err != nil {
		return nil, err
	}
	if err := validateToken("gameId", p.GameID, maxTokenLength); err != nil {
		return nil, err
	}
	if err := validateReference(p.Kind, p.ReferenceExternalID, p.ExternalID); err != nil {
		return nil, err
	}
	if err := validateCorrelationID(p.CorrelationID); err != nil {
		return nil, err
	}
	if p.Now.IsZero() {
		return nil, fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}

	hash := PayloadHash(CanonicalPayload{
		ProviderID:                     p.ProviderID,
		ExternalTransactionID:          p.ExternalID,
		PlayerID:                       p.PlayerID.String(),
		WalletID:                       p.WalletID.String(),
		RoundID:                        p.RoundID,
		GameID:                         p.GameID,
		Kind:                           string(p.Kind),
		Amount:                         p.Amount.String(),
		Currency:                       p.Amount.Currency().String(),
		ReferenceExternalTransactionID: p.ReferenceExternalID,
	})

	return &Transaction{
		id:       p.ID,
		origin:   OriginExternal,
		kind:     p.Kind,
		status:   StatusPending,
		channel:  p.Channel,
		walletID: p.WalletID,
		playerID: p.PlayerID,
		amount:   p.Amount,
		external: &External{
			ProviderID:          p.ProviderID,
			ExternalID:          p.ExternalID,
			IdempotencyKey:      p.IdempotencyKey,
			PayloadHash:         hash,
			RoundID:             p.RoundID,
			GameID:              p.GameID,
			ReferenceExternalID: p.ReferenceExternalID,
		},
		correlationID: p.CorrelationID,
		createdAt:     p.Now,
		updatedAt:     p.Now,
	}, nil
}

// NewOpening creates the internal credit that funds a newly opened wallet.
func NewOpening(id ID, walletID wallet.ID, playerID wallet.PlayerID, amount money.Money, correlationID string, now time.Time) (*Transaction, error) {
	switch {
	case id.IsZero():
		return nil, fmt.Errorf("%w: missing transaction id", ErrInvalidTransaction)
	case walletID.IsZero() || playerID.IsZero():
		return nil, fmt.Errorf("%w: missing wallet or player", ErrInvalidTransaction)
	case !amount.IsValid() || !amount.IsPositive():
		return nil, fmt.Errorf("%w: opening amount must be positive", ErrInvalidTransaction)
	case now.IsZero():
		return nil, fmt.Errorf("%w: missing timestamp", ErrInvalidTransaction)
	}
	if err := validateCorrelationID(correlationID); err != nil {
		return nil, err
	}
	return &Transaction{
		id:            id,
		origin:        OriginInternal,
		kind:          KindOpening,
		status:        StatusPending,
		channel:       ChannelInternal,
		walletID:      walletID,
		playerID:      playerID,
		amount:        amount,
		correlationID: correlationID,
		createdAt:     now,
		updatedAt:     now,
	}, nil
}

func validateAmount(kind Kind, amount money.Money) error {
	if !amount.IsValid() {
		return fieldError("money", "is required")
	}
	if amount.IsNegative() {
		return fieldError("money.amount", "must not be negative")
	}
	if kind.AllowsZeroAmount() {
		if !amount.IsZero() {
			return fieldError("money.amount", "LOSS requires 0.00")
		}
		return nil
	}
	if !amount.IsPositive() {
		return fieldError("money.amount", fmt.Sprintf("%s requires a positive amount", kind))
	}
	return nil
}

func validateToken(field, value string, maxLen int) error {
	if value == "" {
		return fieldError(field, "is required")
	}
	if len(value) > maxLen {
		return fieldError(field, fmt.Sprintf("must have at most %d characters", maxLen))
	}
	if !tokenPattern.MatchString(value) {
		return fieldError(field, "must contain only printable ASCII without spaces")
	}
	return nil
}

func validateReference(kind Kind, ref, own string) error {
	switch {
	case ref == "" && kind.RequiresReference():
		return fieldError("referenceExternalTransactionId", fmt.Sprintf("is required for %s", kind))
	case ref != "" && !kind.AllowsReference():
		return fieldError("referenceExternalTransactionId", fmt.Sprintf("is not allowed for %s", kind))
	case ref == "":
		return nil
	case ref == own:
		return fieldError("referenceExternalTransactionId", "must not reference itself")
	default:
		return validateToken("referenceExternalTransactionId", ref, maxTokenLength)
	}
}

func validateCorrelationID(v string) error {
	if v == "" || len(v) > maxCorrelationIDLength || !tokenPattern.MatchString(v) {
		return fmt.Errorf("%w: correlation id", ErrInvalidTransaction)
	}
	return nil
}

var transitions = map[Status][]Status{
	StatusPending:          {StatusProcessed, StatusRejected, StatusPendingReference, StatusFailed},
	StatusPendingReference: {StatusPending, StatusRejected, StatusFailed},
}

func (t *Transaction) transition(to Status, now time.Time) error {
	if !slices.Contains(transitions[t.status], to) {
		return &TransitionError{From: t.status, To: to}
	}
	t.status = to
	t.touch(now)
	return nil
}

func (t *Transaction) touch(now time.Time) {
	if now.After(t.updatedAt) {
		t.updatedAt = now
	}
}

func (t *Transaction) complete(now time.Time) {
	t.completedAt = t.updatedAt
	if now.After(t.completedAt) {
		t.completedAt = now
	}
	t.nextAttemptAt = time.Time{}
}

func (t *Transaction) validateResult(r Result) error {
	if !r.Balance.IsValid() || r.Balance.IsNegative() || !r.Balance.Currency().Equal(t.amount.Currency()) {
		return fmt.Errorf("%w: invalid result balance", ErrInvalidTransaction)
	}
	if r.WalletVersion < wallet.InitialVersion {
		return fmt.Errorf("%w: invalid result wallet version", ErrInvalidTransaction)
	}
	return nil
}

func (t *Transaction) setReference(ref *ID) error {
	if ref == nil {
		return nil
	}
	if !t.kind.AllowsReference() || ref.IsZero() || *ref == t.id {
		return fmt.Errorf("%w: invalid resolved reference", ErrInvalidTransaction)
	}
	r := *ref
	t.referenceID = &r
	return nil
}

// MarkProcessed completes the operation with the balance observed after it.
func (t *Transaction) MarkProcessed(result Result, reference *ID, now time.Time) error {
	if err := t.validateResult(result); err != nil {
		return err
	}
	if t.kind.RequiresReference() && reference == nil {
		return fmt.Errorf("%w: %s requires a resolved reference", ErrInvalidTransaction, t.kind)
	}
	if err := t.transition(StatusProcessed, now); err != nil {
		return err
	}
	if err := t.setReference(reference); err != nil {
		return err
	}
	r := result
	t.result = &r
	t.complete(now)
	return nil
}

// Reject records a definitive business rejection with the balance observed at that time.
func (t *Transaction) Reject(code FailureCode, observed Result, reference *ID, now time.Time) error {
	if !code.IsRejection() {
		return fmt.Errorf("%w: %q is not a rejection code", ErrInvalidTransaction, code)
	}
	if err := t.validateResult(observed); err != nil {
		return err
	}
	if err := t.transition(StatusRejected, now); err != nil {
		return err
	}
	if err := t.setReference(reference); err != nil {
		return err
	}
	r := observed
	t.result = &r
	t.failureCode = code
	t.complete(now)
	return nil
}

// AwaitReference parks the operation until its reference is available or the deadline passes.
func (t *Transaction) AwaitReference(nextAttemptAt, deadline, now time.Time) error {
	if t.external == nil || t.external.ReferenceExternalID == "" {
		return fmt.Errorf("%w: no reference to wait for", ErrInvalidTransaction)
	}
	if nextAttemptAt.IsZero() || deadline.IsZero() || deadline.Before(nextAttemptAt) {
		return fmt.Errorf("%w: invalid reference schedule", ErrInvalidTransaction)
	}
	if err := t.transition(StatusPendingReference, now); err != nil {
		return err
	}
	t.nextAttemptAt = nextAttemptAt
	t.deadline = deadline
	return nil
}

// Reschedule keeps waiting for the reference until nextAttemptAt.
func (t *Transaction) Reschedule(nextAttemptAt, now time.Time) error {
	if t.status != StatusPendingReference {
		return &TransitionError{From: t.status, To: StatusPendingReference}
	}
	if nextAttemptAt.IsZero() {
		return fmt.Errorf("%w: invalid reference schedule", ErrInvalidTransaction)
	}
	t.nextAttemptAt = nextAttemptAt
	t.touch(now)
	return nil
}

// Resume moves a waiting operation back to PENDING so it can be processed.
func (t *Transaction) Resume(now time.Time) error {
	return t.transition(StatusPending, now)
}

// Fail records a permanent infrastructure failure for audit.
func (t *Transaction) Fail(detail string, now time.Time) error {
	if err := t.transition(StatusFailed, now); err != nil {
		return err
	}
	t.failureCode = FailureProcessingFailed
	t.failureDetail = detail
	t.complete(now)
	return nil
}

// ReferenceWaitExhausted reports whether the wait budget is spent.
func (t *Transaction) ReferenceWaitExhausted(now time.Time, maxAttempts int) bool {
	return t.attempts >= maxAttempts || !now.Before(t.deadline)
}

// SamePayload reports whether an incoming request is equivalent to this one.
func (t *Transaction) SamePayload(hash string) bool {
	return t.external != nil && t.external.PayloadHash == hash
}

func (t *Transaction) ID() ID                    { return t.id }
func (t *Transaction) Origin() Origin            { return t.origin }
func (t *Transaction) Kind() Kind                { return t.kind }
func (t *Transaction) Status() Status            { return t.status }
func (t *Transaction) Channel() Channel          { return t.channel }
func (t *Transaction) WalletID() wallet.ID       { return t.walletID }
func (t *Transaction) PlayerID() wallet.PlayerID { return t.playerID }
func (t *Transaction) Amount() money.Money       { return t.amount }
func (t *Transaction) FailureCode() FailureCode  { return t.failureCode }
func (t *Transaction) FailureDetail() string     { return t.failureDetail }
func (t *Transaction) Attempts() int             { return t.attempts }
func (t *Transaction) NextAttemptAt() time.Time  { return t.nextAttemptAt }
func (t *Transaction) Deadline() time.Time       { return t.deadline }
func (t *Transaction) CorrelationID() string     { return t.correlationID }
func (t *Transaction) CreatedAt() time.Time      { return t.createdAt }
func (t *Transaction) UpdatedAt() time.Time      { return t.updatedAt }
func (t *Transaction) CompletedAt() time.Time    { return t.completedAt }

// External returns a copy of the provider metadata, or false for internal transactions.
func (t *Transaction) External() (External, bool) {
	if t.external == nil {
		return External{}, false
	}
	return *t.external, true
}

func (t *Transaction) ReferenceID() (ID, bool) {
	if t.referenceID == nil {
		return ID{}, false
	}
	return *t.referenceID, true
}

func (t *Transaction) Result() (Result, bool) {
	if t.result == nil {
		return Result{}, false
	}
	return *t.result, true
}
