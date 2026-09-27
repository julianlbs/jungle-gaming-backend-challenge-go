package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

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
		func(pool *pgxpool.Pool) *httpapi.Readiness { return httpapi.NewReadiness(pool.Ping) },
		newAPI,
	),
	fx.Invoke(registerHTTPServer),
)

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
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", cfg.HTTPAddr)
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
