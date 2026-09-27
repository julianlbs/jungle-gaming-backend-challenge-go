package fault

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestHitKillsOnlyAtTheConfiguredPoint(t *testing.T) {
	if os.Getenv("FAULT_CHILD") == "1" {
		Hit("other.point")
		Hit(ConsumerAfterCommit)
		os.Exit(0)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHitKillsOnlyAtTheConfiguredPoint$")
	cmd.Env = append(os.Environ(), "FAULT_CHILD=1", "FAULT_POINT="+ConsumerAfterCommit)
	err := cmd.Run()

	killed := false
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			killed = ws.Signaled() && ws.Signal() == syscall.SIGKILL
		}
	}
	if enabled != killed {
		t.Fatalf("enabled=%v killed=%v err=%v", enabled, killed, err)
	}
}
