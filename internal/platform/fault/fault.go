//go:build faultinject

// Package fault kills the process at named points so that crash recovery can be tested
// against real processes. It is compiled in only with the faultinject build tag.
package fault

import (
	"os"
	"syscall"
)

const (
	ConsumerAfterCommit = "consumer.after_commit"
	OutboxAfterPublish  = "outbox.after_publish"
	PendingAfterClaim   = "pending.after_claim"
)

const enabled = true

// Hit sends SIGKILL to the current process when FAULT_POINT names point.
func Hit(point string) {
	if os.Getenv("FAULT_POINT") == point {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
}
