package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func TestObservationClassifiesErrors(t *testing.T) {
	cmd := WagerCommand{Channel: wagering.ChannelSQS, Kind: "BET"}
	cases := []struct {
		err                 error
		status, conflict    string
		concurrencyConflict bool
	}{
		{ErrIdempotencyKeyReused, ResultConflict, ConflictKeyReused, false},
		{ErrExternalTransactionConflict, ResultConflict, ConflictExternalID, false},
		{ErrMessageConflict, ResultConflict, ConflictMessageReused, false},
		{fmt.Errorf("wallet: %w", ErrConcurrentUpdate), ResultConcurrent, "", true},
		{fmt.Errorf("%w: lock timeout", ErrUnavailable), ResultUnavailable, "", false},
		{ErrWalletNotFound, ResultInvalid, "", false},
		{errors.New("boom"), ResultError, "", false},
	}
	for _, c := range cases {
		o := observation(cmd, WagerOutcome{}, false, c.err, 0)
		if o.Status != c.status || o.Conflict != c.conflict || o.ConcurrencyConflict != c.concurrencyConflict ||
			o.Channel != "SQS" || o.Kind != "BET" || o.Replay {
			t.Errorf("%v: %+v", c.err, o)
		}
	}
}

func TestObservationBoundsLabels(t *testing.T) {
	o := observation(WagerCommand{Channel: "carrier-pigeon", Kind: "anything-a-caller-sends"}, WagerOutcome{}, false, ErrWalletNotFound, 0)
	if o.Channel != unknownKind || o.Kind != unknownKind {
		t.Fatalf("unbounded labels leaked: %+v", o)
	}
}

func TestObservationMarksDuplicatesAsReplays(t *testing.T) {
	o := observation(WagerCommand{Channel: wagering.ChannelSQS, Kind: "WIN"}, WagerOutcome{}, true, nil, 0)
	if o.Status != ResultDuplicate || !o.Replay {
		t.Fatalf("%+v", o)
	}
}

type failingUoW struct{ err error }

func (u failingUoW) Do(context.Context, func(context.Context, Tx) error) error { return u.err }

func TestProcessReportsFailures(t *testing.T) {
	var got []WagerObservation
	p := NewWagerProcessor(failingUoW{fmt.Errorf("%w: deadlock", ErrUnavailable)}, SystemClock{}, TimeOrderedIDs{}, PendingPolicy{}, nil).
		WithObserver(func(o WagerObservation) { got = append(got, o) })
	if _, err := p.Process(context.Background(), WagerCommand{Channel: wagering.ChannelHTTP, Kind: "BET"}); err == nil {
		t.Fatal("expected error")
	}
	if len(got) != 1 || got[0].Status != ResultUnavailable || got[0].Channel != "HTTP" {
		t.Fatalf("%+v", got)
	}
}
