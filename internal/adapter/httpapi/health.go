package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// Readiness reports whether the instance should receive traffic. It becomes unready as soon as
// shutdown starts so that load balancers stop routing before the server drains.
type Readiness struct {
	draining atomic.Bool
	check    func(context.Context) error
}

func NewReadiness(check func(context.Context) error) *Readiness {
	return &Readiness{check: check}
}

func (h *Readiness) StartDraining() { h.draining.Store(true) }

func (h *Readiness) Ready(ctx context.Context) (string, bool) {
	if h.draining.Load() {
		return "draining", false
	}
	if h.check != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := h.check(ctx); err != nil {
			return "database unavailable", false
		}
	}
	return "ok", true
}

type healthResponse struct {
	Status string `json:"status"`
}

func (a *API) registerHealthRoutes() {
	a.public("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})
	a.public("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if a.Readiness == nil {
			writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
			return
		}
		status, ok := a.Readiness.Ready(r.Context())
		if !ok {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: status})
			return
		}
		writeJSON(w, http.StatusOK, healthResponse{Status: status})
	})
}
