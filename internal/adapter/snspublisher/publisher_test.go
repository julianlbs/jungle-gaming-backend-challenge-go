package snspublisher

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/tracing"
)

type fakeSNS struct{ in *sns.PublishInput }

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.in = in
	return &sns.PublishOutput{}, nil
}

func TestPublishMapsRoutingContract(t *testing.T) {
	api := &fakeSNS{}
	ev := app.ClaimedEvent{
		ID: uuid.New(), PartitionKey: "wallet-1", EventType: "WalletBalanceChanged", EventVersion: 1,
		CorrelationID: "corr", Payload: []byte(`{"eventId":"x"}`),
	}
	if err := New(api, "arn:topic").Publish(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	in := api.in
	if aws.ToString(in.TopicArn) != "arn:topic" || aws.ToString(in.Message) != `{"eventId":"x"}` ||
		aws.ToString(in.MessageGroupId) != "wallet-1" || aws.ToString(in.MessageDeduplicationId) != ev.ID.String() {
		t.Fatalf("input = %+v", in)
	}
	if aws.ToString(in.MessageAttributes["eventType"].StringValue) != "WalletBalanceChanged" ||
		aws.ToString(in.MessageAttributes["eventVersion"].StringValue) != "1" ||
		aws.ToString(in.MessageAttributes["correlationId"].StringValue) != "corr" {
		t.Fatalf("attributes = %+v", in.MessageAttributes)
	}

	ev.CorrelationID = ""
	_ = New(api, "arn:topic").Publish(context.Background(), ev)
	if _, ok := api.in.MessageAttributes["correlationId"]; ok {
		t.Fatal("empty correlation id must not be sent")
	}
	if _, ok := api.in.MessageAttributes["traceparent"]; ok {
		t.Fatal("traceparent sent without a trace")
	}
}

func TestPublishPropagatesTraceContext(t *testing.T) {
	if _, err := tracing.Setup(context.Background(), tracing.Config{}); err != nil {
		t.Fatal(err)
	}
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := tracing.Extract(context.Background(), map[string]string{"traceparent": tp})
	api := &fakeSNS{}
	if err := New(api, "arn:topic").Publish(ctx, app.ClaimedEvent{ID: uuid.New(), PartitionKey: "w", EventType: "T", EventVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(api.in.MessageAttributes["traceparent"].StringValue); got != tp {
		t.Fatalf("traceparent attribute = %q", got)
	}
}
