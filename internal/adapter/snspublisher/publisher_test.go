package snspublisher

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
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
}
