//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

func (c *cluster) transactionStatus(i *instance, id string) (string, string) {
	c.t.Helper()
	r := c.mustCall(i, http.MethodGet, "/wagering/transactions/"+id, "provider-a", nil, nil)
	if r.status != http.StatusOK {
		c.t.Fatalf("get transaction %s: %d %v", id, r.status, r.body)
	}
	code, _ := r.body["failureCode"].(string)
	return r.body["status"].(string), code
}

// assertBetRefunded checks that the bet was processed and that, besides the opening, the wallet
// moved by exactly one debit and one credit of the bet amount and is back at the opening balance.
func (c *cluster) assertBetRefunded(w walletRef, betExternalID string, amountMinor int64, opening string) {
	c.t.Helper()
	if n := c.count(`SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND external_transaction_id = $2
		AND kind = 'BET' AND status = 'PROCESSED'`, w.id, betExternalID); n != 1 {
		c.t.Fatalf("processed bets %s = %d", betExternalID, n)
	}
	movements := `SELECT count(*) FROM wallet_ledger_entries e JOIN wager_transactions t ON t.id = e.transaction_id
		WHERE e.wallet_id = $1 AND t.kind <> 'OPENING'`
	if n := c.count(movements, w.id); n != 2 {
		c.t.Fatalf("ledger movements besides the opening = %d, want 2", n)
	}
	for _, direction := range []string{"DEBIT", "CREDIT"} {
		if n := c.count(movements+` AND e.direction = $2 AND e.amount_minor = $3`, w.id, direction, amountMinor); n != 1 {
			c.t.Fatalf("%s entries of %d = %d, want 1", direction, amountMinor, n)
		}
	}
	if got := c.balance(w.id); got != opening {
		c.t.Fatalf("balance = %s, want %s", got, opening)
	}
}

func TestSameOperationOverHTTPAndSQSHasOneEffect(t *testing.T) {
	c := newCluster(t)
	apps := []*instance{c.start("app-1", nil), c.start("app-2", nil)}
	w := c.openWallet(apps[0], "100.00")

	r := c.mustCall(apps[0], http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": "provider-a:bet-1"}, wagerBody(w, "bet-1", "BET", "25.00", ""))
	if r.status != http.StatusOK {
		t.Fatalf("http bet: %d %v", r.status, r.body)
	}
	c.sendWager(w, "msg-1", "bet-1", "BET", "25.00", "")
	c.waitSettled(30 * time.Second)
	waitFor(t, 10*time.Second, "message recorded", func() bool {
		return c.count(`SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1'`) == 1
	})

	if n := c.count(`SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1' AND transaction_id = $1`, r.body["transactionId"]); n != 1 {
		t.Fatal("sqs delivery not linked to the http transaction")
	}
	if got := c.balance(w.id); got != "75.00" {
		t.Fatalf("balance = %s, want 75.00", got)
	}
	c.sendWager(w, "msg-2", "bet-1", "BET", "99.00", "")
	waitFor(t, 30*time.Second, "conflicting message dead-lettered", func() bool { return c.queueDepth(c.dlq) == 1 })
	if got := c.balance(w.id); got != "75.00" {
		t.Fatalf("conflicting payload changed balance to %s", got)
	}
	c.assertLedgerConsistency(apps[1])
}

func TestRefundBeforeBetIsProcessedOnceTheBetArrives(t *testing.T) {
	c := newCluster(t)
	apps := []*instance{c.start("app-1", nil), c.start("app-2", nil)}
	w := c.openWallet(apps[0], "100.00")

	r := c.mustCall(apps[0], http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": "provider-a:refund-1"}, wagerBody(w, "refund-1", "REFUND", "40.00", "bet-1"))
	if r.status != http.StatusAccepted || r.body["status"] != "PENDING_REFERENCE" || r.header.Get("Location") == "" {
		t.Fatalf("early refund: %d %v", r.status, r.body)
	}
	refund := r.body["transactionId"].(string)

	c.sendWager(w, "msg-bet", "bet-1", "BET", "40.00", "")
	waitFor(t, 30*time.Second, "refund processed", func() bool {
		status, _ := c.transactionStatus(apps[1], refund)
		return status == "PROCESSED"
	})
	c.assertBetRefunded(w, "bet-1", 4000, "100.00")
	c.assertLedgerConsistency(apps[1])
}

func TestRefundWithoutBetIsRejectedAfterTheDeadline(t *testing.T) {
	c := newCluster(t)
	app := c.start("app", map[string]string{"PENDING_REFERENCE_TTL": "2s"})
	w := c.openWallet(app, "100.00")

	c.sendWager(w, "msg-refund", "refund-1", "REFUND", "10.00", "bet-never")
	var refund string
	waitFor(t, 30*time.Second, "refund recorded", func() bool {
		r := c.mustCall(app, http.MethodGet, "/providers/provider-a/wagering/transactions/refund-1", "provider-a", nil, nil)
		refund, _ = r.body["id"].(string)
		return r.status == http.StatusOK
	})
	waitFor(t, 30*time.Second, "refund rejected", func() bool {
		status, _ := c.transactionStatus(app, refund)
		return status == "REJECTED"
	})
	if _, code := c.transactionStatus(app, refund); code != "REFERENCE_NOT_FOUND" {
		t.Fatalf("failure code = %s", code)
	}
	if got := c.balance(w.id); got != "100.00" {
		t.Fatalf("balance = %s, want 100.00", got)
	}
	c.assertLedgerConsistency(app)
}
