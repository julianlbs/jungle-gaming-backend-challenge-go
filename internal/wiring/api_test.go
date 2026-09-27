package wiring

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadinessChecksFollowRoles(t *testing.T) {
	sqsClient := sqs.NewFromConfig(aws.Config{Region: "us-east-1"})
	snsClient := sns.NewFromConfig(aws.Config{Region: "us-east-1"})
	for roles, want := range map[string][]string{
		"api":                         {"database"},
		"api,pending":                 {"database"},
		"api,consumer":                {"database", "sqs"},
		"api,outbox":                  {"database", "sns"},
		"api,consumer,outbox,pending": {"database", "sqs", "sns"},
	} {
		got := newReadiness(readinessDeps{Cfg: testConfig(t, roles), Pool: &pgxpool.Pool{}, SQS: sqsClient, SNS: snsClient}).Checks()
		if !slices.Equal(got, want) {
			t.Errorf("%s: checks = %v, want %v", roles, got, want)
		}
	}
}

type fakeQueue struct {
	err   error
	input *sqs.GetQueueAttributesInput
}

func (f *fakeQueue) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.input = in
	return &sqs.GetQueueAttributesOutput{}, f.err
}

type fakeTopic struct{ err error }

func (f fakeTopic) GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	return &sns.GetTopicAttributesOutput{}, f.err
}

func TestBrokerChecks(t *testing.T) {
	q := &fakeQueue{}
	if err := sqsCheck(q, "http://queue")(context.Background()); err != nil || aws.ToString(q.input.QueueUrl) != "http://queue" {
		t.Fatalf("err=%v input=%+v", err, q.input)
	}
	q.err = errors.New("down")
	if err := sqsCheck(q, "http://queue")(context.Background()); err == nil {
		t.Fatal("sqs failure not reported")
	}
	if err := snsCheck(fakeTopic{errors.New("down")}, "arn")(context.Background()); err == nil {
		t.Fatal("sns failure not reported")
	}
}
