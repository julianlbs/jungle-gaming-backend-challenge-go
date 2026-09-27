package wagering

import "fmt"

// FailureCode is the stable reason persisted with REJECTED and FAILED transactions.
// Correctable input problems are never persisted and are reported as errors instead.
type FailureCode string

const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReferenceAmountMismatch   FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	FailureReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	FailureReferenceAlreadyReversed  FailureCode = "REFERENCE_ALREADY_REVERSED"
	FailureProcessingFailed          FailureCode = "PROCESSING_FAILED"
)

var rejectionCodes = map[FailureCode]struct{}{
	FailureInsufficientFunds:         {},
	FailureReversalInsufficientFunds: {},
	FailureReferenceNotFound:         {},
	FailureReferenceNotProcessed:     {},
	FailureReferenceMismatch:         {},
	FailureReferenceAmountMismatch:   {},
	FailureReferenceKindNotAllowed:   {},
	FailureReferenceAlreadyReversed:  {},
}

func ParseFailureCode(s string) (FailureCode, error) {
	c := FailureCode(s)
	if c.IsRejection() || c.IsFailure() {
		return c, nil
	}
	return "", fmt.Errorf("%w: failure code %q", ErrInvalidField, s)
}

// IsRejection reports whether the code is a definitive business rejection.
func (c FailureCode) IsRejection() bool {
	_, ok := rejectionCodes[c]
	return ok
}

// IsFailure reports whether the code records a permanent infrastructure failure.
func (c FailureCode) IsFailure() bool { return c == FailureProcessingFailed }
