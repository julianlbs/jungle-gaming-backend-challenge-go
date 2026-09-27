package httpapi

import (
	"net/http"
	"strings"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
)

const maxIdempotencyKeyLength = 200

var problemProviderMismatch = problem(http.StatusForbidden, "PROVIDER_MISMATCH", "the token is not authorized for this provider")

func (a *API) registerWageringRoutes() {
	a.protected("POST /wagering/transactions", a.submitWager, auth.ScopeWageringWrite)
	a.protected("GET /wagering/transactions/{transactionId}", a.getTransaction,
		auth.ScopeWageringRead, auth.ScopeWageringReadAll)
	a.protected("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", a.getProviderTransaction,
		auth.ScopeWageringRead, auth.ScopeWageringReadAll)
}

type wagerRequest struct {
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	PlayerID                       string     `json:"playerId"`
	WalletID                       string     `json:"walletId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Kind                           string     `json:"kind"`
	Money                          *moneyBody `json:"money"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId"`
}

func (a *API) submitWager(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	switch {
	case strings.TrimSpace(key) == "":
		writeProblem(w, r, validationProblem(FieldProblem{Field: "Idempotency-Key", Reason: "header is required"}))
		return
	case len(key) > maxIdempotencyKeyLength:
		writeProblem(w, r, validationProblem(FieldProblem{Field: "Idempotency-Key", Reason: "is too long"}))
		return
	}
	var req wagerRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, *p)
		return
	}
	if req.Money == nil {
		writeProblem(w, r, validationProblem(FieldProblem{Field: "money", Reason: "is required"}))
		return
	}
	principal, _ := auth.PrincipalFrom(r.Context())
	if principal.ProviderID == "" || principal.ProviderID != req.ProviderID {
		writeProblem(w, r, problemProviderMismatch)
		return
	}

	ctx := logging.With(r.Context(), "externalTransactionId", req.ExternalTransactionID)
	out, err := a.Wagers.Process(ctx, app.WagerCommand{
		Channel:                        wagering.ChannelHTTP,
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 key,
		WalletID:                       req.WalletID,
		PlayerID:                       req.PlayerID,
		Kind:                           req.Kind,
		Amount:                         req.Money.Amount,
		Currency:                       req.Money.Currency,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
		CorrelationID:                  logging.CorrelationID(ctx),
	})
	if err != nil {
		writeError(w, r.WithContext(ctx), a.log, err)
		return
	}

	t := out.Transaction
	body := wagerResultView(t, out.Replay)
	status := wagerStatusCode(t.Status())
	if status == http.StatusAccepted {
		w.Header().Set("Location", "/wagering/transactions/"+t.ID().String())
	}
	a.log.InfoContext(logging.With(ctx, logging.KeyTransactionID, t.ID().String()), "wager handled",
		"status", string(t.Status()), "replay", out.Replay)
	writeJSON(w, status, body)
}

func wagerStatusCode(s wagering.Status) int {
	switch s {
	case wagering.StatusProcessed:
		return http.StatusOK
	case wagering.StatusRejected:
		return http.StatusUnprocessableEntity
	case wagering.StatusFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusAccepted
	}
}

func (a *API) getTransaction(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFrom(r.Context())
	viewer := app.Viewer{ProviderID: principal.ProviderID, ReadAll: principal.HasScope(auth.ScopeWageringReadAll)}
	t, err := a.Queries.Transaction(r.Context(), r.PathValue("transactionId"), viewer)
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionView(t))
}

func (a *API) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	principal, _ := auth.PrincipalFrom(r.Context())
	if !principal.HasScope(auth.ScopeWageringReadAll) && principal.ProviderID != providerID {
		writeProblem(w, r, problemProviderMismatch)
		return
	}
	t, err := a.Queries.TransactionByExternalID(r.Context(), providerID, r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionView(t))
}
