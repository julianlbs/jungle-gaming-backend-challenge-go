package money

import "errors"

var (
	ErrInvalidAmount       = errors.New("money: invalid amount")
	ErrUnsupportedCurrency = errors.New("money: unsupported currency")
	ErrCurrencyMismatch    = errors.New("money: currency mismatch")
	ErrOverflow            = errors.New("money: amount out of range")
	ErrUninitialized       = errors.New("money: uninitialized value")
)
