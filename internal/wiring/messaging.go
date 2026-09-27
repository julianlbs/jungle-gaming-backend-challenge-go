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
	fx.Invoke(registerRelay),
)

func registerRelay(lc fx.Lifecycle, cfg config.Config, store *postgres.OutboxStore, client *sns.Client,
	log *slog.Logger, m *metrics.Metrics) {
	log = log.With("worker", "outbox-relay")
	relay := app.NewOutboxRelay(store, snspublisher.New(client, cfg.SNS.TopicARN), app.RelayConfig{
		Owner:     cfg.InstanceID + "-" + uuid.NewString()[:8],
		Lease:     cfg.Outbox.Lease,
		BatchSize: cfg.Outbox.BatchSize,
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

func registerConsumer(lc fx.Lifecycle, cfg config.Config, client *sqs.Client, intake *app.WagerIntake,
	log *slog.Logger, m *metrics.Metrics) {
	c := sqsconsumer.New(client, intake, sqsconsumer.Config{
		QueueURL:         cfg.SQS.QueueURL,
		DLQURL:           cfg.SQS.DLQURL,
		AllowedProviders: cfg.SQS.AllowedProviders,
		MaxInFlight:      cfg.SQS.MaxInFlight,
		MessageTimeout:   cfg.SQS.MessageTimeout,
	}, log, m)
	lifecycle.Register(lc, log, "sqs-consumer", c.Run)
}
