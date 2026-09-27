package wagering

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidField marks correctable input problems; they are never persisted.
	ErrInvalidField = errors.New("wagering: invalid field")
	// ErrReservedKind is returned when an external channel submits OPENING.
	ErrReservedKind = errors.New("wagering: kind is reserved for internal use")
	// ErrInvalidTransaction marks a snapshot or construction that violates invariants.
	ErrInvalidTransaction = errors.New("wagering: invalid transaction")
)

// FieldError identifies which input field is invalid.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("wagering: invalid %s: %s", e.Field, e.Reason)
}

func (e *FieldError) Unwrap() error { return ErrInvalidField }

func fieldError(field, reason string) error {
	return &FieldError{Field: field, Reason: reason}
}

// TransitionError is returned when a state change is not allowed.
type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("wagering: transition %s -> %s is not allowed", e.From, e.To)
}
