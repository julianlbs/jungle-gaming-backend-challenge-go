package wallet

import "errors"

var (
	ErrInvalidID          = errors.New("wallet: invalid identifier")
	ErrInvalidWallet      = errors.New("wallet: invalid wallet state")
	ErrInvalidAmount      = errors.New("wallet: movement amount must be positive")
	ErrCurrencyMismatch   = errors.New("wallet: movement currency differs from wallet currency")
	ErrInsufficientFunds  = errors.New("wallet: insufficient funds")
	ErrInvalidDirection   = errors.New("wallet: invalid ledger direction")
	ErrInconsistentEntry  = errors.New("wallet: ledger entry balances do not match its movement")
	ErrInvalidLedgerEntry = errors.New("wallet: invalid ledger entry")
)
