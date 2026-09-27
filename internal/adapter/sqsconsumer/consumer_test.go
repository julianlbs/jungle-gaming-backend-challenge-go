package sqsconsumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

type fakeSQS struct {
	mu         sync.Mutex
	deleted    []string
	visibility map[string]int32
	sent       []*sqs.SendMessageInput
	sendErr    error
}

func newFakeSQS() *fakeSQS { return &fakeSQS{visibility: map[string]int32{}} }

func (f *fakeSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visibility[aws.ToString(in.ReceiptHandle)] = in.VisibilityTimeout
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, in)
	return &sqs.SendMessageOutput{}, nil
}

type fakeHandler struct {
	results map[string]error
	calls   []string
	dup     map[string]bool
}

func (h *fakeHandler) Handle(_ context.Context, msg app.IncomingWager) (app.IntakeResult, error) {
	h.calls = append(h.calls, msg.MessageID)
	if err := h.results[msg.MessageID]; err != nil {
		return app.IntakeResult{}, err
	}
	return app.IntakeResult{
		Duplicate: h.dup[msg.MessageID],
		Entry:     app.InboxEntry{Outcome: app.InboxProcessed, TransactionID: uuid.New()},
	}, nil
}

func message(t *testing.T, id, provider string, receives string) types.Message {
	t.Helper()
	body, err := Encode(id, "2026-09-08T12:00:00Z", "", app.WagerCommand{
		ProviderID: provider, ExternalTransactionID: "ext-" + id, IdempotencyKey: provider + ":ext-" + id,
		PlayerID: uuid.NewString(), WalletID: "wallet-1", RoundID: "r", GameID: "g",
		Kind: "BET", Amount: "1.00", Currency: "BRL",
	})
	if err != nil {
		t.Fatal(err)
	}
	return types.Message{
		MessageId: aws.String("sqs-" + id), ReceiptHandle: aws.String(id), Body: aws.String(string(body)),
		Attributes: map[string]string{"MessageGroupId": "wallet-1", "ApproximateReceiveCount": receives},
	}
}

func newTestConsumer(api API, h Handler) *Consumer {
	return New(api, h, Config{QueueURL: "q", DLQURL: "dlq", AllowedProviders: []string{"provider-a"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
}

func TestDecode(t *testing.T) {
	valid := message(t, "m1", "provider-a", "1")
	_, cmd, err := decode(aws.ToString(valid.Body))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.CausationID != "m1" || cmd.CorrelationID != "m1" || cmd.IdempotencyKey != "provider-a:ext-m1" || cmd.Channel != "SQS" {
		t.Fatalf("command = %+v", cmd)
	}

	for name, body := range map[string]string{
		"not json":      "{",
		"trailing":      `{"messageId":"a","type":"WagerTransactionRequested","data":{"money":{},"idempotencyKey":"k"}} {}`,
		"no message id": `{"type":"WagerTransactionRequested","data":{"money":{},"idempotencyKey":"k"}}`,
		"unknown type":  `{"messageId":"a","type":"Other","data":{"money":{},"idempotencyKey":"k"}}`,
		"no data":       `{"messageId":"a","type":"WagerTransactionRequested"}`,
		"no money":      `{"messageId":"a","type":"WagerTransactionRequested","data":{"idempotencyKey":"k"}}`,
		"no key":        `{"messageId":"a","type":"WagerTransactionRequested","data":{"money":{}}}`,
	} {
		if _, _, err := decode(body); !errors.Is(err, errInvalidMessage) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestHandledMessagesAreDeleted(t *testing.T) {
	api := newFakeSQS()
	h := &fakeHandler{dup: map[string]bool{"m2": true}}
	c := newTestConsumer(api, h)
	c.handleGroup(context.Background(), []types.Message{message(t, "m1", "provider-a", "1"), message(t, "m2", "provider-a", "1")})
	if len(api.deleted) != 2 {
		t.Fatalf("deleted = %v", api.deleted)
	}
}

func TestTransientFailureKeepsGroupOrder(t *testing.T) {
	api := newFakeSQS()
	h := &fakeHandler{results: map[string]error{"m1": app.ErrUnavailable}}
	c := newTestConsumer(api, h)
	c.handleGroup(context.Background(), []types.Message{message(t, "m1", "provider-a", "1"), message(t, "m2", "provider-a", "1")})
	if len(api.deleted) != 0 || len(h.calls) != 1 {
		t.Fatalf("deleted = %v, calls = %v", api.deleted, h.calls)
	}
	if v, ok := api.visibility["m2"]; !ok || v != 0 {
		t.Fatalf("m2 was not released: %v", api.visibility)
	}
}

func TestShutdownReleasesUnstartedMessages(t *testing.T) {
	api := newFakeSQS()
	h := &fakeHandler{}
	c := newTestConsumer(api, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.handleGroup(ctx, []types.Message{message(t, "m1", "provider-a", "1")})
	if len(h.calls) != 0 || api.visibility["m1"] != 0 {
		t.Fatalf("calls = %v, visibility = %v", h.calls, api.visibility)
	}
	if _, ok := api.visibility["m1"]; !ok {
		t.Fatal("m1 was not released")
	}
}

func TestGroupByMessageGroupKeepsOrder(t *testing.T) {
	mk := func(id, g string) types.Message {
		return types.Message{MessageId: aws.String(id), Attributes: map[string]string{"MessageGroupId": g}}
	}
	groups := groupByMessageGroup([]types.Message{mk("1", "a"), mk("2", "b"), mk("3", "a")})
	if len(groups) != 2 || aws.ToString(groups[0][1].MessageId) != "3" || aws.ToString(groups[1][0].MessageId) != "2" {
		t.Fatalf("groups = %v", groups)
	}
}
