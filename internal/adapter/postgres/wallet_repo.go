package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type WalletRepository struct {
	db DBTX
}

func NewWalletRepository(db DBTX) *WalletRepository {
	return &WalletRepository{db: db}
}

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r *WalletRepository) Insert(ctx context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	_, err := r.db.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID.UUID(), s.PlayerID.UUID(), s.Balance.Currency().String(), s.Balance.Minor(),
		s.Version, s.CreatedAt, s.UpdatedAt,
	)
	if uniqueViolation(err, "wallets_player_currency_key") {
		return app.ErrWalletAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("insert wallet: %w", err)
	}
	return nil
}

// GetForUpdate locks the wallet row until the surrounding transaction ends.
func (r *WalletRepository) GetForUpdate(ctx context.Context, id wallet.ID) (*wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id.UUID())
}

func (r *WalletRepository) Get(ctx context.Context, id wallet.ID) (*wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id.UUID())
}

func (r *WalletRepository) FindByPlayer(ctx context.Context, player wallet.PlayerID, currency money.Currency) (*wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE player_id = $1 AND currency = $2`,
		player.UUID(), currency.String())
}

// UpdateBalance persists the new balance only if the stored version is still expectedVersion.
func (r *WalletRepository) UpdateBalance(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	s := w.Snapshot()
	tag, err := r.db.Exec(ctx,
		`UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $5`,
		s.ID.UUID(), s.Balance.Minor(), s.Version, s.UpdatedAt, expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("update wallet balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("wallet %s at version %d: %w", s.ID, expectedVersion, app.ErrConcurrentUpdate)
	}
	return nil
}

func (r *WalletRepository) get(ctx context.Context, sql string, args ...any) (*wallet.Wallet, error) {
	var (
		id, player         uuid.UUID
		currency           string
		balance, version   int64
		createdAt, updated time.Time
	)
	err := r.db.QueryRow(ctx, sql, args...).Scan(&id, &player, &currency, &balance, &version, &createdAt, &updated)
	if err != nil {
		return nil, notFound(err)
	}
	return rehydrateWallet(id, player, currency, balance, version, createdAt, updated)
}

func rehydrateWallet(id, player uuid.UUID, currency string, balance, version int64, createdAt, updatedAt time.Time) (*wallet.Wallet, error) {
	wid, err := wallet.NewID(id)
	if err != nil {
		return nil, err
	}
	pid, err := wallet.NewPlayerID(player)
	if err != nil {
		return nil, err
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, err
	}
	bal, err := money.FromMinor(balance, cur)
	if err != nil {
		return nil, err
	}
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID: wid, PlayerID: pid, Balance: bal, Version: version,
		CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("rehydrate wallet %s: %w", id, err)
	}
	return w, nil
}
