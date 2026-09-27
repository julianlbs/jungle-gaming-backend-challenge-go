package sqsconsumer

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

const maxRetryDelay = 300 * time.Second

// failureReason names why a message can never succeed, or returns "" for transient errors.
func failureReason(err error) string {
	var fe *wagering.FieldError
	switch {
	case errors.Is(err, errInvalidMessage):
		return "INVALID_MESSAGE"
	case errors.Is(err, errProviderNotAllowed):
		return "PROVIDER_NOT_ALLOWED"
	case errors.Is(err, wagering.ErrReservedKind), errors.As(err, &fe):
		return "VALIDATION_ERROR"
	case errors.Is(err, app.ErrWalletNotFound):
		return "WALLET_NOT_FOUND"
	case errors.Is(err, app.ErrWalletMismatch):
		return "WALLET_MISMATCH"
	case errors.Is(err, app.ErrIdempotencyKeyReused):
		return "IDEMPOTENCY_KEY_REUSED"
	case errors.Is(err, app.ErrExternalTransactionConflict):
		return "EXTERNAL_TRANSACTION_CONFLICT"
	case errors.Is(err, app.ErrMessageConflict):
		return "MESSAGE_CONFLICT"
	case app.IsPermanent(err):
		return "PERMANENT_ERROR"
	default:
		return ""
	}
}

// retryDelay grows with each delivery; after the queue's maxReceiveCount the redrive policy
// moves the message to the dead-letter queue.
func retryDelay(receiveCount int) time.Duration {
	if receiveCount < 1 {
		receiveCount = 1
	}
	return min(time.Duration(1<<min(receiveCount, 9))*time.Second, maxRetryDelay)
}

// fail routes a message that was not handled. It returns false when the message stays in the
// queue, which also holds back the rest of its group.
func (c *Consumer) fail(ctx context.Context, queueURL string, msg types.Message, err error) bool {
	count := receiveCount(msg)
	if reason := failureReason(err); reason != "" {
		if dlqErr := c.deadLetter(ctx, msg, reason, err); dlqErr != nil {
			c.log.ErrorContext(ctx, "dead-letter failed, message left for redrive",
				"reason", reason, "error", err, "dlqError", dlqErr)
			c.setVisibility(queueURL, []types.Message{msg}, retryDelay(count))
			return false
		}
		c.log.WarnContext(ctx, "message dead-lettered", "reason", reason, "error", err)
		c.metrics.SQSDeadLettered.WithLabelValues(reason).Inc()
		c.metrics.SQSMessages.WithLabelValues("DEAD_LETTERED").Inc()
		c.delete(ctx, queueURL, msg)
		return true
	}
	delay := retryDelay(count)
	c.log.WarnContext(ctx, "message will be retried", "error", err, "receiveCount", count, "retryIn", delay.String())
	c.metrics.SQSRetries.Inc()
	c.metrics.SQSMessages.WithLabelValues("RETRY").Inc()
	c.setVisibility(queueURL, []types.Message{msg}, delay)
	return false
}

func (c *Consumer) deadLetter(ctx context.Context, msg types.Message, reason string, cause error) error {
	group := msg.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "unknown"
	}
	detail := cause.Error()
	if len(detail) > 256 {
		detail = detail[:256]
	}
	_, err := c.sqs.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.cfg.DLQURL),
		MessageBody:            msg.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: msg.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"failureReason":   {DataType: aws.String("String"), StringValue: aws.String(reason)},
			"failureDetail":   {DataType: aws.String("String"), StringValue: aws.String(detail)},
			"sourceMessageId": {DataType: aws.String("String"), StringValue: msg.MessageId},
			"consumer":        {DataType: aws.String("String"), StringValue: aws.String(ConsumerName)},
		},
	})
	return err
}
