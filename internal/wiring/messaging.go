package wiring

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

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
