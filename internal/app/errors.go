package app

import "errors"

var (
	// ErrUnavailable means a dependency failed transiently and the operation was not applied;
	// callers may retry with the same idempotency key.
	ErrUnavailable = errors.New("service temporarily unavailable")

	ErrNotFound            = errors.New("not found")
	ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")
	// ErrConcurrentUpdate means a row changed between being read and written.
	ErrConcurrentUpdate = errors.New("concurrent update")

	// Correctable wager errors: nothing is persisted and the caller may fix and resubmit.
	ErrWalletNotFound = errors.New("wallet not found")
	ErrWalletMismatch = errors.New("wallet does not belong to the player or currency")

	// Idempotency conflicts: the request collides with a different, already recorded operation.
	ErrIdempotencyKeyReused        = errors.New("idempotency key reused with a different payload")
	ErrExternalTransactionConflict = errors.New("external transaction id already used with another idempotency key")
)
