package wiring

import (
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/httpapi"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
)

func testConfig(t *testing.T, roles string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{
			"APP_ROLES":     roles,
			"DATABASE_URL":  "postgres://wallet_app:x@localhost:5432/wallet",
			"OIDC_ISSUER":   "http://keycloak:8080/realms/wallet",
			"OIDC_JWKS_URL": "http://keycloak:8080/realms/wallet/protocol/openid-connect/certs",
			"OIDC_AUDIENCE": "wallet-api",

			"AWS_REGION":           "us-east-1",
			"SQS_PROVIDER_QUEUES":  "provider-a=http://localstack:4566/000000000000/wager-transactions-provider-a.fifo",
			"SQS_WAGER_DLQ_URL":    "http://localstack:4566/000000000000/wager-transactions-dlq.fifo",
			"SNS_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMetricsServerTimeouts(t *testing.T) {
	srv := newMetricsServer(nil)
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 15*time.Second ||
		srv.WriteTimeout != 30*time.Second || srv.IdleTimeout != 60*time.Second {
		t.Fatalf("timeouts = header %s read %s write %s idle %s",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

func TestDependencyGraphIsComplete(t *testing.T) {
	err := fx.ValidateApp(
		Options(testConfig(t, "pending")),
		fx.Invoke(func(*app.WalletOpener, *app.WagerProcessor, *app.PendingResumer, *app.Queries) {}),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEachRoleGraphIsComplete(t *testing.T) {
	for _, roles := range []string{"api", "consumer", "outbox", "pending", "api,consumer,outbox,pending"} {
		if err := fx.ValidateApp(Options(testConfig(t, roles))); err != nil {
			t.Errorf("%s: %v", roles, err)
		}
	}
	err := fx.ValidateApp(
		Options(testConfig(t, "api")),
		fx.Invoke(func(*httpapi.API) {}),
	)
	if err != nil {
		t.Fatal(err)
	}
}
