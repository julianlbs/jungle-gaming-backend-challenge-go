package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth/authtest"
)

func TestVerify(t *testing.T) {
	issuer := authtest.NewTokenIssuer(t)
	v := auth.NewVerifier(context.Background(), auth.Config{
		Issuer: authtest.Issuer, JWKSURL: issuer.JWKSURL(), Audience: authtest.Audience,
	})
	ctx := context.Background()

	p, err := v.Verify(ctx, issuer.Token(t, authtest.Claims{
		Subject: "svc-a", ClientID: "provider-a", ProviderID: "provider-a", Scope: "wagering:write wagering:read",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p.ClientID != "provider-a" || p.ProviderID != "provider-a" || p.Subject != "svc-a" ||
		!p.HasScope(auth.ScopeWageringWrite) || p.HasScope(auth.ScopeWalletsWrite) {
		t.Fatalf("principal = %+v", p)
	}

	for name, token := range map[string]string{
		"expired":        issuer.Token(t, authtest.Claims{Expired: true}),
		"wrong issuer":   issuer.Token(t, authtest.Claims{Issuer: "http://evil/realms/wallet"}),
		"wrong audience": issuer.Token(t, authtest.Claims{Audience: []string{"other-api"}}),
		"unknown key":    issuer.ForeignToken(t, authtest.Claims{}),
		"garbage":        "not-a-jwt",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(ctx, token); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Fatal("empty context has a principal")
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ClientID: "c"})
	if p, ok := auth.PrincipalFrom(ctx); !ok || p.ClientID != "c" {
		t.Fatalf("principal = %+v %v", p, ok)
	}
}
