// Package httpapi exposes the use cases over HTTP.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

// API holds the dependencies shared by the handlers.
type API struct {
	log      *slog.Logger
	metrics  *metrics.Metrics
	verifier TokenVerifier
	mux      *http.ServeMux
}

func NewAPI(log *slog.Logger, m *metrics.Metrics, verifier TokenVerifier) *API {
	a := &API{log: log, metrics: m, verifier: verifier, mux: http.NewServeMux()}
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problemNotFound)
	})
	return a
}

// protected registers a route that requires authentication and one of the scopes.
func (a *API) protected(pattern string, h http.HandlerFunc, scopes ...string) {
	a.mux.Handle(pattern, authenticated(a.verifier, a.log, requireAnyScope(h, scopes...)))
}

func (a *API) public(pattern string, h http.HandlerFunc) {
	a.mux.HandleFunc(pattern, h)
}

func (a *API) Handler() http.Handler {
	return observe(a.log, a.metrics, a.mux)
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
