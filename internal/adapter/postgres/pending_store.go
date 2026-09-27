package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// PendingStore leases operations waiting for a reference. The lease is next_attempt_at itself:
// if the worker dies, the row becomes due again once the lease elapses.
type PendingStore struct {
	db DBTX
}

func NewPendingStore(db DBTX) *PendingStore {
	return &PendingStore{db: db}
}

func (s *PendingStore) ClaimDue(ctx context.Context, lease time.Duration, limit int) ([]app.PendingClaim, error) {
	rows, err := s.db.Query(ctx,
		`UPDATE wager_transactions t
		    SET next_attempt_at = now() + make_interval(secs => $1),
		        attempts = t.attempts + 1,
		        updated_at = GREATEST(t.updated_at, now())
		  WHERE t.id IN (
		        SELECT id FROM wager_transactions
		         WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= now()
		         ORDER BY next_attempt_at
		         LIMIT $2
		         FOR UPDATE SKIP LOCKED)
		 RETURNING t.id, t.wallet_id`,
		lease.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim pending transactions: %w", err)
	}
	defer rows.Close()

	var out []app.PendingClaim
	for rows.Next() {
		var id, walletID uuid.UUID
		if err := rows.Scan(&id, &walletID); err != nil {
			return nil, fmt.Errorf("scan pending claim: %w", err)
		}
		txID, err := wagering.NewID(id)
		if err != nil {
			return nil, err
		}
		wid, err := wallet.NewID(walletID)
		if err != nil {
			return nil, err
		}
		out = append(out, app.PendingClaim{TransactionID: txID, WalletID: wid})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending claims: %w", err)
	}
	return out, nil
}

// NudgeWaiting makes operations waiting for the given reference due immediately.
func (s *PendingStore) NudgeWaiting(ctx context.Context, providerID, referenceExternalID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE wager_transactions SET next_attempt_at = now()
		 WHERE status = 'PENDING_REFERENCE' AND provider_id = $1
		   AND reference_external_transaction_id = $2 AND next_attempt_at > now()`,
		providerID, referenceExternalID)
	if err != nil {
		return fmt.Errorf("nudge waiting transactions: %w", err)
	}
	return nil
}

func (s *PendingStore) CountWaiting(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count waiting transactions: %w", err)
	}
	return n, nil
}
