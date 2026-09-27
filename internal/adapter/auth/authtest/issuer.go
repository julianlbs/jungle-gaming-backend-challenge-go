// Package authtest issues signed tokens from a local JWKS for tests.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	Issuer   = "http://localhost:8080/realms/wallet"
	Audience = "wallet-api"
)

// TokenIssuer signs RS256 tokens and serves the matching JWKS.
type TokenIssuer struct {
	Server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
}

func NewTokenIssuer(t testing.TB) *TokenIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ti := &TokenIssuer{key: key, keyID: "test-key"}
	ti.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: ti.keyID, Algorithm: string(jose.RS256), Use: "sig",
		}}})
	}))
	t.Cleanup(ti.Server.Close)
	return ti
}

func (ti *TokenIssuer) JWKSURL() string { return ti.Server.URL }

// Claims describes a token; zero values fall back to a valid provider-a token.
type Claims struct {
	Subject    string
	ClientID   string
	ProviderID string
	Scope      string
	Issuer     string
	Audience   []string
	ExpiresIn  time.Duration
	Expired    bool
}

func (ti *TokenIssuer) Token(t testing.TB, c Claims) string {
	t.Helper()
	return ti.sign(t, ti.key, ti.keyID, c)
}

// ForeignToken is signed by a key the JWKS does not publish.
func (ti *TokenIssuer) ForeignToken(t testing.TB, c Claims) string {
	t.Helper()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return ti.sign(t, other, ti.keyID, c)
}

func (ti *TokenIssuer) sign(t testing.TB, key *rsa.PrivateKey, kid string, c Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exp := now.Add(5 * time.Minute)
	if c.ExpiresIn != 0 {
		exp = now.Add(c.ExpiresIn)
	}
	if c.Expired {
		exp = now.Add(-time.Minute)
	}
	iss := c.Issuer
	if iss == "" {
		iss = Issuer
	}
	aud := c.Audience
	if aud == nil {
		aud = []string{Audience}
	}
	std := jwt.Claims{
		Issuer: iss, Subject: c.Subject, Audience: jwt.Audience(aud),
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), Expiry: jwt.NewNumericDate(exp),
	}
	extra := map[string]any{"azp": c.ClientID, "scope": c.Scope}
	if c.ProviderID != "" {
		extra["provider_id"] = c.ProviderID
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(extra).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
