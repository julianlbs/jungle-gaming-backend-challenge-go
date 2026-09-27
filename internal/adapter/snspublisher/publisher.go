// Package snspublisher publishes outbox events to an SNS FIFO topic.
package snspublisher

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/tracing"
)

type API interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Publisher struct {
	sns      API
	topicARN string
}

func New(api API, topicARN string) *Publisher {
	return &Publisher{sns: api, topicARN: topicARN}
}

// Publish sends the stored payload unchanged. Using the event id as deduplication id makes a
// republication after a crash indistinguishable from the original for consumers.
func (p *Publisher) Publish(ctx context.Context, ev app.ClaimedEvent) error {
	attrs := map[string]types.MessageAttributeValue{
		"eventType":    {DataType: aws.String("String"), StringValue: aws.String(ev.EventType)},
		"eventVersion": {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(ev.EventVersion))},
	}
	if ev.CorrelationID != "" {
		attrs["correlationId"] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(ev.CorrelationID)}
	}
	for k, v := range tracing.Inject(ctx) {
		attrs[k] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
	}
	_, err := p.sns.Publish(ctx, &sns.PublishInput{
		TopicArn:               aws.String(p.topicARN),
		Message:                aws.String(string(ev.Payload)),
		MessageGroupId:         aws.String(ev.PartitionKey),
		MessageDeduplicationId: aws.String(ev.ID.String()),
		MessageAttributes:      attrs,
	})
	if err != nil {
		return fmt.Errorf("publish %s %s: %w", ev.EventType, ev.ID, err)
	}
	return nil
}
