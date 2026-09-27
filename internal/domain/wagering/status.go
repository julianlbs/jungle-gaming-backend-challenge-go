package wagering

import "fmt"

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return st, nil
	default:
		return "", fmt.Errorf("%w: status %q", ErrInvalidField, s)
	}
}

func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

func ParseOrigin(s string) (Origin, error) {
	switch o := Origin(s); o {
	case OriginInternal, OriginExternal:
		return o, nil
	default:
		return "", fmt.Errorf("%w: origin %q", ErrInvalidField, s)
	}
}

// Channel is the transport through which a transaction was first received.
type Channel string

const (
	ChannelHTTP     Channel = "HTTP"
	ChannelSQS      Channel = "SQS"
	ChannelInternal Channel = "INTERNAL"
)

func ParseChannel(s string) (Channel, error) {
	switch c := Channel(s); c {
	case ChannelHTTP, ChannelSQS, ChannelInternal:
		return c, nil
	default:
		return "", fmt.Errorf("%w: channel %q", ErrInvalidField, s)
	}
}
