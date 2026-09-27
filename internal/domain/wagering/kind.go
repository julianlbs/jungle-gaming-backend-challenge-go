package wagering

import "fmt"

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// ParseKind accepts every kind, including the internal OPENING; use it for persisted data.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return k, nil
	default:
		return "", fmt.Errorf("%w: kind %q", ErrInvalidField, s)
	}
}

// ParseExternalKind accepts only kinds that providers may submit.
func ParseExternalKind(s string) (Kind, error) {
	k, err := ParseKind(s)
	if err != nil {
		return "", err
	}
	if k == KindOpening {
		return "", ErrReservedKind
	}
	return k, nil
}

// RequiresReference reports whether the kind must reference an earlier transaction.
func (k Kind) RequiresReference() bool { return k == KindRefund || k == KindRollback }

// AllowsReference reports whether a reference may be supplied at all.
func (k Kind) AllowsReference() bool { return k == KindWin || k.RequiresReference() }

// AllowsZeroAmount reports whether the kind accepts "0.00"; LOSS also requires it.
func (k Kind) AllowsZeroAmount() bool { return k == KindLoss }

// IsReversal reports whether the kind undoes a referenced transaction.
func (k Kind) IsReversal() bool { return k.RequiresReference() }
