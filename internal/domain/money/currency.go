package money

import "fmt"

// Currency is an ISO 4217 code restricted to currencies with two minor digits,
// which is the only scale the service supports.
type Currency struct {
	code string
}

var supportedCurrencies = map[string]struct{}{
	"BRL": {},
	"EUR": {},
	"USD": {},
}

func ParseCurrency(code string) (Currency, error) {
	if _, ok := supportedCurrencies[code]; !ok {
		return Currency{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, code)
	}
	return Currency{code: code}, nil
}

func (c Currency) IsZero() bool { return c.code == "" }

func (c Currency) String() string { return c.code }

func (c Currency) Equal(other Currency) bool { return c.code == other.code }
