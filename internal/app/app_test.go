package app

import (
	"errors"
	"testing"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func TestPendingPolicyDelay(t *testing.T) {
	p := PendingPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for attempts, w := range want {
		if got := p.Delay(attempts); got != w*time.Millisecond {
			t.Errorf("Delay(%d) = %v, want %v", attempts, got, w*time.Millisecond)
		}
	}
	if got := p.Delay(1000); got != time.Second {
		t.Errorf("large attempts not capped: %v", got)
	}

	p.Jitter = 0.2
	for range 200 {
		d := p.Delay(2)
		if d < 320*time.Millisecond || d > 480*time.Millisecond {
			t.Fatalf("jittered delay %v outside ±20%% of 400ms", d)
		}
	}
}

func TestLedgerCursor(t *testing.T) {
	for _, v := range []int64{0, 1, 42, 1 << 40} {
		got, err := decodeCursor(encodeCursor(v))
		if err != nil || got != v {
			t.Fatalf("round trip %d: %d %v", v, got, err)
		}
	}
	if v, err := decodeCursor(""); err != nil || v != 0 {
		t.Fatalf("empty cursor: %d %v", v, err)
	}
	for _, bad := range []string{"%%%", "bm90LWpzb24", "eyJ2IjotMX0"} {
		if _, err := decodeCursor(bad); !errors.Is(err, wagering.ErrInvalidField) {
			t.Errorf("cursor %q: %v", bad, err)
		}
	}
}
