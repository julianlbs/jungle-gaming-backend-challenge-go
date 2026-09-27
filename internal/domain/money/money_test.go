package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func currency(t testing.TB, code string) Currency {
	t.Helper()
	c, err := ParseCurrency(code)
	if err != nil {
		t.Fatalf("ParseCurrency(%q): %v", code, err)
	}
	return c
}

func minor(t testing.TB, v int64, code string) Money {
	t.Helper()
	m, err := FromMinor(v, currency(t, code))
	if err != nil {
		t.Fatalf("FromMinor: %v", err)
	}
	return m
}

func TestParseCurrency(t *testing.T) {
	for _, code := range []string{"BRL", "USD", "EUR"} {
		if _, err := ParseCurrency(code); err != nil {
			t.Errorf("ParseCurrency(%q) unexpected error: %v", code, err)
		}
	}
	for _, code := range []string{"", "brl", "BR", "BRLL", "JPY", "XXX", " BRL"} {
		if _, err := ParseCurrency(code); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("ParseCurrency(%q) = %v, want ErrUnsupportedCurrency", code, err)
		}
	}
}

func TestParse(t *testing.T) {
	brl := currency(t, "BRL")
	valid := []struct {
		in    string
		minor int64
		out   string
	}{
		{"0", 0, "0.00"},
		{"0.00", 0, "0.00"},
		{"0.01", 1, "0.01"},
		{"25", 2500, "25.00"},
		{"25.5", 2550, "25.50"},
		{"25.00", 2500, "25.00"},
		{"1000.99", 100099, "1000.99"},
		{"92233720368547758.07", math.MaxInt64, "92233720368547758.07"},
	}
	for _, tc := range valid {
		m, err := Parse(tc.in, brl)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if m.Minor() != tc.minor || m.String() != tc.out {
			t.Errorf("Parse(%q) = %d (%s), want %d (%s)", tc.in, m.Minor(), m, tc.minor, tc.out)
		}
	}

	invalid := []string{
		"", " ", "25.001", "1.", ".5", "-1.00", "+1.00", "01.00", "00", "1e3", "1E3",
		"NaN", "nan", "Infinity", "-Infinity", "Inf", "0x10", "1,00", " 1.00", "1.00 ", "1_000.00",
		"١٢", "25.0O",
	}
	for _, in := range invalid {
		if _, err := Parse(in, brl); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidAmount", in, err)
		}
	}

	overflow := []string{"92233720368547758.08", "92233720368547759", "99999999999999999999999"}
	for _, in := range overflow {
		if _, err := Parse(in, brl); !errors.Is(err, ErrOverflow) {
			t.Errorf("Parse(%q) = %v, want ErrOverflow", in, err)
		}
	}

	if _, err := Parse("1.00", Currency{}); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Parse with zero currency = %v, want ErrUninitialized", err)
	}
}

func TestArithmetic(t *testing.T) {
	a := minor(t, 10000, "BRL")
	b := minor(t, 8000, "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.Minor() != 18000 {
		t.Fatalf("Add = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.String() != "-20.00" || !diff.IsNegative() {
		t.Fatalf("Sub = %v, %v", diff, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.Minor() != -10000 {
		t.Fatalf("Neg = %v, %v", neg, err)
	}
	if c, err := a.Cmp(b); err != nil || c != 1 {
		t.Fatalf("Cmp(a, b) = %d, %v", c, err)
	}
	if c, err := b.Cmp(a); err != nil || c != -1 {
		t.Fatalf("Cmp(b, a) = %d, %v", c, err)
	}
	if c, err := a.Cmp(a); err != nil || c != 0 {
		t.Fatalf("Cmp(a, a) = %d, %v", c, err)
	}
	if a.Minor() != 10000 || b.Minor() != 8000 {
		t.Fatal("operands must remain unchanged")
	}
}

func TestOverflow(t *testing.T) {
	maxV := minor(t, math.MaxInt64, "BRL")
	minV := minor(t, math.MinInt64, "BRL")
	one := minor(t, 1, "BRL")
	negOne := minor(t, -1, "BRL")

	cases := []struct {
		name string
		op   func() (Money, error)
	}{
		{"max+1", func() (Money, error) { return maxV.Add(one) }},
		{"min-1", func() (Money, error) { return minV.Sub(one) }},
		{"min+(-1)", func() (Money, error) { return minV.Add(negOne) }},
		{"max-(-1)", func() (Money, error) { return maxV.Sub(negOne) }},
		{"-min", func() (Money, error) { return minV.Neg() }},
		{"0-min", func() (Money, error) { return minor(t, 0, "BRL").Sub(minV) }},
	}
	for _, tc := range cases {
		if _, err := tc.op(); !errors.Is(err, ErrOverflow) {
			t.Errorf("%s = %v, want ErrOverflow", tc.name, err)
		}
	}

	if got, err := maxV.Sub(maxV); err != nil || !got.IsZero() {
		t.Errorf("max-max = %v, %v", got, err)
	}
	if got, err := minV.Sub(negOne); err != nil || got.Minor() != math.MinInt64+1 {
		t.Errorf("min-(-1) = %v, %v", got, err)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	brl := minor(t, 100, "BRL")
	usd := minor(t, 100, "USD")

	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add = %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub = %v", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Cmp = %v", err)
	}
	if brl.Equal(usd) {
		t.Error("Equal must consider currency")
	}
}

func TestUninitialized(t *testing.T) {
	var zero Money
	valid := minor(t, 100, "BRL")

	if zero.IsValid() {
		t.Error("zero value must be invalid")
	}
	if _, err := zero.Add(valid); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Add = %v", err)
	}
	if _, err := valid.Sub(zero); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Sub = %v", err)
	}
	if _, err := zero.Neg(); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Neg = %v", err)
	}
	if _, err := zero.Cmp(valid); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Cmp = %v", err)
	}
	if _, err := json.Marshal(zero); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Marshal = %v", err)
	}
	if _, err := FromMinor(1, Currency{}); !errors.Is(err, ErrUninitialized) {
		t.Errorf("FromMinor = %v", err)
	}
	if _, err := Zero(Currency{}); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Zero = %v", err)
	}
}

func TestZero(t *testing.T) {
	z, err := Zero(currency(t, "BRL"))
	if err != nil || !z.IsZero() || z.String() != "0.00" || z.Currency().String() != "BRL" {
		t.Fatalf("Zero = %v, %v", z, err)
	}
}

func TestString(t *testing.T) {
	cases := map[int64]string{
		0:             "0.00",
		5:             "0.05",
		-5:            "-0.05",
		100:           "1.00",
		-12345:        "-123.45",
		math.MinInt64: "-92233720368547758.08",
	}
	for v, want := range cases {
		if got := minor(t, v, "BRL").String(); got != want {
			t.Errorf("String(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	b, err := json.Marshal(minor(t, 2500, "BRL"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("Marshal = %s", b)
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"0", "0.01", "25.5", "1e3", "NaN", "-1", "92233720368547758.07", "92233720368547758.08"} {
		f.Add(seed)
	}
	brl := currency(f, "BRL")
	f.Fuzz(func(t *testing.T, in string) {
		m, err := Parse(in, brl)
		if err != nil {
			return
		}
		if m.IsNegative() {
			t.Fatalf("Parse(%q) produced negative %v", in, m)
		}
		again, err := Parse(m.String(), brl)
		if err != nil || !again.Equal(m) {
			t.Fatalf("round trip of %q failed: %v, %v", in, again, err)
		}
	})
}
