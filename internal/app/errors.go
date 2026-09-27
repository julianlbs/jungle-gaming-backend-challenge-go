package app

import "errors"

// ErrUnavailable means a dependency failed transiently and the operation was not applied;
// callers may retry with the same idempotency key.
var ErrUnavailable = errors.New("service temporarily unavailable")
