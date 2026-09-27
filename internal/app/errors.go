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
)
