//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

type step struct {
	kind, amount, id, ref string
	status                wagering.Status
	code                  wagering.FailureCode
	balance               string
}

func runSteps(t *testing.T, f *appFixture, initial string, steps []step) {
	t.Helper()
	ctx := context.Background()
	w := f.openWallet(t, initial)
	for _, s := range steps {
		cmd := wager(w, s.kind, s.amount, s.id)
		cmd.ReferenceExternalTransactionID = s.ref
		out, err := f.processor.Process(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %v", s.id, err)
		}
		if out.Transaction.Status() != s.status || out.Transaction.FailureCode() != s.code {
			t.Fatalf("%s: got %s %s, want %s %s", s.id, out.Transaction.Status(), out.Transaction.FailureCode(), s.status, s.code)
		}
		if got := f.balance(t, w); got != s.balance {
			t.Fatalf("%s: balance %s, want %s", s.id, got, s.balance)
		}
	}
	rec, err := app.NewQueries(postgresReadModel(f), app.SystemClock{}).Reconcile(ctx, w.ID().String())
	if err != nil || !rec.Consistent {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
}

const (
	processed = wagering.StatusProcessed
	rejected  = wagering.StatusRejected
)

func TestReversalCombinations(t *testing.T) {
	cases := map[string][]step{
		"refund returns the bet": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"REFUND", "30.00", "r", "b", processed, "", "100.00"},
		},
		"rollback of bet credits": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"ROLLBACK", "30.00", "rb", "b", processed, "", "100.00"},
		},
		"rollback of win debits": {
			{"BET", "10.00", "b", "", processed, "", "90.00"},
			{"WIN", "50.00", "w", "b", processed, "", "140.00"},
			{"ROLLBACK", "50.00", "rw", "w", processed, "", "90.00"},
		},
		"rollback of win without funds": {
			{"BET", "10.00", "b", "", processed, "", "90.00"},
			{"WIN", "50.00", "w", "b", processed, "", "140.00"},
			{"BET", "120.00", "b2", "", processed, "", "20.00"},
			{"ROLLBACK", "50.00", "rw", "w", rejected, wagering.FailureReversalInsufficientFunds, "20.00"},
		},
		"refund then rollback of the same bet": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"REFUND", "30.00", "r", "b", processed, "", "100.00"},
			{"ROLLBACK", "30.00", "rb", "b", rejected, wagering.FailureReferenceAlreadyReversed, "100.00"},
		},
		"rollback then refund of the same bet": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"ROLLBACK", "30.00", "rb", "b", processed, "", "100.00"},
			{"REFUND", "30.00", "r", "b", rejected, wagering.FailureReferenceAlreadyReversed, "100.00"},
		},
		"rollback of refund restores the bet": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"REFUND", "30.00", "r", "b", processed, "", "100.00"},
			{"ROLLBACK", "30.00", "rr", "r", processed, "", "70.00"},
			{"REFUND", "30.00", "r2", "b", rejected, wagering.FailureReferenceAlreadyReversed, "70.00"},
		},
		"reversal amount must match": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"REFUND", "20.00", "r", "b", rejected, wagering.FailureReferenceAmountMismatch, "70.00"},
		},
		"rollback of rollback is not allowed": {
			{"BET", "30.00", "b", "", processed, "", "70.00"},
			{"ROLLBACK", "30.00", "rb", "b", processed, "", "100.00"},
			{"ROLLBACK", "30.00", "rrb", "rb", rejected, wagering.FailureReferenceKindNotAllowed, "100.00"},
		},
		"refund of a win is not allowed": {
			{"BET", "10.00", "b", "", processed, "", "90.00"},
			{"WIN", "20.00", "w", "b", processed, "", "110.00"},
			{"REFUND", "20.00", "r", "w", rejected, wagering.FailureReferenceKindNotAllowed, "110.00"},
		},
		"reversal of a rejected bet": {
			{"BET", "500.00", "b", "", rejected, wagering.FailureInsufficientFunds, "100.00"},
			{"REFUND", "500.00", "r", "b", rejected, wagering.FailureReferenceNotProcessed, "100.00"},
		},
		"loss keeps the balance": {
			{"BET", "10.00", "b", "", processed, "", "90.00"},
			{"LOSS", "0", "l", "", processed, "", "90.00"},
		},
		"several wins may cite the same bet": {
			{"BET", "10.00", "b", "", processed, "", "90.00"},
			{"WIN", "5.00", "w1", "b", processed, "", "95.00"},
			{"WIN", "7.00", "w2", "b", processed, "", "102.00"},
		},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			runSteps(t, newAppFixture(t), "100.00", steps)
		})
	}
}

func TestSameOperationConcurrentlyHasOneEffect(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")
	cmd := wager(w, "BET", "10.00", "dup")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		fresh   int
		replays int
	)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := cmd
			if i%2 == 1 {
				c.Channel = wagering.ChannelSQS
			}
			out, err := f.processor.Process(ctx, c)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if out.Replay {
				replays++
			} else {
				fresh++
			}
		}()
	}
	wg.Wait()
	if fresh != 1 || replays != 7 {
		t.Fatalf("fresh=%d replays=%d", fresh, replays)
	}
	if got := f.balance(t, w); got != "90.00" {
		t.Fatalf("balance = %s", got)
	}
}

func TestConcurrentReversalOfOneBetHasOneCredit(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")
	bet, err := f.processor.Process(ctx, wager(w, "BET", "40.00", "bet"))
	requireOutcome(t, bet, err, processed, "60.00", false)

	const reversals = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[wagering.Status]int{}
		codes    = map[wagering.FailureCode]int{}
	)
	for i := range reversals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			kind := "REFUND"
			if i%2 == 1 {
				kind = "ROLLBACK"
			}
			cmd := wager(w, kind, "40.00", fmt.Sprintf("reversal-%d", i))
			cmd.ReferenceExternalTransactionID = "bet"
			out, err := f.processor.Process(ctx, cmd)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			statuses[out.Transaction.Status()]++
			codes[out.Transaction.FailureCode()]++
		}()
	}
	wg.Wait()
	if statuses[processed] != 1 || statuses[rejected] != reversals-1 || codes[wagering.FailureReferenceAlreadyReversed] != reversals-1 {
		t.Fatalf("statuses = %v codes = %v", statuses, codes)
	}
	if got := f.balance(t, w); got != "100.00" {
		t.Fatalf("balance = %s", got)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM wallet_ledger_entries e JOIN wager_transactions t ON t.id = e.transaction_id
		WHERE e.wallet_id = $1 AND t.kind IN ('REFUND', 'ROLLBACK') AND e.direction = 'CREDIT' AND e.amount_minor = 4000`, w.ID().UUID()); n != 1 {
		t.Fatalf("reversal credits = %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID().UUID()); n != 3 {
		t.Fatalf("ledger entries = %d, want opening, bet and one reversal", n)
	}
	rec, err := app.NewQueries(postgresReadModel(f), app.SystemClock{}).Reconcile(ctx, w.ID().String())
	if err != nil || !rec.Consistent {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
}

func TestDistinctWalletsInParallel(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 6 {
		w := f.openWallet(t, "50.00")
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 5 {
				if _, err := f.processor.Process(ctx, wager(w, "BET", "3.00", fmt.Sprintf("w%d-%d", i, j))); err != nil {
					t.Error(err)
				}
			}
			if got := f.balance(t, w); got != "35.00" {
				t.Errorf("wallet %d balance = %s", i, got)
			}
		}()
	}
	wg.Wait()
}
