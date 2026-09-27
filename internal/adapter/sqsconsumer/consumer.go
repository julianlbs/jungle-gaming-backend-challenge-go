package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
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

type Config struct {
	QueueURL         string
	DLQURL           string
	AllowedProviders []string
	MaxInFlight      int
	// MessageTimeout bounds the handling of one message and must stay below the visibility timeout.
	MessageTimeout time.Duration
	WaitTime       time.Duration
}

type Consumer struct {
	sqs     API
	handler Handler
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Metrics
	wg      sync.WaitGroup
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
	return &Consumer{sqs: api, handler: handler, cfg: cfg, log: log.With("worker", "sqs-consumer"), metrics: m}
}

// Run polls until ctx is cancelled, then waits for the messages already started.
func (c *Consumer) Run(ctx context.Context) {
	defer c.wg.Wait()
	sem := make(chan struct{}, c.cfg.MaxInFlight)
	failures := 0
	for ctx.Err() == nil {
		out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.cfg.QueueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     int32(c.cfg.WaitTime / time.Second),
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			delay := min(time.Duration(1<<min(failures, 5))*time.Second, 30*time.Second)
			c.log.Warn("receive failed", "error", err, "retryIn", delay.String())
			sleep(ctx, delay)
			continue
		}
		failures = 0
		for _, group := range groupByMessageGroup(out.Messages) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				c.release(group)
				continue
			}
			c.wg.Add(1)
			go func() {
				defer func() { <-sem; c.wg.Done() }()
				c.handleGroup(ctx, group)
			}()
		}
	}
}

// handleGroup processes messages sharing a MessageGroupId in delivery order. After a message
// is left for redelivery, the rest of the group is released so that order is kept.
func (c *Consumer) handleGroup(ctx context.Context, group []types.Message) {
	for i, msg := range group {
		if ctx.Err() != nil {
			c.release(group[i:])
			return
		}
		if !c.handle(ctx, msg) {
			c.release(group[i+1:])
			return
		}
	}
}

// handle returns false when the message stays in the queue for another attempt.
func (c *Consumer) handle(parent context.Context, msg types.Message) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), c.cfg.MessageTimeout)
	defer cancel()
	ctx = logging.With(ctx, logging.KeyMessageID, aws.ToString(msg.MessageId))

	env, cmd, err := decode(aws.ToString(msg.Body))
	if err == nil {
		ctx = logging.With(ctx, logging.KeyCorrelationID, cmd.CorrelationID, logging.KeyProviderID, cmd.ProviderID)
		if !slices.Contains(c.cfg.AllowedProviders, cmd.ProviderID) {
			err = errProviderNotAllowed
		}
	}
	if err != nil {
		return c.fail(ctx, msg, err)
	}

	res, err := c.handler.Handle(ctx, app.IncomingWager{Consumer: ConsumerName, MessageID: env.MessageID, Command: cmd})
	if err != nil {
		return c.fail(ctx, msg, err)
	}
	outcome := string(res.Entry.Outcome)
	if res.Duplicate {
		outcome = "DUPLICATE"
		c.metrics.InboxDuplicates.WithLabelValues(ConsumerName).Inc()
	}
	ctx = logging.With(ctx, logging.KeyTransactionID, res.Entry.TransactionID.String())
	c.log.InfoContext(ctx, "message handled", "outcome", outcome)
	c.metrics.SQSMessages.WithLabelValues(outcome).Inc()
	c.delete(ctx, msg)
	return true
}

var errProviderNotAllowed = errors.New("provider not allowed on this queue")

// fail leaves the message for redelivery; the visibility timeout paces the retries.
func (c *Consumer) fail(ctx context.Context, msg types.Message, err error) bool {
	c.log.WarnContext(ctx, "message not handled", "error", err, "receiveCount", receiveCount(msg))
	c.metrics.SQSMessages.WithLabelValues("RETRY").Inc()
	return false
}

func (c *Consumer) delete(ctx context.Context, msg types.Message) {
	if _, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		// The inbox makes the redelivery a no-op.
		c.log.WarnContext(ctx, "delete failed", "error", err)
	}
}

// release makes messages visible again immediately.
func (c *Consumer) release(msgs []types.Message) {
	c.setVisibility(msgs, 0)
}

func (c *Consumer) setVisibility(msgs []types.Message, timeout time.Duration) {
	for _, m := range msgs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.sqs.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle,
			VisibilityTimeout: int32(timeout / time.Second),
		})
		cancel()
		if err != nil {
			c.log.Warn("change visibility failed", "error", err, logging.KeyMessageID, aws.ToString(m.MessageId))
		}
	}
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
