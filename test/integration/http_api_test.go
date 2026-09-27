//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth/authtest"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/httpapi"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

type apiFixture struct {
	server *httptest.Server
	issuer *authtest.TokenIssuer
}

func newAPIFixture(t *testing.T) *apiFixture {
	f := newAppFixture(t)
	issuer := authtest.NewTokenIssuer(t)
	api := httpapi.NewAPI(httpapi.Deps{
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:   metrics.New(),
		Verifier:  auth.NewVerifier(context.Background(), auth.Config{Issuer: authtest.Issuer, JWKSURL: issuer.JWKSURL(), Audience: authtest.Audience}),
		Wallets:   f.opener,
		Wagers:    f.processor,
		Queries:   app.NewQueries(postgres.NewReadModel(f.db.App), app.SystemClock{}),
		Readiness: httpapi.NewReadiness(httpapi.ReadinessCheck{Name: "database", Check: f.db.App.Ping}),
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &apiFixture{server: srv, issuer: issuer}
}

func (f *apiFixture) token(t *testing.T, provider, scope string) string {
	return f.issuer.Token(t, authtest.Claims{Subject: "svc", ClientID: provider + "-client", ProviderID: provider, Scope: scope})
}

type apiResponse struct {
	status int
	header http.Header
	body   map[string]any
}

func (f *apiFixture) do(t *testing.T, method, path, token string, headers map[string]string, body any) apiResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(must(json.Marshal(body)))
	}
	req := must(http.NewRequestWithContext(t.Context(), method, f.server.URL+path, reader))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := apiResponse{status: resp.StatusCode, header: resp.Header}
	raw := must(io.ReadAll(resp.Body))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			t.Fatalf("%s %s: invalid json %q", method, path, raw)
		}
	}
	return out
}

func expectStatus(t *testing.T, r apiResponse, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d; body %v", r.status, status, r.body)
	}
	if code != "" && r.body["code"] != code && r.body["failureCode"] != code {
		t.Fatalf("code = %v/%v, want %s", r.body["code"], r.body["failureCode"], code)
	}
}

func brlBody(amount string) map[string]string {
	return map[string]string{"amount": amount, "currency": "BRL"}
}

func wagerBody(walletID, playerID, externalID, kind, amount string) map[string]any {
	return map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID,
		"playerId": playerID, "walletId": walletID,
		"roundId": "round-1", "gameId": "game-1", "kind": kind, "money": brlBody(amount),
	}
}

func TestHTTPAuthentication(t *testing.T) {
	f := newAPIFixture(t)
	path := "/wallets/" + uuid.NewString()
	c := authtest.Claims{ProviderID: "provider-a", Scope: auth.ScopeWalletsRead}

	cases := map[string]string{
		"missing":        "",
		"expired":        f.issuer.Token(t, authtest.Claims{ProviderID: "provider-a", Scope: auth.ScopeWalletsRead, Expired: true}),
		"foreign key":    f.issuer.ForeignToken(t, c),
		"wrong audience": f.issuer.Token(t, authtest.Claims{ProviderID: "provider-a", Scope: auth.ScopeWalletsRead, Audience: []string{"other"}}),
		"wrong issuer":   f.issuer.Token(t, authtest.Claims{ProviderID: "provider-a", Scope: auth.ScopeWalletsRead, Issuer: "http://evil"}),
		"garbage":        "not-a-jwt",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			r := f.do(t, http.MethodGet, path, tok, nil, nil)
			expectStatus(t, r, http.StatusUnauthorized, "UNAUTHENTICATED")
			if r.header.Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate")
			}
		})
	}

	r := f.do(t, http.MethodGet, path, f.token(t, "provider-a", auth.ScopeWageringRead), nil, nil)
	expectStatus(t, r, http.StatusForbidden, "FORBIDDEN")

	r = f.do(t, http.MethodGet, path, f.issuer.Token(t, c), nil, nil)
	expectStatus(t, r, http.StatusNotFound, "NOT_FOUND")

	r = f.do(t, http.MethodGet, "/health/ready", "", nil, nil)
	expectStatus(t, r, http.StatusOK, "")
}

func TestHTTPWalletAndWagerFlow(t *testing.T) {
	f := newAPIFixture(t)
	internal := f.token(t, "", auth.ScopeWalletsWrite+" "+auth.ScopeWalletsRead+" "+auth.ScopeWalletsReconcile+" "+auth.ScopeWageringReadAll)
	providerA := f.token(t, "provider-a", auth.ScopeWageringWrite+" "+auth.ScopeWageringRead)
	providerB := f.token(t, "provider-b", auth.ScopeWageringWrite+" "+auth.ScopeWageringRead)

	player := uuid.NewString()
	r := f.do(t, http.MethodPost, "/wallets", internal, nil, map[string]any{"playerId": player, "initialBalance": brlBody("100.00")})
	expectStatus(t, r, http.StatusCreated, "")
	walletID := r.body["id"].(string)
	if r.header.Get("Location") != "/wallets/"+walletID {
		t.Fatalf("location = %q", r.header.Get("Location"))
	}
	r = f.do(t, http.MethodPost, "/wallets", internal, nil, map[string]any{"playerId": player, "initialBalance": brlBody("1.00")})
	expectStatus(t, r, http.StatusConflict, "WALLET_ALREADY_EXISTS")
	r = f.do(t, http.MethodPost, "/wallets", providerA, nil, map[string]any{"playerId": uuid.NewString(), "initialBalance": brlBody("1.00")})
	expectStatus(t, r, http.StatusForbidden, "FORBIDDEN")

	key := map[string]string{"Idempotency-Key": "provider-a:tx-1"}
	bet := wagerBody(walletID, player, "tx-1", "BET", "25.00")

	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, nil, bet)
	expectStatus(t, r, http.StatusBadRequest, "VALIDATION_ERROR")

	r = f.do(t, http.MethodPost, "/wagering/transactions", providerB, map[string]string{"Idempotency-Key": "provider-b:tx-1"}, bet)
	expectStatus(t, r, http.StatusForbidden, "PROVIDER_MISMATCH")

	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, key, bet)
	expectStatus(t, r, http.StatusOK, "")
	txID := r.body["transactionId"].(string)
	if r.body["balance"].(map[string]any)["amount"] != "75.00" || r.body["idempotentReplay"] != false {
		t.Fatalf("bet body = %v", r.body)
	}

	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, key, bet)
	expectStatus(t, r, http.StatusOK, "")
	if r.body["idempotentReplay"] != true || r.body["transactionId"] != txID ||
		r.body["balance"].(map[string]any)["amount"] != "75.00" {
		t.Fatalf("replay body = %v", r.body)
	}

	changed := wagerBody(walletID, player, "tx-1", "BET", "26.00")
	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, key, changed)
	expectStatus(t, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED")
	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, map[string]string{"Idempotency-Key": "other"}, bet)
	expectStatus(t, r, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")

	big := wagerBody(walletID, player, "tx-2", "BET", "500.00")
	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, map[string]string{"Idempotency-Key": "provider-a:tx-2"}, big)
	expectStatus(t, r, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS")
	if r.body["status"] != "REJECTED" {
		t.Fatalf("rejected body = %v", r.body)
	}

	refund := wagerBody(walletID, player, "tx-3", "REFUND", "10.00")
	refund["referenceExternalTransactionId"] = "tx-unknown"
	r = f.do(t, http.MethodPost, "/wagering/transactions", providerA, map[string]string{"Idempotency-Key": "provider-a:tx-3"}, refund)
	expectStatus(t, r, http.StatusAccepted, "")
	if r.body["status"] != "PENDING_REFERENCE" || r.header.Get("Location") == "" || r.body["nextAttemptAt"] == nil {
		t.Fatalf("pending response = %v %v", r.header, r.body)
	}

	r = f.do(t, http.MethodGet, "/wagering/transactions/"+txID, providerA, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if r.body["externalTransactionId"] != "tx-1" {
		t.Fatalf("transaction = %v", r.body)
	}
	r = f.do(t, http.MethodGet, "/wagering/transactions/"+txID, providerB, nil, nil)
	expectStatus(t, r, http.StatusNotFound, "NOT_FOUND")
	r = f.do(t, http.MethodGet, "/wagering/transactions/"+txID, internal, nil, nil)
	expectStatus(t, r, http.StatusOK, "")

	r = f.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", providerA, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	r = f.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", providerB, nil, nil)
	expectStatus(t, r, http.StatusForbidden, "PROVIDER_MISMATCH")
	r = f.do(t, http.MethodGet, "/providers/provider-b/wagering/transactions/tx-1", providerB, nil, nil)
	expectStatus(t, r, http.StatusNotFound, "NOT_FOUND")

	r = f.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger?limit=1", internal, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if len(r.body["items"].([]any)) != 1 || r.body["nextCursor"] == nil {
		t.Fatalf("ledger page = %v", r.body)
	}
	r = f.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger?cursor="+r.body["nextCursor"].(string), internal, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if len(r.body["items"].([]any)) != 1 || r.body["nextCursor"] != nil {
		t.Fatalf("last ledger page = %v", r.body)
	}

	r = f.do(t, http.MethodPost, "/wallets/"+walletID+"/reconciliation", internal, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if r.body["consistent"] != true || r.body["checkedEntries"] != float64(2) ||
		r.body["difference"].(map[string]any)["amount"] != "0.00" {
		t.Fatalf("reconciliation = %v", r.body)
	}

	r = f.do(t, http.MethodGet, "/wallets/"+walletID, internal, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if r.body["balance"].(map[string]any)["amount"] != "75.00" {
		t.Fatalf("wallet = %v", r.body)
	}
}
