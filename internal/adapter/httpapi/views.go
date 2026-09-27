package httpapi

import (
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyView(m money.Money) moneyBody {
	return moneyBody{Amount: m.String(), Currency: m.Currency().String()}
}

func timeView(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

type walletResponse struct {
	ID        string    `json:"id"`
	PlayerID  string    `json:"playerId"`
	Balance   moneyBody `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt string    `json:"createdAt"`
	UpdatedAt string    `json:"updatedAt"`
}

func walletView(w *wallet.Wallet) walletResponse {
	return walletResponse{
		ID:        w.ID().String(),
		PlayerID:  w.PlayerID().String(),
		Balance:   moneyView(w.Balance()),
		Version:   w.Version(),
		CreatedAt: timeView(w.CreatedAt()),
		UpdatedAt: timeView(w.UpdatedAt()),
	}
}

type ledgerEntryResponse struct {
	ID            string    `json:"id"`
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         moneyBody `json:"money"`
	BalanceBefore moneyBody `json:"balanceBefore"`
	BalanceAfter  moneyBody `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
	CreatedAt     string    `json:"createdAt"`
}

func ledgerEntryView(e wallet.LedgerEntry) ledgerEntryResponse {
	return ledgerEntryResponse{
		ID:            e.ID().String(),
		WalletID:      e.WalletID().String(),
		TransactionID: e.TransactionID().String(),
		Direction:     string(e.Direction()),
		Money:         moneyView(e.Amount()),
		BalanceBefore: moneyView(e.BalanceBefore()),
		BalanceAfter:  moneyView(e.BalanceAfter()),
		WalletVersion: e.WalletVersion(),
		CreatedAt:     timeView(e.CreatedAt()),
	}
}

type ledgerPageResponse struct {
	Items      []ledgerEntryResponse `json:"items"`
	NextCursor *string               `json:"nextCursor"`
}

type wagerResultResponse struct {
	TransactionID    string     `json:"transactionId"`
	Status           string     `json:"status"`
	Balance          *moneyBody `json:"balance,omitempty"`
	FailureCode      string     `json:"failureCode,omitempty"`
	NextAttemptAt    string     `json:"nextAttemptAt,omitempty"`
	IdempotentReplay bool       `json:"idempotentReplay"`
}

func wagerResultView(t *wagering.Transaction, replay bool) wagerResultResponse {
	resp := wagerResultResponse{
		TransactionID:    t.ID().String(),
		Status:           string(t.Status()),
		FailureCode:      string(t.FailureCode()),
		IdempotentReplay: replay,
	}
	if res, ok := t.Result(); ok {
		b := moneyView(res.Balance)
		resp.Balance = &b
	}
	if t.Status() == wagering.StatusPendingReference || t.Status() == wagering.StatusPending {
		resp.NextAttemptAt = optionalTime(t.NextAttemptAt())
	}
	return resp
}

type transactionResponse struct {
	ID                             string     `json:"id"`
	Origin                         string     `json:"origin"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	Channel                        string     `json:"channel"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	Money                          moneyBody  `json:"money"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string     `json:"referenceTransactionId,omitempty"`
	FailureCode                    string     `json:"failureCode,omitempty"`
	FailureDetail                  string     `json:"failureDetail,omitempty"`
	Balance                        *moneyBody `json:"balance,omitempty"`
	WalletVersion                  *int64     `json:"walletVersion,omitempty"`
	Attempts                       int        `json:"attempts"`
	NextAttemptAt                  string     `json:"nextAttemptAt,omitempty"`
	Deadline                       string     `json:"deadline,omitempty"`
	CorrelationID                  string     `json:"correlationId,omitempty"`
	CreatedAt                      string     `json:"createdAt"`
	UpdatedAt                      string     `json:"updatedAt"`
	CompletedAt                    string     `json:"completedAt,omitempty"`
}

func transactionView(t *wagering.Transaction) transactionResponse {
	resp := transactionResponse{
		ID:            t.ID().String(),
		Origin:        string(t.Origin()),
		Kind:          string(t.Kind()),
		Status:        string(t.Status()),
		Channel:       string(t.Channel()),
		WalletID:      t.WalletID().String(),
		PlayerID:      t.PlayerID().String(),
		Money:         moneyView(t.Amount()),
		FailureCode:   string(t.FailureCode()),
		FailureDetail: t.FailureDetail(),
		Attempts:      t.Attempts(),
		NextAttemptAt: optionalTime(t.NextAttemptAt()),
		Deadline:      optionalTime(t.Deadline()),
		CorrelationID: t.CorrelationID(),
		CreatedAt:     timeView(t.CreatedAt()),
		UpdatedAt:     timeView(t.UpdatedAt()),
		CompletedAt:   optionalTime(t.CompletedAt()),
	}
	if ext, ok := t.External(); ok {
		resp.ProviderID = ext.ProviderID
		resp.ExternalTransactionID = ext.ExternalID
		resp.RoundID = ext.RoundID
		resp.GameID = ext.GameID
		resp.ReferenceExternalTransactionID = ext.ReferenceExternalID
	}
	if ref, ok := t.ReferenceID(); ok {
		resp.ReferenceTransactionID = ref.String()
	}
	if res, ok := t.Result(); ok {
		b := moneyView(res.Balance)
		v := res.WalletVersion
		resp.Balance, resp.WalletVersion = &b, &v
	}
	return resp
}

func optionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return timeView(t)
}

type reconciliationResponse struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     moneyBody `json:"storedBalance"`
	CalculatedBalance moneyBody `json:"calculatedBalance"`
	Difference        moneyBody `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int64     `json:"checkedEntries"`
	CheckedAt         string    `json:"checkedAt"`
}
