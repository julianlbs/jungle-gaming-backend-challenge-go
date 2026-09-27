//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

func TestHarnessRunsIndependentInstances(t *testing.T) {
	c := newCluster(t)
	a := c.start("app-a", nil)
	b := c.start("app-b", nil)

	w := c.openWallet(a, "10.00")
	r := c.mustCall(b, http.MethodGet, "/wallets/"+w.id, "wallet-backoffice", nil, nil)
	if r.status != http.StatusOK || r.body["id"] != w.id {
		t.Fatalf("read from second instance: %d %v", r.status, r.body)
	}

	if code, took := a.Stop(t, 20*time.Second); code != 0 || took > 10*time.Second {
		t.Fatalf("graceful stop: code=%d took=%v", code, took)
	}
	b.Kill()
	if !killedBySignal(b.state) {
		t.Fatalf("kill not detected: %v", b.state)
	}
}
