package wallet

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
)

func entryID(t *testing.T) EntryID {
	t.Helper()
	id, err := NewEntryID(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLedgerEntryFromMovements(t *testing.T) {
	w := open(t, "100.00")
	debit, err := w.Debit(brl(t, "80.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewLedgerEntry(entryID(t), w.ID(), uuid.New(), debit, t0)
	if err != nil {
		t.Fatal(err)
	}
	if e.Direction() != DirectionDebit || e.BalanceBefore().String() != "100.00" ||
		e.BalanceAfter().String() != "20.00" || e.WalletVersion() != 2 {
		t.Fatalf("unexpected entry %+v", e)
	}

	credit, err := w.Credit(brl(t, "80.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewLedgerEntry(entryID(t), w.ID(), uuid.New(), credit, t0); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerEntryRejectsInconsistentBalances(t *testing.T) {
	w := open(t, "100.00")
	valid := LedgerEntrySnapshot{
		ID:            entryID(t),
		WalletID:      w.ID(),
		TransactionID: uuid.New(),
		Direction:     DirectionCredit,
		Amount:        brl(t, "10.00"),
		BalanceBefore: brl(t, "100.00"),
		BalanceAfter:  brl(t, "110.00"),
		WalletVersion: 2,
		CreatedAt:     t0,
	}
	if _, err := RehydrateLedgerEntry(valid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	cases := map[string]struct {
		mutate func(*LedgerEntrySnapshot)
		want   error
	}{
		"credit arithmetic": {func(s *LedgerEntrySnapshot) { s.BalanceAfter = brl(t, "109.99") }, ErrInconsistentEntry},
		"debit arithmetic":  {func(s *LedgerEntrySnapshot) { s.Direction = DirectionDebit }, ErrInconsistentEntry},
		"unknown direction": {func(s *LedgerEntrySnapshot) { s.Direction = "SIDEWAYS" }, ErrInvalidDirection},
		"zero amount":       {func(s *LedgerEntrySnapshot) { s.Amount = brl(t, "0.00") }, ErrInvalidAmount},
		"currency mismatch": {func(s *LedgerEntrySnapshot) { s.Amount = usd(t, "10.00") }, money.ErrCurrencyMismatch},
		"missing tx":        {func(s *LedgerEntrySnapshot) { s.TransactionID = uuid.Nil }, ErrInvalidID},
		"missing wallet":    {func(s *LedgerEntrySnapshot) { s.WalletID = ID{} }, ErrInvalidID},
		"version zero":      {func(s *LedgerEntrySnapshot) { s.WalletVersion = 0 }, ErrInvalidLedgerEntry},
		"uninitialized":     {func(s *LedgerEntrySnapshot) { s.BalanceBefore = money.Money{} }, money.ErrUninitialized},
		"negative before": {func(s *LedgerEntrySnapshot) {
			s.BalanceBefore, _ = brl(t, "5.00").Neg()
			s.BalanceAfter = brl(t, "5.00")
		}, ErrInvalidLedgerEntry},
	}
	for name, tc := range cases {
		s := valid
		tc.mutate(&s)
		if _, err := RehydrateLedgerEntry(s); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}
