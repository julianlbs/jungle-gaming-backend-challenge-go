//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/httpapi"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

func keycloakIssuer() string {
	return getenv("TEST_KEYCLOAK_URL", "http://localhost:8080") + "/realms/wallet"
}

// newKeycloakAPI serves the API with the verifier configured as in production.
func newKeycloakAPI(t *testing.T) *apiFixture {
	f := newAppFixture(t)
	issuer := keycloakIssuer()
	api := httpapi.NewAPI(httpapi.Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: metrics.New(),
		Verifier: auth.NewVerifier(context.Background(), auth.Config{
			Issuer: issuer, JWKSURL: issuer + "/protocol/openid-connect/certs", Audience: "wallet-api",
		}),
		Wallets:   f.opener,
		Wagers:    f.processor,
		Queries:   app.NewQueries(postgres.NewReadModel(f.db.App), app.SystemClock{}),
		Readiness: httpapi.NewReadiness(httpapi.ReadinessCheck{Name: "database", Check: f.db.App.Ping}),
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &apiFixture{server: srv}
}

func keycloakToken(t *testing.T, client string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {client + "-local-secret"}}
	req := must(http.NewRequestWithContext(t.Context(), http.MethodPost, keycloakIssuer()+"/protocol/openid-connect/token", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token for %s: %v", client, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d, %v", client, resp.StatusCode, err)
	}
	return body.AccessToken
}

func tamper(token string) string {
	dot := strings.LastIndex(token, ".")
	sig := []byte(token[dot+1:])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	return token[:dot+1] + string(sig)
}

func TestKeycloakCredentials(t *testing.T) {
	f := newKeycloakAPI(t)
	shortLived := keycloakToken(t, "provider-a-short-lived")
	providerA := keycloakToken(t, "provider-a")
	providerB := keycloakToken(t, "provider-b")
	backoffice := keycloakToken(t, "wallet-backoffice")
	unrelated := keycloakToken(t, "unrelated-service")

	player := uuid.NewString()
	r := f.do(t, http.MethodPost, "/wallets", backoffice, nil, map[string]any{"playerId": player, "initialBalance": brlBody("100.00")})
	expectStatus(t, r, http.StatusCreated, "")
	walletID := r.body["id"].(string)

	bet := func(ext string) map[string]any { return wagerBody(walletID, player, ext, "BET", "10.00") }
	post := func(token, ext string) apiResponse {
		return f.do(t, http.MethodPost, "/wagering/transactions", token, map[string]string{"Idempotency-Key": "provider-a:" + ext}, bet(ext))
	}

	r = post(shortLived, "kc-short-fresh")
	expectStatus(t, r, http.StatusOK, "")

	// provider-a-short-lived issues tokens valid for two seconds.
	time.Sleep(3 * time.Second)
	for name, tok := range map[string]string{
		"missing":            "",
		"tampered signature": tamper(providerA),
		"expired":            shortLived,
		"another audience":   unrelated,
		"not a jwt":          "invalid",
	} {
		t.Run(name, func(t *testing.T) {
			r := post(tok, "kc-"+strings.ReplaceAll(name, " ", "-"))
			expectStatus(t, r, http.StatusUnauthorized, "UNAUTHENTICATED")
			if r.header.Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate")
			}
		})
	}

	expectStatus(t, post(backoffice, "kc-backoffice"), http.StatusForbidden, "FORBIDDEN")
	expectStatus(t, post(providerB, "kc-provider-b"), http.StatusForbidden, "PROVIDER_MISMATCH")

	r = post(providerA, "kc-bet")
	expectStatus(t, r, http.StatusOK, "")
	txID := r.body["transactionId"].(string)
	expectStatus(t, f.do(t, http.MethodGet, "/wagering/transactions/"+txID, providerB, nil, nil), http.StatusNotFound, "NOT_FOUND")
	expectStatus(t, f.do(t, http.MethodGet, "/wagering/transactions/"+txID, providerA, nil, nil), http.StatusOK, "")

	r = f.do(t, http.MethodGet, "/wallets/"+walletID, backoffice, nil, nil)
	expectStatus(t, r, http.StatusOK, "")
	if got := r.body["balance"].(map[string]any)["amount"]; got != "80.00" {
		t.Fatalf("balance = %v", got)
	}
}
