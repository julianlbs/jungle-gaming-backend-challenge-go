//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

type awsClients struct {
	sqs *sqs.Client
	sns *sns.Client
}

func newAWS(t *testing.T) awsClients {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithBaseEndpoint(getenv("TEST_AWS_ENDPOINT_URL", "http://localhost:4566")),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return awsClients{sqs: sqs.NewFromConfig(cfg), sns: sns.NewFromConfig(cfg)}
}

func uniqueName(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] + ".fifo"
}

func (a awsClients) queueArn(t *testing.T, url string) string {
	t.Helper()
	out := must(a.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}))
	return out.Attributes[string(types.QueueAttributeNameQueueArn)]
}

// fifoQueue creates a temporary FIFO queue, optionally redriving to dlqURL after maxReceives.
func (a awsClients) fifoQueue(t *testing.T, prefix, dlqURL string, maxReceives int) string {
	t.Helper()
	ctx := context.Background()
	attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": "30"}
	if dlqURL != "" {
		attrs["RedrivePolicy"] = fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"%d"}`, a.queueArn(t, dlqURL), maxReceives)
	}
	out := must(a.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniqueName(prefix)), Attributes: attrs}))
	t.Cleanup(func() { _, _ = a.sqs.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl}) })
	return aws.ToString(out.QueueUrl)
}

// fifoTopic creates a temporary topic delivering raw messages to a new queue.
func (a awsClients) fifoTopic(t *testing.T) (topicARN, queueURL string) {
	t.Helper()
	ctx := context.Background()
	topic := must(a.sns.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(uniqueName("events")),
		Attributes: map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "false"},
	}))
	t.Cleanup(func() {
		_, _ = a.sns.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: topic.TopicArn})
	})
	queueURL = a.fifoQueue(t, "audit", "", 0)
	must(a.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(a.queueArn(t, queueURL)),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	}))
	return aws.ToString(topic.TopicArn), queueURL
}

func (a awsClients) queueEmpty(t *testing.T, url string) bool {
	t.Helper()
	out := must(a.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	}))
	return out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
}

func (a awsClients) send(t *testing.T, queueURL, group, dedup string, body []byte) {
	t.Helper()
	must(a.sqs.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup),
	}))
}

// drain receives and deletes messages until want arrived or the timeout passed, then keeps
// listening briefly to surface unexpected extras.
func (a awsClients) drain(t *testing.T, queueURL string, want int, timeout time.Duration) []types.Message {
	t.Helper()
	var got []types.Message
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := must(a.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
		}))
		for _, m := range out.Messages {
			got = append(got, m)
			_, _ = a.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle})
		}
		if len(got) >= want && len(out.Messages) == 0 {
			break
		}
	}
	return got
}

func eventID(t *testing.T, m types.Message) string {
	t.Helper()
	var env struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil || env.EventID == "" {
		t.Fatalf("event body %q: %v", aws.ToString(m.Body), err)
	}
	return env.EventID
}
