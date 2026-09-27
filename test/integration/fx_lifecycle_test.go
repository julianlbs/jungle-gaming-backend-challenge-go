//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth/authtest"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/sqsconsumer"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/wiring"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln := must(net.Listen("tcp", "127.0.0.1:0"))
	defer ln.Close()
	return ln.Addr().String()
}

func TestApplicationStartsAndShutsDownGracefully(t *testing.T) {
	f := newAppFixture(t)
	a := newAWS(t)
	dlq := a.fifoQueue(t, "dlq", "", 0)
	queue := a.fifoQueue(t, "wagers", dlq, 5)
	topic, audit := a.fifoTopic(t)
	issuer := authtest.NewTokenIssuer(t)

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	httpAddr := freeAddr(t)
	vars := map[string]string{
		"APP_ROLES":             "api,consumer,outbox,pending",
		"INSTANCE_ID":           "lifecycle-test",
		"HTTP_ADDR":             httpAddr,
		"METRICS_ADDR":          freeAddr(t),
		"LOG_LEVEL":             "error",
		"SHUTDOWN_TIMEOUT":      "15s",
		"DATABASE_URL":          withDatabase(env.appURL, f.db.Name),
		"OIDC_ISSUER":           authtest.Issuer,
		"OIDC_JWKS_URL":         issuer.JWKSURL(),
		"OIDC_AUDIENCE":         authtest.Audience,
		"AWS_ENDPOINT_URL":      getenv("TEST_AWS_ENDPOINT_URL", "http://localhost:4566"),
		"SQS_WAGER_QUEUE_URL":   queue,
		"SQS_WAGER_DLQ_URL":     dlq,
		"SQS_ALLOWED_PROVIDERS": "provider-a",
		"SNS_EVENTS_TOPIC_ARN":  topic,
		"OUTBOX_POLL_INTERVAL":  "50ms",
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}

	application := fx.New(wiring.Options(cfg))
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = application.Stop(context.Background())
		}
	})

	resp := must(http.Get("http://" + httpAddr + "/health/ready"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", resp.StatusCode)
	}

	w := f.openWallet(t, "100.00")
	a.send(t, queue, w.ID().String(), "d1",
		must(sqsconsumer.Encode("msg-1", "2026-09-08T12:00:00Z", "", wager(w, "BET", "10.00", "ext-1"))))
	waitFor(t, 20*time.Second, "message consumed", func() bool { return f.balance(t, w) == "90.00" })

	events := countRows(t, f.db, `SELECT count(*) FROM outbox_events`)
	waitFor(t, 20*time.Second, "events published", func() bool {
		return countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	if got := a.drain(t, audit, events, 20*time.Second); len(got) != events {
		t.Fatalf("delivered %d events, want %d", len(got), events)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStop()
	began := time.Now()
	if err := application.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped = true
	if d := time.Since(began); d > 10*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if _, err := net.DialTimeout("tcp", httpAddr, time.Second); err == nil {
		t.Fatal("http server still accepting connections")
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE locked_by IS NOT NULL`); n != 0 {
		t.Fatalf("%d outbox leases held after shutdown", n)
	}
}
