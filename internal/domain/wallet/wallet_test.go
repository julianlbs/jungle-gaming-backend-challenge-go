package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
)

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	c, err := money.ParseCurrency("BRL")
	if err != nil {
		t.Fatal(err)
	}
	m, err := money.Parse(amount, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func usd(t *testing.T, amount string) money.Money {
	t.Helper()
	c, _ := money.ParseCurrency("USD")
	m, err := money.Parse(amount, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ids(t *testing.T) (ID, PlayerID) {
	t.Helper()
	id, err := NewID(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPlayerID(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return id, p
}

func open(t *testing.T, initial string) *Wallet {
	t.Helper()
	id, p := ids(t)
	w, _, err := Open(id, p, brl(t, initial), t0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestOpenWithPositiveBalance(t *testing.T) {
	id, p := ids(t)
	w, mv, err := Open(id, p, brl(t, "100.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version() != 1 || w.Balance().String() != "100.00" || w.Currency().String() != "BRL" {
		t.Fatalf("unexpected wallet %+v", w.Snapshot())
	}
	if mv == nil || mv.Direction != DirectionCredit || !mv.BalanceBefore.IsZero() ||
		mv.BalanceAfter.String() != "100.00" || mv.VersionAfter != 1 {
		t.Fatalf("unexpected opening movement %+v", mv)
	}
}

func TestOpenWithZeroBalanceHasNoMovement(t *testing.T) {
	id, p := ids(t)
	w, mv, err := Open(id, p, brl(t, "0.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if mv != nil || w.Version() != 1 || !w.Balance().IsZero() {
		t.Fatalf("zero opening must not produce a movement: %+v %+v", w.Snapshot(), mv)
	}
}

func TestOpenRejectsInvalidInput(t *testing.T) {
	id, p := ids(t)
	neg, _ := brl(t, "1.00").Neg()
	cases := map[string]func() error{
		"zero id":      func() error { _, _, err := Open(ID{}, p, brl(t, "1.00"), t0); return err },
		"zero player":  func() error { _, _, err := Open(id, PlayerID{}, brl(t, "1.00"), t0); return err },
		"negative":     func() error { _, _, err := Open(id, p, neg, t0); return err },
		"uninit money": func() error { _, _, err := Open(id, p, money.Money{}, t0); return err },
		"zero time":    func() error { _, _, err := Open(id, p, brl(t, "1.00"), time.Time{}); return err },
	}
	for name, fn := range cases {
		if err := fn(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDebitAndCredit(t *testing.T) {
	w := open(t, "100.00")

	mv, err := w.Debit(brl(t, "80.00"), t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if mv.BalanceBefore.String() != "100.00" || mv.BalanceAfter.String() != "20.00" ||
		mv.VersionBefore != 1 || mv.VersionAfter != 2 || w.Version() != 2 {
		t.Fatalf("unexpected debit %+v", mv)
	}

	if _, err := w.Debit(brl(t, "80.00"), t0); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("second debit = %v, want ErrInsufficientFunds", err)
	}
	if w.Balance().String() != "20.00" || w.Version() != 2 {
		t.Fatal("rejected debit must not change the wallet")
	}

	mv, err = w.Credit(brl(t, "5.50"), t0)
	if err != nil || mv.BalanceAfter.String() != "25.50" || w.Version() != 3 {
		t.Fatalf("credit = %+v, %v", mv, err)
	}

	if _, err := w.Debit(brl(t, "25.50"), t0); err != nil || !w.Balance().IsZero() {
		t.Fatalf("debit to zero = %v", err)
	}
}

func TestMovementValidation(t *testing.T) {
	w := open(t, "10.00")
	if _, err := w.Credit(usd(t, "1.00"), t0); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("credit usd = %v", err)
	}
	if _, err := w.Debit(usd(t, "1.00"), t0); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("debit usd = %v", err)
	}
	if _, err := w.Credit(brl(t, "0.00"), t0); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("credit zero = %v", err)
	}
	neg, _ := brl(t, "1.00").Neg()
	if _, err := w.Debit(neg, t0); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("debit negative = %v", err)
	}
	if _, err := w.Credit(money.Money{}, t0); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("credit uninitialized = %v", err)
	}
	if w.Version() != 1 || w.Balance().String() != "10.00" {
		t.Fatal("invalid movements must not change the wallet")
	}
}

func TestCreditOverflow(t *testing.T) {
	id, p := ids(t)
	c, _ := money.ParseCurrency("BRL")
	maxV, _ := money.Parse("92233720368547758.07", c)
	w, _, err := Open(id, p, maxV, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Credit(brl(t, "0.01"), t0); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("credit = %v, want ErrOverflow", err)
	}
}

func TestRehydrateDoesNotApplyMovements(t *testing.T) {
	id, p := ids(t)
	s := Snapshot{ID: id, PlayerID: p, Balance: brl(t, "20.00"), Version: 7, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)}
	w, err := Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if w.Snapshot() != s {
		t.Fatalf("rehydrated snapshot differs: %+v", w.Snapshot())
	}
}

func TestRehydrateRejectsInvalidSnapshots(t *testing.T) {
	id, p := ids(t)
	neg, _ := brl(t, "1.00").Neg()
	valid := Snapshot{ID: id, PlayerID: p, Balance: brl(t, "1.00"), Version: 1, CreatedAt: t0, UpdatedAt: t0}
	mutations := map[string]func(*Snapshot){
		"zero id":          func(s *Snapshot) { s.ID = ID{} },
		"zero player":      func(s *Snapshot) { s.PlayerID = PlayerID{} },
		"negative balance": func(s *Snapshot) { s.Balance = neg },
		"uninit balance":   func(s *Snapshot) { s.Balance = money.Money{} },
		"version zero":     func(s *Snapshot) { s.Version = 0 },
		"zero created":     func(s *Snapshot) { s.CreatedAt = time.Time{} },
		"updated first":    func(s *Snapshot) { s.UpdatedAt = t0.Add(-time.Second) },
	}
	for name, mutate := range mutations {
		s := valid
		mutate(&s)
		if _, err := Rehydrate(s); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseID(t *testing.T) {
	if _, err := ParseID("0192F291-27DD-7D3F-8071-5F8685DEEF37"); err != nil {
		t.Errorf("uppercase canonical id rejected: %v", err)
	}
	for _, in := range []string{"", "00000000-0000-0000-0000-000000000000", "0192f29127dd7d3f80715f8685deef37", "urn:uuid:0192f291-27dd-7d3f-8071-5f8685deef37", "{0192f291-27dd-7d3f-8071-5f8685deef37}"} {
		if _, err := ParseID(in); !errors.Is(err, ErrInvalidID) {
			t.Errorf("ParseID(%q) = %v, want ErrInvalidID", in, err)
		}
	}
}
