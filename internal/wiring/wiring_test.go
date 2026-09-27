package wiring

import (
	"testing"

	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{
			"APP_ROLES":    "pending",
			"DATABASE_URL": "postgres://wallet_app:x@localhost:5432/wallet",
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
		Options(testConfig(t)),
		fx.Invoke(func(*app.WalletOpener, *app.WagerProcessor, *app.PendingResumer, *app.Queries) {}),
	)
	if err != nil {
		t.Fatal(err)
	}
}
