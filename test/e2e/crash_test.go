//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

func (c *cluster) queueDepth(url string) int {
	c.t.Helper()
	out := must(c.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	}))
	var total, n int
	for _, v := range out.Attributes {
		_, _ = fmt.Sscan(v, &n)
		total += n
	}
	return total
}

func (c *cluster) unpublished() int {
	return c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`)
}

func (c *cluster) waitSettled(timeout time.Duration) {
	c.t.Helper()
	waitFor(c.t, timeout, "queue drained and outbox published", func() bool {
		return c.queueDepth(c.queue) == 0 && c.unpublished() == 0 &&
			c.count(`SELECT count(*) FROM wager_transactions WHERE status IN ('PENDING', 'PENDING_REFERENCE')`) == 0
	})
}

func assertKilled(t *testing.T, i *instance) {
	t.Helper()
	if s := i.WaitExit(t, 30*time.Second); !killedBySignal(s) {
		t.Fatalf("%s exited with %v, want the injected SIGKILL", i.name, s)
	}
}

func TestConsumerCrashAfterCommitIsRedeliveredWithoutDoubleEffect(t *testing.T) {
	c := newCluster(t)
	api := c.start("api", map[string]string{"APP_ROLES": "api,outbox"})
	w := c.openWallet(api, "100.00")

	crashing, _ := c.spawn("consumer-crash", map[string]string{"APP_ROLES": "consumer", "FAULT_POINT": "consumer.after_commit"})
	c.sendWager(w, "msg-1", "bet-1", "BET", "40.00", "")
	assertKilled(t, crashing)
	if got := c.balance(w.id); got != "60.00" {
		t.Fatalf("balance after crash = %s, want the committed bet", got)
	}

	c.start("consumer", map[string]string{"APP_ROLES": "consumer"})
	c.waitSettled(30 * time.Second)
	if got := c.balance(w.id); got != "60.00" {
		t.Fatalf("balance after redelivery = %s, want 60.00", got)
	}
	if n := c.count(`SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1'`); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
	if n := c.queueDepth(c.dlq); n != 0 {
		t.Fatalf("dlq holds %d messages", n)
	}
	c.assertLedgerConsistency(api)
}

func TestOutboxCrashAfterPublishIsRepublishedByAnotherInstance(t *testing.T) {
	c := newCluster(t)
	api := c.start("api", map[string]string{"APP_ROLES": "api"})
	w := c.openWallet(api, "100.00")
	for i := range 3 {
		if r, err := c.wager(api, w, fmt.Sprintf("bet-%d", i), "BET", "10.00", ""); err != nil || r.status != http.StatusOK {
			t.Fatalf("bet %d: %v %v", i, err, r.body)
		}
	}
	api.Kill()
	events := c.count(`SELECT count(*) FROM outbox_events`)
	if c.unpublished() != events {
		t.Fatal("events published without an outbox role")
	}

	crashing, _ := c.spawn("outbox-crash", map[string]string{"APP_ROLES": "outbox", "FAULT_POINT": "outbox.after_publish"})
	assertKilled(t, crashing)
	if c.unpublished() == 0 {
		t.Fatal("crash happened after marking the event published")
	}

	c.start("outbox", map[string]string{"APP_ROLES": "outbox"})
	waitFor(t, 30*time.Second, "outbox published", func() bool { return c.unpublished() == 0 })
	if seen := c.drainAudit(events, 20*time.Second); len(seen) != events {
		t.Fatalf("audit received %d distinct events, want %d", len(seen), events)
	}
}

func TestPendingWorkerCrashAfterClaimIsResumedAfterLease(t *testing.T) {
	c := newCluster(t)
	crashing := c.start("pending-crash", map[string]string{"APP_ROLES": "api,pending", "FAULT_POINT": "pending.after_claim"})
	w := c.openWallet(crashing, "100.00")
	r, err := c.wager(crashing, w, "refund-1", "REFUND", "5.00", "bet-late")
	if err == nil && r.status != http.StatusAccepted {
		t.Fatalf("refund of unknown bet: %d %v", r.status, r.body)
	}
	assertKilled(t, crashing)

	app := c.start("app", nil)
	c.sendWager(w, "msg-late", "bet-late", "BET", "5.00", "")
	c.waitSettled(30 * time.Second)
	if n := c.count(`SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'refund-1' AND status = 'PROCESSED'`); n != 1 {
		t.Fatal("refund not resumed after the crashed claim")
	}
	c.assertBetRefunded(w, "bet-late", 500, "100.00")
	c.assertLedgerConsistency(app)
}

func TestWaitingRefundSurvivesKillingEveryInstance(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	w := c.openWallet(apps[0], "100.00")
	r, err := c.wager(apps[1], w, "refund-1", "REFUND", "20.00", "bet-late")
	if err != nil || r.status != http.StatusAccepted || r.body["status"] != "PENDING_REFERENCE" {
		t.Fatalf("early refund: %v %d %v", err, r.status, r.body)
	}
	refund := r.body["transactionId"].(string)
	for _, a := range apps {
		a.Kill()
	}

	restarted := []*instance{c.start("app-4", nil), c.start("app-5", nil)}
	// Over SQS the killed consumers' in-flight long polls can hide the bet for longer than the
	// short pending budget of the test cluster.
	if r, err := c.wager(restarted[0], w, "bet-late", "BET", "20.00", ""); err != nil || r.status != http.StatusOK {
		t.Fatalf("late bet: %v %d %v", err, r.status, r.body)
	}
	c.waitSettled(60 * time.Second)
	if status, code := c.transactionStatus(restarted[1], refund); status != "PROCESSED" {
		t.Fatalf("refund %s %s", status, code)
	}
	c.assertBetRefunded(w, "bet-late", 2000, "100.00")
	c.assertLedgerConsistency(restarted[0])
}

func TestAcceptedOperationsSurviveGracefulStopUnderLoad(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	const wallets = 10
	refs := make([]walletRef, wallets)
	for i := range refs {
		refs[i] = c.openWallet(apps[1], "1000.00")
	}

	var accepted sync.Map
	var stop atomic.Bool
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; !stop.Load(); n++ {
				ext := fmt.Sprintf("load-%d-%d", g, n)
				w := refs[(g+n)%wallets]
				if n%2 == 0 {
					c.sendWager(w, uuid.NewString(), ext, "BET", "1.00", "")
					accepted.Store(ext, w.id)
					continue
				}
				r, err := c.wager(apps[n%len(apps)], w, ext, "BET", "1.00", "")
				if err == nil && r.status == http.StatusOK {
					accepted.Store(ext, w.id)
				}
			}
		}()
	}

	time.Sleep(time.Second)
	code, took := apps[0].Stop(t, 30*time.Second)
	time.Sleep(500 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	if code != 0 {
		t.Fatalf("graceful stop exited %d after %v", code, took)
	}

	c.waitSettled(60 * time.Second)
	missing := 0
	accepted.Range(func(ext, _ any) bool {
		if c.count(`SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'`, ext) != 1 {
			missing++
		}
		return true
	})
	if missing != 0 {
		t.Fatalf("%d accepted operations were lost", missing)
	}
	if n := c.count(`SELECT count(*) FROM outbox_events WHERE locked_by LIKE 'app-1%' AND published_at IS NULL`); n != 0 {
		t.Fatalf("stopped instance still holds %d outbox leases", n)
	}
	c.assertLedgerConsistency(apps[2])
}

func TestAllInstancesKilledAndRestartedRecoverEverything(t *testing.T) {
	c := newCluster(t)
	apps := c.threeInstances()
	w := c.openWallet(apps[0], "1000.00")
	const messages = 30
	for i := range messages {
		c.sendWager(w, fmt.Sprintf("msg-%d", i), fmt.Sprintf("bet-%d", i), "BET", "1.00", "")
	}
	time.Sleep(200 * time.Millisecond)
	for _, a := range apps {
		a.Kill()
	}

	restarted := []*instance{c.start("app-4", nil), c.start("app-5", nil)}
	c.waitSettled(60 * time.Second)
	if n := c.count(`SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'BET' AND status = 'PROCESSED'`, w.id); n != messages {
		t.Fatalf("processed bets = %d, want %d", n, messages)
	}
	if got := c.balance(w.id); got != "970.00" {
		t.Fatalf("balance = %s, want 970.00", got)
	}
	events := c.count(`SELECT count(*) FROM outbox_events`)
	if seen := c.drainAudit(events, 30*time.Second); len(seen) != events {
		t.Fatalf("audit received %d distinct events, want %d", len(seen), events)
	}
	c.assertLedgerConsistency(restarted[0])
}
