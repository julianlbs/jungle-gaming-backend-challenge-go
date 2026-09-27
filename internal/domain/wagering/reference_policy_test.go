package wagering

import (
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

func referenced(t *testing.T, kind Kind, amount string, status Status) *Transaction {
	t.Helper()
	p := params(t, kind, amount)
	p.ExternalID = "transaction-100"
	if kind.RequiresReference() {
		p.ReferenceExternalID = "transaction-050"
	}
	tx := mustExternal(t, p)
	switch status {
	case StatusProcessed:
		var ref *ID
		if kind.RequiresReference() {
			ref = refPtr(t)
		}
		if err := tx.MarkProcessed(result(t, "100.00", 2), ref, t0); err != nil {
			t.Fatal(err)
		}
	case StatusRejected:
		if err := tx.Reject(FailureInsufficientFunds, result(t, "0.00", 1), nil, t0); err != nil {
			t.Fatal(err)
		}
	case StatusFailed:
		if err := tx.Fail("x", t0); err != nil {
			t.Fatal(err)
		}
	case StatusPendingReference:
		if err := tx.AwaitReference(t0.Add(1), t0.Add(2), t0); err != nil {
			t.Fatal(err)
		}
	}
	return tx
}

func operation(t *testing.T, kind Kind, amount string, mutate func(*ExternalParams)) *Transaction {
	t.Helper()
	p := params(t, kind, amount)
	p.ReferenceExternalID = "transaction-100"
	if mutate != nil {
		mutate(&p)
	}
	return mustExternal(t, p)
}

func TestEvaluateReference(t *testing.T) {
	cases := []struct {
		name     string
		op       *Transaction
		ref      *Transaction
		reversed bool
		want     Decision
		code     FailureCode
	}{
		{"missing reference waits", operation(t, KindRefund, "25.00", nil), nil, false, DecisionWait, ""},
		{"pending reference waits", operation(t, KindRefund, "25.00", nil), referenced(t, KindBet, "25.00", StatusPending), false, DecisionWait, ""},
		{"waiting reference waits", operation(t, KindRollback, "25.00", nil), referenced(t, KindRefund, "25.00", StatusPendingReference), false, DecisionWait, ""},
		{"rejected reference", operation(t, KindRefund, "25.00", nil), referenced(t, KindBet, "25.00", StatusRejected), false, DecisionReject, FailureReferenceNotProcessed},
		{"failed reference", operation(t, KindRollback, "25.00", nil), referenced(t, KindBet, "25.00", StatusFailed), false, DecisionReject, FailureReferenceNotProcessed},
		{"refund of bet", operation(t, KindRefund, "25.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionApply, ""},
		{"refund of win", operation(t, KindRefund, "25.00", nil), referenced(t, KindWin, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceKindNotAllowed},
		{"rollback of bet", operation(t, KindRollback, "25.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionApply, ""},
		{"rollback of win", operation(t, KindRollback, "25.00", nil), referenced(t, KindWin, "25.00", StatusProcessed), false, DecisionApply, ""},
		{"rollback of refund", operation(t, KindRollback, "25.00", nil), referenced(t, KindRefund, "25.00", StatusProcessed), false, DecisionApply, ""},
		{"rollback of rollback", operation(t, KindRollback, "25.00", nil), referenced(t, KindRollback, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceKindNotAllowed},
		{"rollback of loss", operation(t, KindRollback, "25.00", nil), referenced(t, KindLoss, "0.00", StatusProcessed), false, DecisionReject, FailureReferenceKindNotAllowed},
		{"win referencing bet", operation(t, KindWin, "50.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionApply, ""},
		{"win referencing win", operation(t, KindWin, "50.00", nil), referenced(t, KindWin, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceKindNotAllowed},
		{"win on reversed bet is allowed", operation(t, KindWin, "50.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), true, DecisionApply, ""},
		{"partial refund", operation(t, KindRefund, "10.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceAmountMismatch},
		{"refund after reversal", operation(t, KindRefund, "25.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), true, DecisionReject, FailureReferenceAlreadyReversed},
		{"rollback after reversal", operation(t, KindRollback, "25.00", nil), referenced(t, KindBet, "25.00", StatusProcessed), true, DecisionReject, FailureReferenceAlreadyReversed},
		{"round mismatch", operation(t, KindRefund, "25.00", func(p *ExternalParams) { p.RoundID = "round-1" }), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceMismatch},
		{"player mismatch", operation(t, KindRefund, "25.00", func(p *ExternalParams) {
			p.PlayerID, _ = wallet.ParsePlayerID("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2")
		}), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceMismatch},
		{"wallet mismatch", operation(t, KindRefund, "25.00", func(p *ExternalParams) {
			p.WalletID, _ = wallet.ParseID("0192f291-27dd-7d3f-8071-5f8685deef38")
		}), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceMismatch},
		{"provider mismatch", operation(t, KindRefund, "25.00", func(p *ExternalParams) { p.ProviderID = "provider-b" }), referenced(t, KindBet, "25.00", StatusProcessed), false, DecisionReject, FailureReferenceMismatch},
		{"no reference applies", operation(t, KindWin, "5.00", func(p *ExternalParams) { p.ReferenceExternalID = "" }), nil, false, DecisionApply, ""},
	}
	for _, tc := range cases {
		got := EvaluateReference(tc.op, tc.ref, tc.reversed)
		if got.Decision != tc.want || got.Code != tc.code {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, got.Decision, got.Code, tc.want, tc.code)
		}
	}
}

func TestEffectOf(t *testing.T) {
	bet := referenced(t, KindBet, "25.00", StatusProcessed)
	win := referenced(t, KindWin, "25.00", StatusProcessed)
	refund := referenced(t, KindRefund, "25.00", StatusProcessed)
	cases := []struct {
		name string
		op   *Transaction
		ref  *Transaction
		want Effect
	}{
		{"bet debits", operation(t, KindBet, "1.00", func(p *ExternalParams) { p.ReferenceExternalID = "" }), nil, EffectDebit},
		{"win credits", operation(t, KindWin, "1.00", nil), bet, EffectCredit},
		{"loss does nothing", operation(t, KindLoss, "0.00", func(p *ExternalParams) { p.ReferenceExternalID = "" }), nil, EffectNone},
		{"refund credits", operation(t, KindRefund, "25.00", nil), bet, EffectCredit},
		{"rollback of bet credits", operation(t, KindRollback, "25.00", nil), bet, EffectCredit},
		{"rollback of win debits", operation(t, KindRollback, "25.00", nil), win, EffectDebit},
		{"rollback of refund debits", operation(t, KindRollback, "25.00", nil), refund, EffectDebit},
	}
	for _, tc := range cases {
		if got := EffectOf(tc.op, tc.ref); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
	if InsufficientFundsCode(KindBet) != FailureInsufficientFunds ||
		InsufficientFundsCode(KindRollback) != FailureReversalInsufficientFunds {
		t.Error("insufficient funds codes must differ between bets and reversals")
	}
}
