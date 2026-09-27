package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestContextFieldsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo, "app-1")

	ctx := With(context.Background(), KeyCorrelationID, "corr-1", KeyProviderID, "provider-a")
	ctx = With(ctx, KeyTransactionID, "tx-1")
	log.InfoContext(ctx, "processed", "status", "PROCESSED")
	log.DebugContext(ctx, "hidden")

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("expected exactly one JSON line, got %q: %v", buf.String(), err)
	}
	for k, want := range map[string]string{
		"msg": "processed", "status": "PROCESSED", KeyInstanceID: "app-1",
		KeyCorrelationID: "corr-1", KeyProviderID: "provider-a", KeyTransactionID: "tx-1",
	} {
		if line[k] != want {
			t.Errorf("%s = %v, want %q", k, line[k], want)
		}
	}
	if got := CorrelationID(ctx); got != "corr-1" {
		t.Errorf("CorrelationID = %q", got)
	}
	if CorrelationID(context.Background()) != "" {
		t.Error("empty context has a correlation id")
	}
}

func TestWithDoesNotLeakIntoParent(t *testing.T) {
	parent := With(context.Background(), KeyCorrelationID, "a")
	_ = With(parent, KeyWalletID, "w")
	attrs, _ := parent.Value(ctxKey{}).([]slog.Attr)
	if len(attrs) != 1 {
		t.Fatalf("parent attrs mutated: %v", attrs)
	}
}
