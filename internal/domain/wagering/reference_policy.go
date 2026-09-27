package wagering

import "github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"

type Decision int

const (
	DecisionApply Decision = iota + 1
	DecisionWait
	DecisionReject
)

func (d Decision) String() string {
	switch d {
	case DecisionApply:
		return "APPLY"
	case DecisionWait:
		return "WAIT"
	case DecisionReject:
		return "REJECT"
	default:
		return "UNKNOWN"
	}
}

// Evaluation is the outcome of checking an operation against its reference.
type Evaluation struct {
	Decision Decision
	Code     FailureCode
}

var allowedTargets = map[Kind][]Kind{
	KindWin:      {KindBet},
	KindRefund:   {KindBet},
	KindRollback: {KindBet, KindWin, KindRefund},
}

// EvaluateReference decides whether op can be applied given its referenced
// transaction (nil when it has not arrived) and whether that reference already
// has a processed reversal. Each transaction accepts at most one successful
// reversal of any kind, which prevents returning the same debit twice.
func EvaluateReference(op, ref *Transaction, alreadyReversed bool) Evaluation {
	ext, ok := op.External()
	if !ok || ext.ReferenceExternalID == "" {
		return Evaluation{Decision: DecisionApply}
	}
	if ref == nil || ref.Status() == StatusPending || ref.Status() == StatusPendingReference {
		return Evaluation{Decision: DecisionWait}
	}
	if ref.Status() != StatusProcessed {
		return reject(FailureReferenceNotProcessed)
	}
	if !kindAllowed(op.Kind(), ref.Kind()) {
		return reject(FailureReferenceKindNotAllowed)
	}
	refExt, ok := ref.External()
	if !ok ||
		refExt.ProviderID != ext.ProviderID ||
		refExt.RoundID != ext.RoundID ||
		ref.PlayerID() != op.PlayerID() ||
		ref.WalletID() != op.WalletID() ||
		!ref.Amount().Currency().Equal(op.Amount().Currency()) {
		return reject(FailureReferenceMismatch)
	}
	if op.Kind().IsReversal() {
		if !ref.Amount().Equal(op.Amount()) {
			return reject(FailureReferenceAmountMismatch)
		}
		if alreadyReversed {
			return reject(FailureReferenceAlreadyReversed)
		}
	}
	return Evaluation{Decision: DecisionApply}
}

func kindAllowed(op, target Kind) bool {
	for _, k := range allowedTargets[op] {
		if k == target {
			return true
		}
	}
	return false
}

func reject(code FailureCode) Evaluation {
	return Evaluation{Decision: DecisionReject, Code: code}
}

// Effect is the balance movement an operation produces.
type Effect int

const (
	EffectNone Effect = iota
	EffectDebit
	EffectCredit
)

// EffectOf returns the movement for op; ROLLBACK moves opposite to its reference.
func EffectOf(op, ref *Transaction) Effect {
	switch op.Kind() {
	case KindBet:
		return EffectDebit
	case KindWin, KindRefund, KindOpening:
		return EffectCredit
	case KindRollback:
		if ref != nil && ref.Kind() == KindBet {
			return EffectCredit
		}
		return EffectDebit
	default:
		return EffectNone
	}
}

// Direction converts an effect into a ledger direction.
func (e Effect) Direction() (wallet.Direction, bool) {
	switch e {
	case EffectDebit:
		return wallet.DirectionDebit, true
	case EffectCredit:
		return wallet.DirectionCredit, true
	default:
		return "", false
	}
}

// InsufficientFundsCode distinguishes a bet without funds from a reversal that cannot be debited.
func InsufficientFundsCode(kind Kind) FailureCode {
	if kind.IsReversal() {
		return FailureReversalInsufficientFunds
	}
	return FailureInsufficientFunds
}
