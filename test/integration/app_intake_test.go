//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func TestIntakeRecordsInboxWithEffects(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	intake := app.NewWagerIntake(f.db.UoW, f.processor, app.SystemClock{})
	w := f.openWallet(t, "100.00")

	cmd := wager(w, "BET", "10.00", "sqs-1")
	cmd.Channel = wagering.ChannelSQS
	msg := app.IncomingWager{Consumer: "test", MessageID: "msg-1", Command: cmd}

	res, err := intake.Handle(ctx, msg)
	if err != nil || res.Duplicate || res.Entry.Outcome != app.InboxProcessed {
		t.Fatalf("first delivery = %+v, %v", res, err)
	}
	res, err = intake.Handle(ctx, msg)
	if err != nil || !res.Duplicate {
		t.Fatalf("redelivery = %+v, %v", res, err)
	}
	if f.balance(t, w) != "90.00" {
		t.Fatalf("balance = %s", f.balance(t, w))
	}

	changed := msg
	changed.Command.Amount = "11.00"
	if _, err := intake.Handle(ctx, changed); !errors.Is(err, app.ErrMessageConflict) || !app.IsPermanent(err) {
		t.Fatalf("conflicting redelivery err = %v", err)
	}

	// The same operation first received over HTTP is a replay when it later arrives over SQS.
	httpCmd := wager(w, "BET", "5.00", "both-1")
	if _, err := f.processor.Process(ctx, httpCmd); err != nil {
		t.Fatal(err)
	}
	sqsCmd := httpCmd
	sqsCmd.Channel = wagering.ChannelSQS
	res, err = intake.Handle(ctx, app.IncomingWager{Consumer: "test", MessageID: "msg-2", Command: sqsCmd})
	if err != nil || res.Entry.Outcome != app.InboxReplayed || !res.Outcome.Replay {
		t.Fatalf("cross-channel = %+v, %v", res, err)
	}
	if f.balance(t, w) != "85.00" {
		t.Fatalf("balance = %s", f.balance(t, w))
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM inbox_messages WHERE consumer_name = 'test'`); n != 2 {
		t.Fatalf("inbox rows = %d", n)
	}

	bad := wager(w, "OPENING", "1.00", "bad-1")
	if _, err := intake.Handle(ctx, app.IncomingWager{Consumer: "test", MessageID: "msg-3", Command: bad}); !app.IsPermanent(err) {
		t.Fatalf("reserved kind err = %v", err)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-3'`); n != 0 {
		t.Fatalf("rejected input left an inbox row")
	}
}
