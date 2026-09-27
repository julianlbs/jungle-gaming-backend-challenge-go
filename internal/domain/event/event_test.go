package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

var t0 = time.Date(2026, 9, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	c, _ := money.ParseCurrency("BRL")
	m, err := money.Parse(amount, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func meta() Meta {
	return Meta{EventID: uuid.New(), CorrelationID: "corr-1", OccurredAt: t0}
}

func openedWallet(t *testing.T) (*wallet.Wallet, *wallet.Movement, *wagering.Transaction) {
	t.Helper()
	wid, _ := wallet.NewID(uuid.New())
	pid, _ := wallet.NewPlayerID(uuid.New())
	w, mv, err := wallet.Open(wid, pid, brl(t, "1000.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := wagering.NewID(uuid.New())
	tx, err := wagering.NewOpening(txID, wid, pid, brl(t, "1000.00"), "corr-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkProcessed(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, nil, t0); err != nil {
		t.Fatal(err)
	}
	return w, mv, tx
}

func decode(t *testing.T, o Outgoing) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(o.Payload, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestOpeningEvents(t *testing.T) {
	w, mv, tx := openedWallet(t)

	processed, err := NewWagerTransactionProcessed(meta(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if processed.EventType != TypeWagerTransactionProcessed || processed.Version != 1 {
		t.Fatalf("type/version not set by constructor: %+v", processed)
	}
	if processed.Data.ProviderID != "" || processed.Data.ExternalTransactionID != "" || processed.Data.Kind != "OPENING" {
		t.Fatalf("internal event must omit provider metadata: %+v", processed.Data)
	}
	out, err := processed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.Payload), "providerId") || strings.Contains(string(out.Payload), "roundId") {
		t.Fatalf("payload leaks inapplicable fields: %s", out.Payload)
	}
	if out.PartitionKey != w.ID().String() {
		t.Fatal("wager events must be partitioned by wallet")
	}

	changed, err := NewWalletBalanceChanged(meta(), w.ID(), tx.ID(), *mv)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := changed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, enc)
	data := doc["data"].(map[string]any)
	for _, field := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, ok := data[field]; !ok {
			t.Errorf("WalletBalanceChanged missing %s", field)
		}
	}
	if data["balanceBefore"].(map[string]any)["amount"] != "0.00" || data["balanceAfter"].(map[string]any)["amount"] != "1000.00" {
		t.Fatalf("unexpected balances: %v", data)
	}
	if data["walletVersion"].(float64) != 1 {
		t.Fatalf("opening walletVersion = %v", data["walletVersion"])
	}
}

func TestEnvelopeFormat(t *testing.T) {
	_, _, tx := openedWallet(t)
	m := meta()
	m.CausationID = "msg-123"
	env, err := NewWagerTransactionProcessed(m, tx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	for _, field := range []string{"eventId", "eventType", "aggregateId", "correlationId", "causationId", "occurredAt", "version", "data"} {
		if _, ok := doc[field]; !ok {
			t.Errorf("envelope missing %s", field)
		}
	}
	if doc["occurredAt"] != "2026-09-08T12:00:00Z" {
		t.Fatalf("occurredAt must be UTC RFC 3339, got %v", doc["occurredAt"])
	}
	if doc["eventId"] != m.EventID.String() || out.EventID != m.EventID {
		t.Fatal("event id must be stable")
	}
	amount := doc["data"].(map[string]any)["money"].(map[string]any)["amount"]
	if _, isString := amount.(string); !isString {
		t.Fatalf("money must be serialized as string, got %T", amount)
	}

	env.CausationID = nil
	out, _ = env.Encode()
	if strings.Contains(string(out.Payload), "causationId") {
		t.Fatal("causationId must be omitted when absent")
	}
}

func TestConstructorsRequireMatchingStatus(t *testing.T) {
	_, _, tx := openedWallet(t)
	if _, err := NewWagerTransactionRejected(meta(), tx); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("rejected from processed = %v", err)
	}
	if _, err := NewWagerTransactionPendingReference(meta(), tx); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("pending from processed = %v", err)
	}
	if _, err := NewWagerTransactionProcessed(Meta{}, tx); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("missing meta = %v", err)
	}
}

func TestRejectedAndPendingEvents(t *testing.T) {
	wid, _ := wallet.NewID(uuid.New())
	pid, _ := wallet.NewPlayerID(uuid.New())
	newTx := func(kind wagering.Kind, ref string) *wagering.Transaction {
		id, _ := wagering.NewID(uuid.New())
		tx, err := wagering.NewExternal(wagering.ExternalParams{
			ID: id, Channel: wagering.ChannelSQS, WalletID: wid, PlayerID: pid, Kind: kind,
			Amount: brl(t, "80.00"), ProviderID: "provider-a", ExternalID: "tx-" + id.String()[:8],
			IdempotencyKey: "k-" + id.String(), RoundID: "r1", GameID: "g1",
			ReferenceExternalID: ref, CorrelationID: "corr", Now: t0,
		})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}

	bet := newTx(wagering.KindBet, "")
	if err := bet.Reject(wagering.FailureInsufficientFunds, wagering.Result{Balance: brl(t, "20.00"), WalletVersion: 2}, nil, t0); err != nil {
		t.Fatal(err)
	}
	rejected, err := NewWagerTransactionRejected(meta(), bet)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.EventType != TypeWagerTransactionRejected || rejected.Data.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Data.Balance.Amount != "20.00" {
		t.Fatalf("unexpected rejected event %+v", rejected)
	}

	refund := newTx(wagering.KindRefund, "bet-1")
	if err := refund.AwaitReference(t0.Add(time.Second), t0.Add(time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	pending, err := NewWagerTransactionPendingReference(meta(), refund)
	if err != nil {
		t.Fatal(err)
	}
	if pending.EventType != TypeWagerTransactionPendingReference || pending.Data.ReferenceExternalTransactionID != "bet-1" ||
		pending.Data.NextAttemptAt != "2026-09-08T12:00:01Z" {
		t.Fatalf("unexpected pending event %+v", pending)
	}
}
