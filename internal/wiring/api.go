package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/httpapi"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

var API = fx.Module("api",
	fx.Provide(
		func(cfg config.Config) httpapi.TokenVerifier {
			return auth.NewVerifier(context.Background(), auth.Config{
				Issuer: cfg.Auth.Issuer, JWKSURL: cfg.Auth.JWKSURL, Audience: cfg.Auth.Audience,
			})
		},
		newReadiness,
		newAPI,
	),
	fx.Invoke(registerHTTPServer),
)

type readinessDeps struct {
	fx.In
	Cfg  config.Config
	Pool *pgxpool.Pool
	SQS  *sqs.Client `optional:"true"`
	SNS  *sns.Client `optional:"true"`
}

// newReadiness probes every dependency the enabled roles use.
func newReadiness(d readinessDeps) *httpapi.Readiness {
	checks := []httpapi.ReadinessCheck{{Name: "database", Check: d.Pool.Ping}}
	if d.Cfg.Roles.Has(config.RoleConsumer) && d.SQS != nil {
		checks = append(checks, httpapi.ReadinessCheck{Name: "sqs", Check: sqsCheck(d.SQS, d.Cfg.SQS.Queues)})
	}
	if d.Cfg.Roles.Has(config.RoleOutbox) && d.SNS != nil {
		checks = append(checks, httpapi.ReadinessCheck{Name: "sns", Check: snsCheck(d.SNS, d.Cfg.SNS.TopicARN)})
	}
	return httpapi.NewReadiness(checks...)
}

type queueAttributesAPI interface {
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type topicAttributesAPI interface {
	GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
}

func sqsCheck(c queueAttributesAPI, queues []config.ProviderQueue) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, q := range queues {
			if _, err := c.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
				QueueUrl:       aws.String(q.URL),
				AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
			}, withoutRetries); err != nil {
				return fmt.Errorf("%s: %w", q.ProviderID, err)
			}
		}
		return nil
	}
}

func snsCheck(c topicAttributesAPI, topicARN string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := c.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)},
			func(o *sns.Options) { o.RetryMaxAttempts = 1 })
		return err
	}
}

func withoutRetries(o *sqs.Options) { o.RetryMaxAttempts = 1 }

func newAPI(log *slog.Logger, m *metrics.Metrics, v httpapi.TokenVerifier, opener *app.WalletOpener,
	wagers *app.WagerProcessor, queries *app.Queries, ready *httpapi.Readiness) *httpapi.API {
	return httpapi.NewAPI(httpapi.Deps{
		Log: log, Metrics: m, Verifier: v,
		Wallets: opener, Wagers: wagers, Queries: queries, Readiness: ready,
	})
}

// registerHTTPServer stops after readiness has turned unready, so that in-flight requests
// finish within the shutdown timeout while new traffic goes to other instances.
func registerHTTPServer(lc fx.Lifecycle, cfg config.Config, api *httpapi.API, ready *httpapi.Readiness, log *slog.Logger) {
	srv := httpapi.NewServer(cfg.HTTPAddr, api.Handler())
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.HTTPAddr)
			if err != nil {
				return fmt.Errorf("http listener: %w", err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server stopped", "error", err)
				}
			}()
			log.Info("http server listening", "addr", ln.Addr().String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			ready.StartDraining()
			log.Info("http server draining")
			return srv.Shutdown(ctx)
		},
	})
}
