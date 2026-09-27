//go:build !faultinject

// Package fault kills the process at named points so that crash recovery can be tested
// against real processes. It is compiled in only with the faultinject build tag.
package fault

const (
	ConsumerAfterCommit = "consumer.after_commit"
	OutboxAfterPublish  = "outbox.after_publish"
	PendingAfterClaim   = "pending.after_claim"
)

const enabled = false

func Hit(string) {}
