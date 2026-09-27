package event

import (
	"fmt"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// TransactionView describes the transaction in every wager event. Provider
// fields are omitted for internal transactions such as OPENING.
type TransactionView struct {
	TransactionID                  string    `json:"transactionId"`
	Origin                         string    `json:"origin"`
	Kind                           string    `json:"kind"`
	Status                         string    `json:"status"`
	WalletID                       string    `json:"walletId"`
	PlayerID                       string    `json:"playerId"`
	Money                          MoneyView `json:"money"`
	ProviderID                     string    `json:"providerId,omitempty"`
	ExternalTransactionID          string    `json:"externalTransactionId,omitempty"`
	RoundID                        string    `json:"roundId,omitempty"`
	GameID                         string    `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string    `json:"referenceTransactionId,omitempty"`
}

type WagerTransactionProcessedData struct {
	TransactionView
	Balance       MoneyView `json:"balance"`
	WalletVersion int64     `json:"walletVersion"`
}

type WagerTransactionRejectedData struct {
	TransactionView
	FailureCode string    `json:"failureCode"`
	Balance     MoneyView `json:"balance"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionView
	NextAttemptAt string `json:"nextAttemptAt"`
	Deadline      string `json:"deadline"`
}

type WalletBalanceChangedData struct {
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         MoneyView `json:"money"`
	BalanceBefore MoneyView `json:"balanceBefore"`
	BalanceAfter  MoneyView `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

func viewOfTransaction(tx *wagering.Transaction) TransactionView {
	v := TransactionView{
		TransactionID: tx.ID().String(),
		Origin:        string(tx.Origin()),
		Kind:          string(tx.Kind()),
		Status:        string(tx.Status()),
		WalletID:      tx.WalletID().String(),
		PlayerID:      tx.PlayerID().String(),
		Money:         viewOf(tx.Amount()),
	}
	if ext, ok := tx.External(); ok {
		v.ProviderID = ext.ProviderID
		v.ExternalTransactionID = ext.ExternalID
		v.RoundID = ext.RoundID
		v.GameID = ext.GameID
		v.ReferenceExternalTransactionID = ext.ReferenceExternalID
	}
	if ref, ok := tx.ReferenceID(); ok {
		v.ReferenceTransactionID = ref.String()
	}
	return v
}

func requireStatus(tx *wagering.Transaction, want wagering.Status) error {
	if tx == nil || tx.Status() != want {
		return fmt.Errorf("%w: transaction must be %s", ErrInvalidEvent, want)
	}
	return nil
}

func NewWagerTransactionProcessed(meta Meta, tx *wagering.Transaction) (Envelope[WagerTransactionProcessedData], error) {
	if err := requireStatus(tx, wagering.StatusProcessed); err != nil {
		return Envelope[WagerTransactionProcessedData]{}, err
	}
	r, _ := tx.Result()
	data := WagerTransactionProcessedData{
		TransactionView: viewOfTransaction(tx),
		Balance:         viewOf(r.Balance),
		WalletVersion:   r.WalletVersion,
	}
	return newEnvelope(meta, TypeWagerTransactionProcessed, 1,
		AggregateWagerTransaction, tx.ID().String(), tx.WalletID().String(), data)
}

func NewWagerTransactionRejected(meta Meta, tx *wagering.Transaction) (Envelope[WagerTransactionRejectedData], error) {
	if err := requireStatus(tx, wagering.StatusRejected); err != nil {
		return Envelope[WagerTransactionRejectedData]{}, err
	}
	r, _ := tx.Result()
	data := WagerTransactionRejectedData{
		TransactionView: viewOfTransaction(tx),
		FailureCode:     string(tx.FailureCode()),
		Balance:         viewOf(r.Balance),
	}
	return newEnvelope(meta, TypeWagerTransactionRejected, 1,
		AggregateWagerTransaction, tx.ID().String(), tx.WalletID().String(), data)
}

func NewWagerTransactionPendingReference(meta Meta, tx *wagering.Transaction) (Envelope[WagerTransactionPendingReferenceData], error) {
	if err := requireStatus(tx, wagering.StatusPendingReference); err != nil {
		return Envelope[WagerTransactionPendingReferenceData]{}, err
	}
	data := WagerTransactionPendingReferenceData{
		TransactionView: viewOfTransaction(tx),
		NextAttemptAt:   formatTime(tx.NextAttemptAt()),
		Deadline:        formatTime(tx.Deadline()),
	}
	return newEnvelope(meta, TypeWagerTransactionPendingReference, 1,
		AggregateWagerTransaction, tx.ID().String(), tx.WalletID().String(), data)
}

func NewWalletBalanceChanged(meta Meta, walletID wallet.ID, transactionID wagering.ID, mv wallet.Movement) (Envelope[WalletBalanceChangedData], error) {
	if walletID.IsZero() || transactionID.IsZero() || !mv.Amount.IsValid() || mv.VersionAfter < wallet.InitialVersion {
		return Envelope[WalletBalanceChangedData]{}, fmt.Errorf("%w: incomplete balance change", ErrInvalidEvent)
	}
	data := WalletBalanceChangedData{
		WalletID:      walletID.String(),
		TransactionID: transactionID.String(),
		Direction:     string(mv.Direction),
		Money:         viewOf(mv.Amount),
		BalanceBefore: viewOf(mv.BalanceBefore),
		BalanceAfter:  viewOf(mv.BalanceAfter),
		WalletVersion: mv.VersionAfter,
	}
	return newEnvelope(meta, TypeWalletBalanceChanged, 1,
		AggregateWallet, walletID.String(), walletID.String(), data)
}
