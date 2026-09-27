// Package lifecycle ties background workers to the Fx application lifecycle.
package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"go.uber.org/fx"
)

// Register starts run in its own goroutine when the application starts and cancels its
// context on stop, waiting until it returns or the stop deadline expires.
func Register(lc fx.Lifecycle, log *slog.Logger, name string, run func(ctx context.Context)) {
	var (
		cancel context.CancelFunc
		done   = make(chan struct{})
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			go func() {
				defer close(done)
				run(ctx)
			}()
			log.Info("worker started", "worker", name)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				log.Info("worker stopped", "worker", name)
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker %s did not stop in time: %w", name, ctx.Err())
			}
		},
	})
}

// Every calls tick immediately and then after each interval until ctx is done. A panicking
// tick is logged and the loop continues.
func Every(ctx context.Context, log *slog.Logger, name string, interval time.Duration, tick func(context.Context)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		safeTick(ctx, log, name, tick)
		timer.Reset(interval)
	}
}

func safeTick(ctx context.Context, log *slog.Logger, name string, tick func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "worker tick panicked", "worker", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	tick(ctx)
}
