//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func (c *cluster) threeInstances() []*instance {
	return []*instance{c.start("app-1", nil), c.start("app-2", nil), c.start("app-3", nil)}
}

// fanOut runs n requests concurrently, spread round-robin over the instances.
func fanOut(t *testing.T, apps []*instance, n int, do func(i int, app *instance) (response, error)) []response {
	t.Helper()
	out := make([]response, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = do(i, apps[i%len(apps)])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	return out
}

func TestSameBetAcrossInstancesHasOneEffect(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	w := c.openWallet(apps[0], "100.00")

	results := fanOut(t, apps, 50, func(_ int, app *instance) (response, error) {
		return c.wager(app, w, "bet-1", "BET", "30.00", "")
	})
	var txID any
	for i, r := range results {
		if r.status != http.StatusOK || r.body["status"] != "PROCESSED" {
			t.Fatalf("request %d: %d %v", i, r.status, r.body)
		}
		if i == 0 {
			txID = r.body["id"]
		} else if r.body["id"] != txID {
			t.Fatalf("request %d returned transaction %v, want %v", i, r.body["id"], txID)
		}
	}
	if got := c.balance(w.id); got != "70.00" {
		t.Fatalf("balance = %s, want 70.00", got)
	}
	if n := c.count(`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.id); n != 2 {
		t.Fatalf("ledger entries = %d, want opening plus one bet", n)
	}
	c.assertLedgerConsistency(apps[1])
}

func TestCompetingBetsAcrossInstancesNeverOverdraw(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	w := c.openWallet(apps[0], "100.00")

	amounts := []string{"80.00", "80.00", "100.00"}
	results := fanOut(t, apps, len(amounts), func(i int, app *instance) (response, error) {
		return c.wager(app, w, fmt.Sprintf("bet-%d", i), "BET", amounts[i], "")
	})
	processed, rejected := 0, 0
	var spent int
	for i, r := range results {
		switch r.body["status"] {
		case "PROCESSED":
			processed++
			if amounts[i] == "80.00" {
				spent += 80
			} else {
				spent += 100
			}
		case "REJECTED":
			rejected++
			if r.status != http.StatusUnprocessableEntity {
				t.Fatalf("rejected with status %d", r.status)
			}
		default:
			t.Fatalf("request %d: %d %v", i, r.status, r.body)
		}
	}
	if processed != 1 || rejected != 2 {
		t.Fatalf("processed=%d rejected=%d, want exactly one processed", processed, rejected)
	}
	if got, want := c.balance(w.id), fmt.Sprintf("%d.00", 100-spent); got != want {
		t.Fatalf("balance = %s, want %s", got, want)
	}
	c.assertLedgerConsistency(apps[2])
}

func TestDistinctWalletsProgressInParallel(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	const wallets, betsPerWallet = 20, 5
	refs := make([]walletRef, wallets)
	for i := range refs {
		refs[i] = c.openWallet(apps[i%len(apps)], "50.00")
	}

	results := fanOut(t, apps, wallets*betsPerWallet, func(i int, app *instance) (response, error) {
		return c.wager(app, refs[i%wallets], fmt.Sprintf("bet-%d", i), "BET", "10.00", "")
	})
	for i, r := range results {
		if r.status != http.StatusOK || r.body["status"] != "PROCESSED" {
			t.Fatalf("request %d: %d %v", i, r.status, r.body)
		}
	}
	for _, w := range refs {
		if got := c.balance(w.id); got != "0.00" {
			t.Fatalf("wallet %s balance = %s, want 0.00", w.id, got)
		}
	}
	c.assertLedgerConsistency(apps[0])
}
