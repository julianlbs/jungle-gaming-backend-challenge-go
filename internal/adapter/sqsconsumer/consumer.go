package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/tracing"
)

const ConsumerName = "wager-transactions"

// API is the subset of the SQS client used by the consumer.
type API interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type Handler interface {
	Handle(ctx context.Context, msg app.IncomingWager) (app.IntakeResult, error)
}

// Queue is one wager FIFO queue bound to a single provider. The binding is the
// queue the message was received from; senders cannot choose the provider.
type Queue struct {
	ProviderID string
	URL        string
}

type Config struct {
	Queues      []Queue
	DLQURL      string
	MaxInFlight int
	// MessageTimeout bounds the handling of one message and must stay below the visibility timeout.
	MessageTimeout time.Duration
	WaitTime       time.Duration
}

type Consumer struct {
	sqs             API
	handler         Handler
	cfg             Config
	providerByQueue map[string]string
	log             *slog.Logger
	metrics         *metrics.Metrics
	wg              sync.WaitGroup
}

func New(api API, handler Handler, cfg Config, log *slog.Logger, m *metrics.Metrics) *Consumer {
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 10
	}
	if cfg.MessageTimeout <= 0 {
		cfg.MessageTimeout = 30 * time.Second
	}
	if cfg.WaitTime <= 0 || cfg.WaitTime > 20*time.Second {
		cfg.WaitTime = 20 * time.Second
	}
	byQueue := make(map[string]string, len(cfg.Queues))
	for _, q := range cfg.Queues {
		byQueue[q.URL] = q.ProviderID
	}
	return &Consumer{
		sqs: api, handler: handler, cfg: cfg, providerByQueue: byQueue,
		log: log.With("worker", "sqs-consumer"), metrics: m,
	}
}

// Run polls every bound queue until ctx is cancelled, then waits for the messages already started.
func (c *Consumer) Run(ctx context.Context) {
	sem := make(chan struct{}, c.cfg.MaxInFlight)
	var pollers sync.WaitGroup
	for _, q := range c.cfg.Queues {
		pollers.Add(1)
		go func(queueURL string) {
			defer pollers.Done()
			c.poll(ctx, queueURL, sem)
		}(q.URL)
	}
	pollers.Wait()
	c.wg.Wait()
}

// poll reserves a worker slot before ReceiveMessage and takes one message for that slot.
// Visibility is not extended while the message is waiting for a slot or being handled:
// MessageTimeout is shorter than the queue visibility timeout, so handling finishes first.
func (c *Consumer) poll(ctx context.Context, queueURL string, sem chan struct{}) {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(queueURL),
			MaxNumberOfMessages:   1,
			WaitTimeSeconds:       int32(c.cfg.WaitTime / time.Second),
			MessageAttributeNames: traceAttributes,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		if err != nil || len(out.Messages) == 0 {
			<-sem
			if err == nil || ctx.Err() != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			failures++
			delay := min(time.Duration(1<<min(failures, 5))*time.Second, 30*time.Second)
			c.log.Warn("receive failed", "queueUrl", queueURL, "error", err, "retryIn", delay.String())
			sleep(ctx, delay)
			continue
		}
		failures = 0
		msgs := out.Messages
		c.wg.Add(1)
		go func() {
			defer func() { <-sem; c.wg.Done() }()
			c.handleGroup(ctx, queueURL, msgs)
		}()
	}
}

// handleGroup processes messages sharing a MessageGroupId in delivery order. After a message
// is left for redelivery, the rest of the group is released so that order is kept.
func (c *Consumer) handleGroup(ctx context.Context, queueURL string, group []types.Message) {
	for i, msg := range group {
		if ctx.Err() != nil {
			c.release(queueURL, group[i:])
			return
		}
		if !c.handle(ctx, queueURL, msg) {
			c.release(queueURL, group[i+1:])
			return
		}
	}
}

// handle returns false when the message stays in the queue for another attempt.
func (c *Consumer) handle(parent context.Context, queueURL string, msg types.Message) (done bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), c.cfg.MessageTimeout)
	defer cancel()
	ctx, span := tracer.Start(tracing.Extract(ctx, traceCarrier(msg)), "sqs.process "+ConsumerName,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "aws_sqs"), attribute.Int("sqs.receive_count", receiveCount(msg))))
	defer func() {
		if !done {
			span.SetStatus(codes.Error, "left for redelivery")
		}
		span.End()
	}()
	ctx = logging.With(ctx, logging.KeyMessageID, aws.ToString(msg.MessageId))

	env, cmd, err := decode(aws.ToString(msg.Body))
	if err == nil {
		ctx = logging.With(ctx, logging.KeyCorrelationID, cmd.CorrelationID, logging.KeyProviderID, cmd.ProviderID)
		// The provider is whichever queue delivered the message. Message attributes are ignored.
		if cmd.ProviderID == "" || cmd.ProviderID != c.providerByQueue[queueURL] {
			err = errProviderNotAllowed
		}
	}
	if err != nil {
		return c.fail(ctx, queueURL, msg, err)
	}

	res, err := c.handler.Handle(ctx, app.IncomingWager{Consumer: ConsumerName, MessageID: env.MessageID, Command: cmd})
	if err != nil {
		return c.fail(ctx, queueURL, msg, err)
	}
	outcome := string(res.Entry.Outcome)
	if res.Duplicate {
		outcome = "DUPLICATE"
		c.metrics.InboxDuplicates.WithLabelValues(ConsumerName).Inc()
	}
	ctx = logging.With(ctx, logging.KeyTransactionID, res.Entry.TransactionID.String())
	c.log.InfoContext(ctx, "message handled", "outcome", outcome)
	c.metrics.SQSMessages.WithLabelValues(outcome).Inc()
	c.delete(ctx, queueURL, msg)
	return true
}

var errProviderNotAllowed = errors.New("provider not allowed on this queue")

func (c *Consumer) delete(ctx context.Context, queueURL string, msg types.Message) {
	if _, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		// The inbox makes the redelivery a no-op.
		c.log.WarnContext(ctx, "delete failed", "error", err)
	}
}

// release makes messages visible again immediately.
func (c *Consumer) release(queueURL string, msgs []types.Message) {
	c.setVisibility(queueURL, msgs, 0)
}

func (c *Consumer) setVisibility(queueURL string, msgs []types.Message, timeout time.Duration) {
	for _, m := range msgs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.sqs.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle,
			VisibilityTimeout: int32(timeout / time.Second),
		})
		cancel()
		if err != nil {
			c.log.Warn("change visibility failed", "error", err, logging.KeyMessageID, aws.ToString(m.MessageId))
		}
	}
}

var (
	tracer          = otel.Tracer("github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/sqsconsumer")
	traceAttributes = []string{"traceparent", "tracestate"}
)

func traceCarrier(m types.Message) map[string]string {
	carrier := map[string]string{}
	for _, k := range traceAttributes {
		if v, ok := m.MessageAttributes[k]; ok && v.StringValue != nil {
			carrier[k] = *v.StringValue
		}
	}
	return carrier
}

func groupByMessageGroup(msgs []types.Message) [][]types.Message {
	var order []string
	groups := map[string][]types.Message{}
	for _, m := range msgs {
		g := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], m)
	}
	out := make([][]types.Message, 0, len(order))
	for _, g := range order {
		out = append(out, groups[g])
	}
	return out
}

func receiveCount(m types.Message) int {
	n, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	return n
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
