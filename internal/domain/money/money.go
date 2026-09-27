package money

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

const minorPerUnit = 100

var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.([0-9]{1,2}))?$`)

// Money is an immutable amount in minor units (cents) with its currency.
// The representable range is that of int64: ±92,233,720,368,547,758.07.
type Money struct {
	minor    int64
	currency Currency
}

// Parse reads a non-negative decimal string with at most two fractional digits.
// Signs, exponents, whitespace, leading zeros and NaN/Infinity are rejected;
// "25" and "25.5" are accepted and normalized to "25.00" and "25.50".
func Parse(amount string, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrUninitialized
	}
	parts := amountPattern.FindStringSubmatch(amount)
	if parts == nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	whole, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	var frac int64
	if f := parts[2]; f != "" {
		frac, _ = strconv.ParseInt(f, 10, 64)
		if len(f) == 1 {
			frac *= 10
		}
	}
	if whole > (math.MaxInt64-frac)/minorPerUnit {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	return Money{minor: whole*minorPerUnit + frac, currency: currency}, nil
}

// FromMinor builds a value from minor units, e.g. when reading persisted data.
func FromMinor(minor int64, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrUninitialized
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency Currency) (Money, error) {
	return FromMinor(0, currency)
}

func (m Money) IsValid() bool { return !m.currency.IsZero() }

func (m Money) Minor() int64 { return m.minor }

func (m Money) Currency() Currency { return m.currency }

func (m Money) IsZero() bool { return m.minor == 0 }

func (m Money) IsPositive() bool { return m.minor > 0 }

func (m Money) IsNegative() bool { return m.minor < 0 }

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	sum := m.minor + other.minor
	if (other.minor > 0 && sum < m.minor) || (other.minor < 0 && sum > m.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: sum, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	diff := m.minor - other.minor
	if (other.minor > 0 && diff > m.minor) || (other.minor < 0 && diff < m.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: diff, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.IsValid() {
		return Money{}, ErrUninitialized
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Cmp returns -1, 0 or 1 when m is less than, equal to or greater than other.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether both values have the same amount and currency.
func (m Money) Equal(other Money) bool {
	return m.minor == other.minor && m.currency.Equal(other.currency)
}

// String renders the amount with exactly two decimals, e.g. "25.00" or "-0.05".
func (m Money) String() string {
	sign := ""
	abs := uint64(m.minor)
	if m.minor < 0 {
		sign = "-"
		abs = uint64(-(m.minor + 1)) + 1
	}
	return fmt.Sprintf("%s%d.%02d", sign, abs/minorPerUnit, abs%minorPerUnit)
}

type jsonMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsValid() {
		return nil, ErrUninitialized
	}
	return json.Marshal(jsonMoney{Amount: m.String(), Currency: m.currency.String()})
}

func (m Money) compatible(other Money) error {
	if !m.IsValid() || !other.IsValid() {
		return ErrUninitialized
	}
	if !m.currency.Equal(other.currency) {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}
