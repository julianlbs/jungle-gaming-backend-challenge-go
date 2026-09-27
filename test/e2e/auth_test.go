//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

func TestKeycloakRejectsExpiredAndForeignCallers(t *testing.T) {
	c := newCluster(t)
	app := c.start("app", nil)
	w := c.openWallet(app, "100.00")
	c.tokens["provider-a-short-lived"] = fetchToken(t, "provider-a-short-lived")
	c.tokens["unrelated-service"] = fetchToken(t, "unrelated-service")

	wagers := `SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'`
	ledger := `SELECT count(*) FROM wallet_ledger_entries`
	wagersBefore, ledgerBefore := c.count(wagers), c.count(ledger)

	// The short-lived client issues tokens valid for two seconds.
	time.Sleep(3 * time.Second)
	cases := []struct {
		name, client string
		status       int
	}{
		{"expired short-lived token", "provider-a-short-lived", http.StatusUnauthorized},
		{"token for another audience", "unrelated-service", http.StatusUnauthorized},
		{"backoffice without wagering scope", "wallet-backoffice", http.StatusForbidden},
	}
	for _, tc := range cases {
		ext := "bet-" + tc.client
		r := c.mustCall(app, http.MethodPost, "/wagering/transactions", tc.client,
			map[string]string{"Idempotency-Key": "provider-a:" + ext}, wagerBody(w, ext, "BET", "10.00", ""))
		if r.status != tc.status {
			t.Errorf("%s: status = %d, want %d; body %v", tc.name, r.status, tc.status, r.body)
		}
	}

	if n := c.count(wagers); n != wagersBefore {
		t.Fatalf("wager transactions %d -> %d", wagersBefore, n)
	}
	if n := c.count(ledger); n != ledgerBefore {
		t.Fatalf("ledger entries %d -> %d", ledgerBefore, n)
	}
	if got := c.balance(w.id); got != "100.00" {
		t.Fatalf("balance = %s", got)
	}
}
