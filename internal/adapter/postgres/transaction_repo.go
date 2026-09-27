package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type TransactionRepository struct {
	db DBTX
}

func NewTransactionRepository(db DBTX) *TransactionRepository {
	return &TransactionRepository{db: db}
}

const transactionColumns = `id, origin, kind, status, channel, wallet_id, player_id, currency, amount_minor,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, reference_transaction_id, failure_code, failure_detail,
	result_balance_minor, result_wallet_version, attempts, next_attempt_at, reference_deadline_at,
	correlation_id, created_at, updated_at, completed_at`

func (r *TransactionRepository) Insert(ctx context.Context, t *wagering.Transaction) error {
	s := t.Snapshot()
	var ext wagering.External
	if s.External != nil {
		ext = *s.External
	}
	resultBalance, resultVersion := resultColumns(s.Result)
	_, err := r.db.Exec(ctx,
		`INSERT INTO wager_transactions (`+transactionColumns+`) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25, $26, $27, $28)`,
		s.ID.UUID(), string(s.Origin), string(s.Kind), string(s.Status), string(s.Channel),
		s.WalletID.UUID(), s.PlayerID.UUID(), s.Amount.Currency().String(), s.Amount.Minor(),
		nullString(ext.ProviderID), nullString(ext.ExternalID), nullString(ext.IdempotencyKey),
		nullString(ext.PayloadHash), nullString(ext.RoundID), nullString(ext.GameID),
		nullString(ext.ReferenceExternalID), referenceColumn(s.ReferenceID),
		nullString(string(s.FailureCode)), nullString(s.FailureDetail),
		resultBalance, resultVersion, s.Attempts, nullTime(s.NextAttemptAt), nullTime(s.Deadline),
		s.CorrelationID, s.CreatedAt, s.UpdatedAt, nullTime(s.CompletedAt),
	)
	if err != nil {
		return fmt.Errorf("insert wager transaction: %w", err)
	}
	return nil
}

// Update persists the mutable lifecycle columns; the database rejects changes to business fields.
func (r *TransactionRepository) Update(ctx context.Context, t *wagering.Transaction) error {
	s := t.Snapshot()
	resultBalance, resultVersion := resultColumns(s.Result)
	tag, err := r.db.Exec(ctx,
		`UPDATE wager_transactions SET
			status = $2, reference_transaction_id = $3, failure_code = $4, failure_detail = $5,
			result_balance_minor = $6, result_wallet_version = $7, attempts = $8,
			next_attempt_at = $9, reference_deadline_at = $10, updated_at = $11, completed_at = $12
		 WHERE id = $1`,
		s.ID.UUID(), string(s.Status), referenceColumn(s.ReferenceID),
		nullString(string(s.FailureCode)), nullString(s.FailureDetail),
		resultBalance, resultVersion, s.Attempts, nullTime(s.NextAttemptAt), nullTime(s.Deadline),
		s.UpdatedAt, nullTime(s.CompletedAt),
	)
	if err != nil {
		return fmt.Errorf("update wager transaction: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("wager transaction %s: %w", s.ID, app.ErrNotFound)
	}
	return nil
}

func (r *TransactionRepository) Get(ctx context.Context, id wagering.ID) (*wagering.Transaction, error) {
	return r.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1`, id.UUID())
}

func (r *TransactionRepository) GetForUpdate(ctx context.Context, id wagering.ID) (*wagering.Transaction, error) {
	return r.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1 FOR UPDATE`, id.UUID())
}

func (r *TransactionRepository) FindByIdempotencyKey(ctx context.Context, provider, key string) (*wagering.Transaction, error) {
	return r.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND idempotency_key = $2`, provider, key)
}

func (r *TransactionRepository) FindByExternalID(ctx context.Context, provider, externalID string) (*wagering.Transaction, error) {
	return r.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2`, provider, externalID)
}

func (r *TransactionRepository) HasProcessedReversal(ctx context.Context, referenceID wagering.ID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM wager_transactions
		WHERE reference_transaction_id = $1 AND kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED')`,
		referenceID.UUID()).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check reversal: %w", err)
	}
	return exists, nil
}

// ListWaitingFor returns transactions parked until the given external reference arrives.
func (r *TransactionRepository) ListWaitingFor(ctx context.Context, provider, referenceExternalID string) ([]*wagering.Transaction, error) {
	rows, err := r.db.Query(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND provider_id = $1 AND reference_external_transaction_id = $2
		ORDER BY created_at, id`, provider, referenceExternalID)
	if err != nil {
		return nil, fmt.Errorf("list waiting transactions: %w", err)
	}
	return collectTransactions(rows)
}

func (r *TransactionRepository) one(ctx context.Context, sql string, args ...any) (*wagering.Transaction, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query wager transaction: %w", err)
	}
	txs, err := collectTransactions(rows)
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, app.ErrNotFound
	}
	return txs[0], nil
}

func collectTransactions(rows pgx.Rows) ([]*wagering.Transaction, error) {
	defer rows.Close()
	var out []*wagering.Transaction
	for rows.Next() {
		t, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read wager transactions: %w", err)
	}
	return out, nil
}

func scanTransaction(row pgx.Row) (*wagering.Transaction, error) {
	var (
		id, walletID, playerID                           uuid.UUID
		origin, kind, status, channel, currency, corrID  string
		amount                                           int64
		provider, externalID, idemKey, hash, round, game *string
		refExternal, failureCode, failureDetail          *string
		refID                                            *uuid.UUID
		resultBalance, resultVersion                     *int64
		attempts                                         int
		nextAttempt, deadline, completed                 *time.Time
		createdAt, updatedAt                             time.Time
	)
	if err := row.Scan(&id, &origin, &kind, &status, &channel, &walletID, &playerID, &currency, &amount,
		&provider, &externalID, &idemKey, &hash, &round, &game, &refExternal, &refID,
		&failureCode, &failureDetail, &resultBalance, &resultVersion, &attempts,
		&nextAttempt, &deadline, &corrID, &createdAt, &updatedAt, &completed); err != nil {
		return nil, fmt.Errorf("scan wager transaction: %w", err)
	}

	s := wagering.Snapshot{
		FailureDetail: derefString(failureDetail),
		Attempts:      attempts,
		NextAttemptAt: derefTime(nextAttempt),
		Deadline:      derefTime(deadline),
		CorrelationID: corrID,
		CreatedAt:     createdAt.UTC(),
		UpdatedAt:     updatedAt.UTC(),
		CompletedAt:   derefTime(completed),
	}
	var err error
	if s.ID, err = wagering.NewID(id); err != nil {
		return nil, err
	}
	if s.Origin, err = wagering.ParseOrigin(origin); err != nil {
		return nil, err
	}
	if s.Kind, err = wagering.ParseKind(kind); err != nil {
		return nil, err
	}
	if s.Status, err = wagering.ParseStatus(status); err != nil {
		return nil, err
	}
	if s.Channel, err = wagering.ParseChannel(channel); err != nil {
		return nil, err
	}
	if s.WalletID, err = wallet.NewID(walletID); err != nil {
		return nil, err
	}
	if s.PlayerID, err = wallet.NewPlayerID(playerID); err != nil {
		return nil, err
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, err
	}
	if s.Amount, err = money.FromMinor(amount, cur); err != nil {
		return nil, err
	}
	if failureCode != nil {
		if s.FailureCode, err = wagering.ParseFailureCode(*failureCode); err != nil {
			return nil, err
		}
	}
	if provider != nil {
		s.External = &wagering.External{
			ProviderID:          *provider,
			ExternalID:          derefString(externalID),
			IdempotencyKey:      derefString(idemKey),
			PayloadHash:         derefString(hash),
			RoundID:             derefString(round),
			GameID:              derefString(game),
			ReferenceExternalID: derefString(refExternal),
		}
	}
	if refID != nil {
		ref, err := wagering.NewID(*refID)
		if err != nil {
			return nil, err
		}
		s.ReferenceID = &ref
	}
	if resultBalance != nil && resultVersion != nil {
		bal, err := money.FromMinor(*resultBalance, cur)
		if err != nil {
			return nil, err
		}
		s.Result = &wagering.Result{Balance: bal, WalletVersion: *resultVersion}
	}

	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, fmt.Errorf("rehydrate wager transaction %s: %w", id, err)
	}
	return t, nil
}

func resultColumns(r *wagering.Result) (*int64, *int64) {
	if r == nil {
		return nil, nil
	}
	balance, version := r.Balance.Minor(), r.WalletVersion
	return &balance, &version
}

func referenceColumn(id *wagering.ID) *uuid.UUID {
	if id == nil {
		return nil
	}
	u := id.UUID()
	return &u
}
