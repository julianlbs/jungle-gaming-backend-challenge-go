package httpapi

import (
	"net/http"
	"strconv"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
)

func (a *API) registerWalletRoutes() {
	a.protected("POST /wallets", a.openWallet, auth.ScopeWalletsWrite)
	a.protected("GET /wallets/{walletId}", a.getWallet, auth.ScopeWalletsRead)
	a.protected("GET /wallets/{walletId}/ledger", a.getLedger, auth.ScopeWalletsRead)
	a.protected("POST /wallets/{walletId}/reconciliation", a.reconcile, auth.ScopeWalletsReconcile)
}

type openWalletRequest struct {
	PlayerID       string     `json:"playerId"`
	InitialBalance *moneyBody `json:"initialBalance"`
}

func (a *API) openWallet(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, *p)
		return
	}
	if req.InitialBalance == nil {
		writeProblem(w, r, validationProblem(FieldProblem{Field: "initialBalance", Reason: "is required"}))
		return
	}
	wl, err := a.Wallets.Open(r.Context(), app.OpenWalletCommand{
		PlayerID:      req.PlayerID,
		Currency:      req.InitialBalance.Currency,
		InitialAmount: req.InitialBalance.Amount,
		CorrelationID: logging.CorrelationID(r.Context()),
	})
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	a.log.InfoContext(logging.With(r.Context(), logging.KeyWalletID, wl.ID().String()), "wallet opened")
	w.Header().Set("Location", "/wallets/"+wl.ID().String())
	writeJSON(w, http.StatusCreated, walletView(wl))
}

func (a *API) getWallet(w http.ResponseWriter, r *http.Request) {
	wl, err := a.Queries.Wallet(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, walletView(wl))
}

func (a *API) getLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeProblem(w, r, validationProblem(FieldProblem{Field: "limit", Reason: "must be an integer"}))
			return
		}
		limit = n
	}
	page, err := a.Queries.Ledger(r.Context(), r.PathValue("walletId"), q.Get("cursor"), limit)
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	resp := ledgerPageResponse{Items: make([]ledgerEntryResponse, 0, len(page.Items))}
	for _, e := range page.Items {
		resp.Items = append(resp.Items, ledgerEntryView(e))
	}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	rec, err := a.Queries.Reconcile(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	a.Metrics.ObserveReconciliation(rec.Consistent)
	ctx := logging.With(r.Context(), logging.KeyWalletID, rec.Wallet.ID().String())
	if rec.Consistent {
		a.log.InfoContext(ctx, "wallet reconciled", "entries", rec.EntryCount)
	} else {
		a.log.ErrorContext(ctx, "wallet balance diverges from ledger",
			"stored", rec.Stored.String(), "calculated", rec.Calculated.String(),
			"difference", rec.Difference.String(), "entries", rec.EntryCount)
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:          rec.Wallet.ID().String(),
		StoredBalance:     moneyView(rec.Stored),
		CalculatedBalance: moneyView(rec.Calculated),
		Difference:        moneyView(rec.Difference),
		Consistent:        rec.Consistent,
		CheckedEntries:    rec.EntryCount,
		CheckedAt:         timeView(rec.CheckedAt),
	})
}
