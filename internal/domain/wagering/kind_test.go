package wagering

import (
	"errors"
	"testing"
)

func TestParseExternalKind(t *testing.T) {
	for _, s := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if _, err := ParseExternalKind(s); err != nil {
			t.Errorf("ParseExternalKind(%q): %v", s, err)
		}
	}
	if _, err := ParseExternalKind("OPENING"); !errors.Is(err, ErrReservedKind) {
		t.Errorf("OPENING = %v, want ErrReservedKind", err)
	}
	for _, s := range []string{"", "bet", "Bet", "DEPOSIT", " BET"} {
		if _, err := ParseExternalKind(s); !errors.Is(err, ErrInvalidField) {
			t.Errorf("ParseExternalKind(%q) = %v, want ErrInvalidField", s, err)
		}
	}
	if k, err := ParseKind("OPENING"); err != nil || k != KindOpening {
		t.Errorf("ParseKind(OPENING) = %v, %v", k, err)
	}
}

func TestKindRules(t *testing.T) {
	cases := []struct {
		kind             Kind
		requiresRef      bool
		allowsRef        bool
		allowsZeroAmount bool
	}{
		{KindBet, false, false, false},
		{KindWin, false, true, false},
		{KindLoss, false, false, true},
		{KindRefund, true, true, false},
		{KindRollback, true, true, false},
		{KindOpening, false, false, false},
	}
	for _, tc := range cases {
		if got := tc.kind.RequiresReference(); got != tc.requiresRef {
			t.Errorf("%s.RequiresReference() = %v", tc.kind, got)
		}
		if got := tc.kind.AllowsReference(); got != tc.allowsRef {
			t.Errorf("%s.AllowsReference() = %v", tc.kind, got)
		}
		if got := tc.kind.AllowsZeroAmount(); got != tc.allowsZeroAmount {
			t.Errorf("%s.AllowsZeroAmount() = %v", tc.kind, got)
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	terminal := map[Status]bool{
		StatusPending: false, StatusPendingReference: false,
		StatusProcessed: true, StatusRejected: true, StatusFailed: true,
	}
	for s, want := range terminal {
		if s.IsTerminal() != want {
			t.Errorf("%s.IsTerminal() = %v", s, !want)
		}
		if parsed, err := ParseStatus(string(s)); err != nil || parsed != s {
			t.Errorf("ParseStatus(%s) = %v, %v", s, parsed, err)
		}
	}
	if _, err := ParseStatus("DONE"); !errors.Is(err, ErrInvalidField) {
		t.Errorf("ParseStatus(DONE) = %v", err)
	}
}

func TestFailureCodes(t *testing.T) {
	for _, c := range []FailureCode{
		FailureInsufficientFunds, FailureReversalInsufficientFunds, FailureReferenceNotFound,
		FailureReferenceNotProcessed, FailureReferenceMismatch, FailureReferenceAmountMismatch,
		FailureReferenceKindNotAllowed, FailureReferenceAlreadyReversed,
	} {
		if !c.IsRejection() || c.IsFailure() {
			t.Errorf("%s must be a rejection code", c)
		}
	}
	if FailureProcessingFailed.IsRejection() || !FailureProcessingFailed.IsFailure() {
		t.Error("PROCESSING_FAILED must be a failure code")
	}
	if FailureInsufficientFunds == FailureReversalInsufficientFunds {
		t.Error("bet and reversal insufficient funds must be distinguishable")
	}
	if _, err := ParseFailureCode("SOMETHING"); !errors.Is(err, ErrInvalidField) {
		t.Errorf("ParseFailureCode = %v", err)
	}
}
