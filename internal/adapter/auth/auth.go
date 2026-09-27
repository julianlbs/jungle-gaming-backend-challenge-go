// Package auth verifies OIDC access tokens issued by the identity provider.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Scopes granted to API clients.
const (
	ScopeWageringWrite    = "wagering:write"
	ScopeWageringRead     = "wagering:read"
	ScopeWageringReadAll  = "wagering:read-all"
	ScopeWalletsRead      = "wallets:read"
	ScopeWalletsWrite     = "wallets:write"
	ScopeWalletsReconcile = "wallets:reconcile"
)

var ErrInvalidToken = errors.New("invalid access token")

// Principal is the authenticated caller.
type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Scopes     []string
}

func (p Principal) HasScope(scope string) bool { return slices.Contains(p.Scopes, scope) }

type Config struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier validates tokens locally against the JWKS. Keys are cached and refetched when a
// token carries an unknown key id, which covers key rotation. The JWKS URL may differ from the
// issuer so that containers can reach the provider on an internal hostname.
func NewVerifier(ctx context.Context, cfg Config) *Verifier {
	keys := oidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
	return &Verifier{verifier: oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}
}

type claims struct {
	Scope      string `json:"scope"`
	AZP        string `json:"azp"`
	ClientID   string `json:"client_id"`
	ProviderID string `json:"provider_id"`
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Principal, error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return Principal{}, fmt.Errorf("%w: claims: %w", ErrInvalidToken, err)
	}
	clientID := c.AZP
	if clientID == "" {
		clientID = c.ClientID
	}
	return Principal{
		Subject:    tok.Subject,
		ClientID:   clientID,
		ProviderID: c.ProviderID,
		Scopes:     strings.Fields(c.Scope),
	}, nil
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
