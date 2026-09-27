package wiring

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/snspublisher"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/sqsconsumer"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/lifecycle"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

var AWS = fx.Module("aws",
	fx.Provide(
		newAWSConfig,
		func(cfg aws.Config) *sqs.Client { return sqs.NewFromConfig(cfg) },
	),
)

func newAWSConfig(cfg config.Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.AWS.Region)}
	if cfg.AWS.EndpointURL != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(cfg.AWS.EndpointURL))
	}
	return awsconfig.LoadDefaultConfig(context.Background(), opts...)
}

var Consumer = fx.Module("consumer",
	fx.Provide(app.NewWagerIntake),
	fx.Invoke(registerConsumer),
)

var Outbox = fx.Module("outbox",
	fx.Provide(func(cfg aws.Config) *sns.Client { return sns.NewFromConfig(cfg) }),
	fx.Invoke(registerRelay, registerOutboxPurge),
)

func registerRelay(lc fx.Lifecycle, cfg config.Config, store *postgres.OutboxStore, client *sns.Client,
	log *slog.Logger, m *metrics.Metrics) {
	relay := app.NewOutboxRelay(store, faultyPublisher{snspublisher.New(client, cfg.SNS.TopicARN)}, app.RelayConfig{
		Owner:     cfg.InstanceID + "-" + uuid.NewString()[:8],
		Lease:     cfg.Outbox.Lease,
		BatchSize: cfg.Outbox.BatchSize,
		BaseDelay: cfg.Outbox.BackoffBase,
		MaxDelay:  cfg.Outbox.BackoffMax,
	}, func(res app.RelayResult, ev app.ClaimedEvent, err error) {
		m.OutboxPublish.WithLabelValues(string(res)).Inc()
		if res != app.RelayPublished {
			log.Warn("outbox event not confirmed", "result", string(res), "eventId", ev.ID.String(),
				"eventType", ev.EventType, "attempts", ev.Attempts, "error", err)
		}
	})
	lifecycle.Register(lc, log, "outbox-relay", func(ctx context.Context) {
		lifecycle.Every(ctx, log, "outbox-relay", cfg.Outbox.PollInterval, func(ctx context.Context) {
			if err := relay.Drain(ctx); err != nil && ctx.Err() == nil {
				log.Warn("outbox claim failed", "error", err)
			}
			if count, age, err := store.Backlog(ctx); err == nil {
				m.ObserveOutboxBacklog(count, age)
			}
		})
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n, err := relay.Release(rctx); err != nil {
			log.Warn("releasing outbox leases failed", "error", err)
		} else if n > 0 {
			log.Info("released outbox leases", "count", n)
		}
	})
}

const outboxPurgeBatch = 1000

func registerOutboxPurge(lc fx.Lifecycle, cfg config.Config, store *postgres.OutboxStore, log *slog.Logger) {
	if cfg.Outbox.Retention <= 0 {
		return
	}
	lifecycle.Register(lc, log, "outbox-purge", func(ctx context.Context) {
		lifecycle.Every(ctx, log, "outbox-purge", time.Minute, func(ctx context.Context) {
			if n, err := purgeOutbox(ctx, store, time.Now().Add(-cfg.Outbox.Retention)); err != nil && ctx.Err() == nil {
				log.Warn("purging published outbox events failed", "error", err)
			} else if n > 0 {
				log.Info("purged published outbox events", "count", n)
			}
		})
	})
}

func purgeOutbox(ctx context.Context, store *postgres.OutboxStore, before time.Time) (int64, error) {
	var total int64
	for ctx.Err() == nil {
		n, err := store.PurgePublished(ctx, before, outboxPurgeBatch)
		total += n
		if err != nil || n < outboxPurgeBatch {
			return total, err
		}
	}
	return total, ctx.Err()
}

var Pending = fx.Module("pending", fx.Invoke(registerPendingWorker))

func registerPendingWorker(lc fx.Lifecycle, cfg config.Config, resumer *app.PendingResumer, store *postgres.PendingStore,
	log *slog.Logger, m *metrics.Metrics) {
	lifecycle.Register(lc, log, "pending-references", func(ctx context.Context) {
		lifecycle.Every(ctx, log, "pending-references", cfg.Pending.PollInterval, func(ctx context.Context) {
			for ctx.Err() == nil {
				results, err := resumer.RunOnce(ctx, cfg.Pending.Lease, cfg.Pending.BatchSize)
				claimed := 0
				for res, n := range results {
					m.ReferenceResolutions.WithLabelValues(res.String()).Add(float64(n))
					claimed += n
				}
				if err != nil && ctx.Err() == nil {
					log.Warn("resuming pending operations failed", "error", err)
					break
				}
				if claimed < cfg.Pending.BatchSize {
					break
				}
			}
			if n, err := store.CountWaiting(ctx); err == nil {
				m.PendingReferences.Set(float64(n))
			}
		})
	})
}

func registerConsumer(lc fx.Lifecycle, cfg config.Config, client *sqs.Client, intake *app.WagerIntake,
	log *slog.Logger, m *metrics.Metrics) {
	c := sqsconsumer.New(client, faultyHandler{intake}, sqsconsumer.Config{
		QueueURL:         cfg.SQS.QueueURL,
		DLQURL:           cfg.SQS.DLQURL,
		AllowedProviders: cfg.SQS.AllowedProviders,
		MaxInFlight:      cfg.SQS.MaxInFlight,
		MessageTimeout:   cfg.SQS.MessageTimeout,
	}, log, m)
	lifecycle.Register(lc, log, "sqs-consumer", c.Run)
}
