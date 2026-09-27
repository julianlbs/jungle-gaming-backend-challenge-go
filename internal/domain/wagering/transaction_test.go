package wagering

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	c, _ := money.ParseCurrency("BRL")
	m, err := money.Parse(amount, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newID(t *testing.T) ID {
	t.Helper()
	id, err := NewID(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func params(t *testing.T, kind Kind, amount string) ExternalParams {
	t.Helper()
	w, _ := wallet.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	p, _ := wallet.ParsePlayerID("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	ref := ""
	if kind.RequiresReference() {
		ref = "transaction-100"
	}
	return ExternalParams{
		ID:                  newID(t),
		Channel:             ChannelHTTP,
		WalletID:            w,
		PlayerID:            p,
		Kind:                kind,
		Amount:              brl(t, amount),
		ProviderID:          "provider-a",
		ExternalID:          "transaction-123",
		IdempotencyKey:      "provider-a:transaction-123",
		RoundID:             "round-987",
		GameID:              "fortune-chimp",
		ReferenceExternalID: ref,
		CorrelationID:       "corr-1",
		Now:                 t0,
	}
}

func mustExternal(t *testing.T, p ExternalParams) *Transaction {
	t.Helper()
	tx, err := NewExternal(p)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func result(t *testing.T, amount string, version int64) Result {
	return Result{Balance: brl(t, amount), WalletVersion: version}
}

func TestNewExternalStartsPending(t *testing.T) {
	tx := mustExternal(t, params(t, KindBet, "25.00"))
	if tx.Status() != StatusPending || tx.Origin() != OriginExternal || tx.Kind() != KindBet {
		t.Fatalf("unexpected transaction %+v", tx.Snapshot())
	}
	ext, ok := tx.External()
	if !ok || ext.PayloadHash != "sha256:629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344" {
		t.Fatalf("unexpected external metadata %+v", ext)
	}
}

func TestPayloadNormalizationBeforeHash(t *testing.T) {
	a := params(t, KindBet, "25.5")
	b := params(t, KindBet, "25.50")
	b.WalletID, _ = wallet.ParseID("0192F291-27DD-7D3F-8071-5F8685DEEF37")
	b.IdempotencyKey = "another-key"
	b.Channel = ChannelSQS
	b.CorrelationID = "corr-2"
	ea, _ := mustExternal(t, a).External()
	eb, _ := mustExternal(t, b).External()
	if ea.PayloadHash != eb.PayloadHash {
		t.Fatal("equivalent payloads must hash equally regardless of transport metadata")
	}
}

func TestPayloadConflictForSameKey(t *testing.T) {
	original := mustExternal(t, params(t, KindBet, "25.00"))
	changed := params(t, KindBet, "30.00")
	other, _ := mustExternal(t, changed).External()
	if original.SamePayload(other.PayloadHash) {
		t.Fatal("different amount under the same key must be detected as a conflict")
	}
	same, _ := mustExternal(t, params(t, KindBet, "25.00")).External()
	if !original.SamePayload(same.PayloadHash) {
		t.Fatal("identical payload must be recognized as a replay")
	}
}

func TestZeroAmountPolicyPerKind(t *testing.T) {
	cases := []struct {
		kind    Kind
		amount  string
		wantErr bool
	}{
		{KindBet, "0.00", true},
		{KindBet, "0.01", false},
		{KindWin, "0.00", true},
		{KindWin, "10.00", false},
		{KindLoss, "0.00", false},
		{KindLoss, "0.01", true},
		{KindRefund, "0.00", true},
		{KindRefund, "25.00", false},
		{KindRollback, "0.00", true},
		{KindRollback, "25.00", false},
	}
	for _, tc := range cases {
		_, err := NewExternal(params(t, tc.kind, tc.amount))
		if tc.wantErr != (err != nil) {
			t.Errorf("%s %s: err = %v, wantErr %v", tc.kind, tc.amount, err, tc.wantErr)
		}
		if err != nil && !errors.Is(err, ErrInvalidField) {
			t.Errorf("%s %s: err = %v, want ErrInvalidField", tc.kind, tc.amount, err)
		}
	}
}

func TestNewExternalValidation(t *testing.T) {
	cases := map[string]struct {
		mutate func(*ExternalParams)
		field  string
	}{
		"opening":             {func(p *ExternalParams) { p.Kind = KindOpening }, ""},
		"unknown kind":        {func(p *ExternalParams) { p.Kind = "DEPOSIT" }, "kind"},
		"provider uppercase":  {func(p *ExternalParams) { p.ProviderID = "Provider-A" }, "providerId"},
		"provider empty":      {func(p *ExternalParams) { p.ProviderID = "" }, "providerId"},
		"external empty":      {func(p *ExternalParams) { p.ExternalID = "" }, "externalTransactionId"},
		"external spaces":     {func(p *ExternalParams) { p.ExternalID = "tx 1" }, "externalTransactionId"},
		"key empty":           {func(p *ExternalParams) { p.IdempotencyKey = "" }, "idempotencyKey"},
		"round empty":         {func(p *ExternalParams) { p.RoundID = "" }, "roundId"},
		"game empty":          {func(p *ExternalParams) { p.GameID = "" }, "gameId"},
		"wallet missing":      {func(p *ExternalParams) { p.WalletID = wallet.ID{} }, "walletId"},
		"player missing":      {func(p *ExternalParams) { p.PlayerID = wallet.PlayerID{} }, "playerId"},
		"money missing":       {func(p *ExternalParams) { p.Amount = money.Money{} }, "money"},
		"bet with reference":  {func(p *ExternalParams) { p.ReferenceExternalID = "x" }, "referenceExternalTransactionId"},
		"channel internal":    {func(p *ExternalParams) { p.Channel = ChannelInternal }, "channel"},
		"negative amount":     {func(p *ExternalParams) { p.Amount, _ = brl(t, "1.00").Neg() }, "money.amount"},
		"oversized external":  {func(p *ExternalParams) { p.ExternalID = string(make([]byte, 129)) }, "externalTransactionId"},
		"non ascii round":     {func(p *ExternalParams) { p.RoundID = "rodada-é" }, "roundId"},
		"missing correlation": {func(p *ExternalParams) { p.CorrelationID = "" }, ""},
		"missing timestamp":   {func(p *ExternalParams) { p.Now = time.Time{} }, ""},
		"missing internal id": {func(p *ExternalParams) { p.ID = ID{} }, ""},
	}
	for name, tc := range cases {
		p := params(t, KindBet, "25.00")
		tc.mutate(&p)
		_, err := NewExternal(p)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		var fe *FieldError
		if tc.field != "" && (!errors.As(err, &fe) || fe.Field != tc.field) {
			t.Errorf("%s: err = %v, want field %q", name, err, tc.field)
		}
	}

	p := params(t, KindBet, "25.00")
	p.Kind = KindOpening
	if _, err := NewExternal(p); !errors.Is(err, ErrReservedKind) {
		t.Errorf("OPENING = %v, want ErrReservedKind", err)
	}
}

func TestReferenceRules(t *testing.T) {
	refund := params(t, KindRefund, "25.00")
	refund.ReferenceExternalID = ""
	if _, err := NewExternal(refund); !errors.Is(err, ErrInvalidField) {
		t.Errorf("refund without reference = %v", err)
	}
	self := params(t, KindRollback, "25.00")
	self.ReferenceExternalID = self.ExternalID
	if _, err := NewExternal(self); !errors.Is(err, ErrInvalidField) {
		t.Errorf("self reference = %v", err)
	}
	win := params(t, KindWin, "10.00")
	if _, err := NewExternal(win); err != nil {
		t.Errorf("win without reference: %v", err)
	}
	win.ReferenceExternalID = "bet-1"
	if _, err := NewExternal(win); err != nil {
		t.Errorf("win with reference: %v", err)
	}
	loss := params(t, KindLoss, "0.00")
	loss.ReferenceExternalID = "bet-1"
	if _, err := NewExternal(loss); !errors.Is(err, ErrInvalidField) {
		t.Errorf("loss with reference = %v", err)
	}
}

func TestProcessedIsTerminal(t *testing.T) {
	tx := mustExternal(t, params(t, KindBet, "25.00"))
	if err := tx.MarkProcessed(result(t, "975.00", 2), nil, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != StatusProcessed || tx.CompletedAt().IsZero() {
		t.Fatalf("unexpected state %+v", tx.Snapshot())
	}
	r, ok := tx.Result()
	if !ok || r.Balance.String() != "975.00" || r.WalletVersion != 2 {
		t.Fatalf("unexpected result %+v", r)
	}

	attempts := []func() error{
		func() error { return tx.MarkProcessed(result(t, "1.00", 3), nil, t0) },
		func() error { return tx.Reject(FailureInsufficientFunds, result(t, "1.00", 3), nil, t0) },
		func() error { return tx.AwaitReference(t0, t0, t0) },
		func() error { return tx.Resume(t0) },
		func() error { return tx.Fail("boom", t0) },
		func() error { return tx.Reschedule(t0, t0) },
	}
	for i, attempt := range attempts {
		var te *TransitionError
		if err := attempt(); !errors.As(err, &te) && !errors.Is(err, ErrInvalidTransaction) {
			t.Errorf("attempt %d on terminal transaction = %v", i, err)
		}
	}
	if tx.Status() != StatusProcessed {
		t.Fatal("terminal status changed")
	}
}

func TestTransitionMatrix(t *testing.T) {
	type step func(*Transaction) error
	steps := map[string]step{
		"process":  func(tx *Transaction) error { return tx.MarkProcessed(result(t, "1.00", 2), refPtr(t), t0) },
		"reject":   func(tx *Transaction) error { return tx.Reject(FailureReferenceMismatch, result(t, "1.00", 1), nil, t0) },
		"await":    func(tx *Transaction) error { return tx.AwaitReference(t0.Add(time.Second), t0.Add(time.Hour), t0) },
		"resume":   func(tx *Transaction) error { return tx.Resume(t0) },
		"fail":     func(tx *Transaction) error { return tx.Fail("permanent", t0) },
		"schedule": func(tx *Transaction) error { return tx.Reschedule(t0.Add(time.Minute), t0) },
	}
	allowed := map[Status]map[string]Status{
		StatusPending: {
			"process": StatusProcessed, "reject": StatusRejected, "await": StatusPendingReference, "fail": StatusFailed,
		},
		StatusPendingReference: {
			"resume": StatusPending, "reject": StatusRejected, "fail": StatusFailed, "schedule": StatusPendingReference,
		},
	}
	setups := map[Status]func() *Transaction{
		StatusPending: func() *Transaction { return mustExternal(t, params(t, KindRefund, "25.00")) },
		StatusPendingReference: func() *Transaction {
			tx := mustExternal(t, params(t, KindRefund, "25.00"))
			if err := tx.AwaitReference(t0.Add(time.Second), t0.Add(time.Hour), t0); err != nil {
				t.Fatal(err)
			}
			return tx
		},
	}
	for from, setup := range setups {
		for name, run := range steps {
			tx := setup()
			err := run(tx)
			want, ok := allowed[from][name]
			if ok && (err != nil || tx.Status() != want) {
				t.Errorf("%s --%s--> got %s, %v; want %s", from, name, tx.Status(), err, want)
			}
			if !ok && err == nil {
				t.Errorf("%s --%s--> must be rejected", from, name)
			}
		}
	}
}

func refPtr(t *testing.T) *ID {
	id := newID(t)
	return &id
}

func TestReversalRequiresResolvedReference(t *testing.T) {
	tx := mustExternal(t, params(t, KindRefund, "25.00"))
	if err := tx.MarkProcessed(result(t, "25.00", 2), nil, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("processing a refund without reference = %v", err)
	}
	if tx.Status() != StatusPending {
		t.Fatal("status must not change")
	}
}

func TestRejectValidation(t *testing.T) {
	tx := mustExternal(t, params(t, KindBet, "80.00"))
	if err := tx.Reject(FailureProcessingFailed, result(t, "20.00", 2), nil, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("reject with failure code = %v", err)
	}
	if err := tx.Reject(FailureInsufficientFunds, Result{}, nil, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("reject without observed balance = %v", err)
	}
	if err := tx.Reject(FailureInsufficientFunds, result(t, "20.00", 2), nil, t0); err != nil {
		t.Fatal(err)
	}
	if tx.FailureCode() != FailureInsufficientFunds {
		t.Fatalf("failure code = %s", tx.FailureCode())
	}
}

func TestReferenceWaitBudget(t *testing.T) {
	tx := mustExternal(t, params(t, KindRefund, "25.00"))
	if err := tx.AwaitReference(t0.Add(time.Second), t0.Add(time.Minute), t0); err != nil {
		t.Fatal(err)
	}
	if tx.ReferenceWaitExhausted(t0.Add(30*time.Second), 3) {
		t.Error("budget must not be exhausted yet")
	}
	if !tx.ReferenceWaitExhausted(t0.Add(time.Minute), 3) {
		t.Error("deadline reached must exhaust the budget")
	}

	s := tx.Snapshot()
	s.Attempts = 3
	rehydrated, err := Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if !rehydrated.ReferenceWaitExhausted(t0, 3) {
		t.Error("max attempts must exhaust the budget")
	}

	bet := mustExternal(t, params(t, KindBet, "25.00"))
	if err := bet.AwaitReference(t0, t0.Add(time.Minute), t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("bet without reference cannot wait: %v", err)
	}
}

func TestOpening(t *testing.T) {
	w, _ := wallet.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	p, _ := wallet.ParsePlayerID("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	tx, err := NewOpening(newID(t), w, p, brl(t, "1000.00"), "corr-open", t0)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Origin() != OriginInternal || tx.Kind() != KindOpening || tx.Channel() != ChannelInternal {
		t.Fatalf("unexpected opening %+v", tx.Snapshot())
	}
	if _, ok := tx.External(); ok {
		t.Fatal("opening must not carry external metadata")
	}
	if err := tx.MarkProcessed(result(t, "1000.00", 1), nil, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOpening(newID(t), w, p, brl(t, "0.00"), "corr", t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("zero opening = %v", err)
	}
	if _, err := NewOpening(newID(t), wallet.ID{}, p, brl(t, "1.00"), "corr", t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("opening without wallet = %v", err)
	}
	if _, err := Rehydrate(tx.Snapshot()); err != nil {
		t.Fatalf("opening round trip: %v", err)
	}
}

func TestRehydrateRoundTripAndValidation(t *testing.T) {
	tx := mustExternal(t, params(t, KindRollback, "25.00"))
	if err := tx.MarkProcessed(result(t, "100.00", 3), refPtr(t), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	back, err := Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != StatusProcessed || back.UpdatedAt() != tx.UpdatedAt() {
		t.Fatal("rehydration must not alter state")
	}

	broken := map[string]func(*Snapshot){
		"processed without result": func(s *Snapshot) { s.Result = nil },
		"processed with failure":   func(s *Snapshot) { s.FailureCode = FailureInsufficientFunds },
		"external without meta":    func(s *Snapshot) { s.External = nil },
		"internal kind external":   func(s *Snapshot) { s.Origin = OriginInternal },
		"opening external":         func(s *Snapshot) { s.Kind = KindOpening },
		"loss with amount":         func(s *Snapshot) { s.Kind = KindLoss },
		"reversal without ref id":  func(s *Snapshot) { s.ReferenceID = nil },
		"rejected with failed":     func(s *Snapshot) { s.Status = StatusRejected; s.FailureCode = FailureProcessingFailed },
		"unknown status":           func(s *Snapshot) { s.Status = "DONE" },
		"negative attempts":        func(s *Snapshot) { s.Attempts = -1 },
	}
	for name, mutate := range broken {
		c := tx.Snapshot()
		mutate(&c)
		if _, err := Rehydrate(c); !errors.Is(err, ErrInvalidTransaction) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
