package lifecycle

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRegisterStartsAndStops(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	var running, stopped atomic.Bool
	Register(lc, discard, "test", func(ctx context.Context) {
		running.Store(true)
		<-ctx.Done()
		stopped.Store(true)
	})
	lc.RequireStart()
	deadline := time.Now().Add(time.Second)
	for !running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !running.Load() {
		t.Fatal("worker did not start")
	}
	lc.RequireStop()
	if !stopped.Load() {
		t.Fatal("stop returned before the worker finished")
	}
}

func TestRegisterReportsStuckWorker(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	release := make(chan struct{})
	defer close(release)
	Register(lc, discard, "stuck", func(context.Context) { <-release })
	lc.RequireStart()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lc.Stop(ctx); err == nil {
		t.Fatal("expected a stop timeout error")
	}
}

func TestEveryRecoversFromPanics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ticks atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Every(ctx, discard, "flaky", time.Millisecond, func(context.Context) {
			if ticks.Add(1) == 1 {
				panic("boom")
			}
		})
	}()
	deadline := time.Now().Add(time.Second)
	for ticks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if ticks.Load() < 3 {
		t.Fatalf("loop stopped after panic: %d ticks", ticks.Load())
	}
}
