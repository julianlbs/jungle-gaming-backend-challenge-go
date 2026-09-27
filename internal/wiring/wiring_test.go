package wiring

import (
	"testing"

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
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
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

func TestAPIRoleGraphIsComplete(t *testing.T) {
	err := fx.ValidateApp(
		Options(testConfig(t, "api")),
		fx.Invoke(func(*httpapi.API) {}),
	)
	if err != nil {
		t.Fatal(err)
	}
}
