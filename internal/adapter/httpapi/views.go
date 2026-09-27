package httpapi

import (
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
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

type reconciliationResponse struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     moneyBody `json:"storedBalance"`
	CalculatedBalance moneyBody `json:"calculatedBalance"`
	Difference        moneyBody `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int64     `json:"checkedEntries"`
	CheckedAt         string    `json:"checkedAt"`
}
