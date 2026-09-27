// Package httpapi exposes the use cases over HTTP.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

type WalletOpener interface {
	Open(ctx context.Context, cmd app.OpenWalletCommand) (*wallet.Wallet, error)
}

type WagerService interface {
	Process(ctx context.Context, cmd app.WagerCommand) (app.WagerOutcome, error)
}

type QueryService interface {
	Wallet(ctx context.Context, walletID string) (*wallet.Wallet, error)
	Ledger(ctx context.Context, walletID, cursor string, limit int) (app.LedgerPage, error)
	Transaction(ctx context.Context, transactionID string, viewer app.Viewer) (*wagering.Transaction, error)
	TransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
	Reconcile(ctx context.Context, walletID string) (app.Reconciliation, error)
}

type Deps struct {
	Log      *slog.Logger
	Metrics  *metrics.Metrics
	Verifier TokenVerifier
	Wallets  WalletOpener
	Wagers   WagerService
	Queries  QueryService
}

// API holds the dependencies shared by the handlers.
type API struct {
	Deps
	log *slog.Logger
	mux *http.ServeMux
}

func NewAPI(d Deps) *API {
	a := &API{Deps: d, log: d.Log, mux: http.NewServeMux()}
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problemNotFound)
	})
	a.registerWalletRoutes()
	return a
}

// protected registers a route that requires authentication and one of the scopes.
func (a *API) protected(pattern string, h http.HandlerFunc, scopes ...string) {
	a.mux.Handle(pattern, authenticated(a.Verifier, a.log, requireAnyScope(h, scopes...)))
}

func (a *API) public(pattern string, h http.HandlerFunc) {
	a.mux.HandleFunc(pattern, h)
}

func (a *API) Handler() http.Handler {
	return observe(a.log, a.Metrics, a.mux)
}

func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
