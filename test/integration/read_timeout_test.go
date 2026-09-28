//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

func TestPoolSetsStatementTimeout(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		URL:              withDatabase(env.appURL, db.Name),
		MaxConns:         1,
		StatementTimeout: 2500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var got string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2500ms" && got != "2.5s" {
		t.Fatalf("statement_timeout = %q", got)
	}
}

func TestReadModelQueryDeadline(t *testing.T) {
	db := newTestDB(t)
	id, err := wallet.NewID(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = postgres.NewReadModel(db.App).WithQueryTimeout(-time.Millisecond).GetWallet(context.Background(), id)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "begin read") {
		t.Fatalf("err = %v", err)
	}
}
