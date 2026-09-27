package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(overrides map[string]string) func(string) (string, bool) {
	base := map[string]string{
		"DATABASE_URL":         "postgres://wallet_app:x@localhost:5432/wallet",
		"OIDC_ISSUER":          "http://localhost:8080/realms/wallet",
		"OIDC_JWKS_URL":        "http://keycloak:8080/realms/wallet/protocol/openid-connect/certs",
		"OIDC_AUDIENCE":        "wallet-api",
		"SQS_PROVIDER_QUEUES":  "provider-a=http://localstack:4566/000000000000/wager-transactions-provider-a.fifo, provider-b=http://localstack:4566/000000000000/wager-transactions-provider-b.fifo",
		"SQS_WAGER_DLQ_URL":    "http://localstack:4566/000000000000/wager-transactions-dlq.fifo",
		"SNS_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
	}
	for k, v := range overrides {
		if v == "<unset>" {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	return func(k string) (string, bool) {
		v, ok := base[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range allRoles {
		if !cfg.Roles.Has(role) {
			t.Errorf("default roles miss %s", role)
		}
	}
	if cfg.HTTPAddr != ":8080" || cfg.MetricsAddr != ":9090" || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("addresses/level: %+v", cfg)
	}
	if cfg.Postgres.MaxConns != 20 || cfg.Postgres.LockTimeout != 2*time.Second {
		t.Errorf("postgres: %+v", cfg.Postgres)
	}
	if len(cfg.SQS.Queues) != 2 || cfg.SQS.Queues[0].ProviderID != "provider-a" ||
		cfg.SQS.Queues[1].ProviderID != "provider-b" || cfg.SQS.Queues[0].URL == cfg.SQS.Queues[1].URL {
		t.Errorf("provider queues: %+v", cfg.SQS.Queues)
	}
	if cfg.Pending.TTL != 30*time.Minute || cfg.Pending.MaxAttempts != 10 || cfg.InstanceID == "" {
		t.Errorf("pending/instance: %+v %q", cfg.Pending, cfg.InstanceID)
	}
	if cfg.Outbox.BackoffBase != time.Second || cfg.Outbox.BackoffMax != 5*time.Minute || cfg.Outbox.Retention != 0 {
		t.Errorf("outbox: %+v", cfg.Outbox)
	}
}

func TestOutboxRetention(t *testing.T) {
	cfg, err := Load(env(map[string]string{"OUTBOX_RETENTION": "168h", "OUTBOX_BACKOFF_BASE": "200ms", "OUTBOX_BACKOFF_MAX": "2s"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outbox.Retention != 168*time.Hour || cfg.Outbox.BackoffBase != 200*time.Millisecond || cfg.Outbox.BackoffMax != 2*time.Second {
		t.Fatalf("outbox: %+v", cfg.Outbox)
	}
	if _, err := Load(env(map[string]string{"OUTBOX_RETENTION": "1s"})); err == nil ||
		!strings.Contains(err.Error(), "OUTBOX_RETENTION must be a duration") {
		t.Fatalf("short retention accepted: %v", err)
	}
}

func TestTracing(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracing.Endpoint != "" || cfg.Tracing.SampleRatio != 1 {
		t.Fatalf("tracing defaults: %+v", cfg.Tracing)
	}
	cfg, err = Load(env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://jaeger:4318/", "OTEL_TRACES_SAMPLER_ARG": "0.25"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracing.Endpoint != "http://jaeger:4318" || cfg.Tracing.SampleRatio != 0.25 {
		t.Fatalf("tracing: %+v", cfg.Tracing)
	}
	_, err = Load(env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "jaeger", "OTEL_TRACES_SAMPLER_ARG": "2"}))
	if err == nil || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute URL") ||
		!strings.Contains(err.Error(), "OTEL_TRACES_SAMPLER_ARG must be a number between 0 and 1") {
		t.Fatalf("invalid tracing accepted: %v", err)
	}
}

func TestRoleScopedRequirements(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"APP_ROLES":            "pending",
		"OIDC_ISSUER":          "<unset>",
		"SQS_PROVIDER_QUEUES":  "<unset>",
		"SNS_EVENTS_TOPIC_ARN": "<unset>",
	}))
	if err != nil {
		t.Fatalf("pending-only process should not need api, sqs or sns settings: %v", err)
	}
	if cfg.Roles.Has(RoleAPI) || !cfg.Roles.Has(RolePending) {
		t.Fatalf("roles = %v", cfg.Roles)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"APP_ROLES":            "api,billing",
		"DATABASE_URL":         "<unset>",
		"DB_MAX_CONNS":         "0",
		"DB_LOCK_TIMEOUT":      "soon",
		"LOG_LEVEL":            "loud",
		"OIDC_JWKS_URL":        "keycloak/certs",
		"SNS_EVENTS_TOPIC_ARN": "wallet-events",
		"PENDING_BACKOFF_BASE": "10m",
		"PENDING_BACKOFF_MAX":  "1m",
		"OUTBOX_BACKOFF_BASE":  "10m",
		"OUTBOX_BACKOFF_MAX":   "1m",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		`APP_ROLES has unknown role "billing"`,
		"DATABASE_URL is required",
		"DB_MAX_CONNS must be an integer",
		"DB_LOCK_TIMEOUT must be a duration",
		"LOG_LEVEL must be",
		"OIDC_JWKS_URL must be an absolute URL",
		"PENDING_BACKOFF_MAX must not be lower",
		"OUTBOX_BACKOFF_MAX must not be lower",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "SNS_EVENTS_TOPIC_ARN") {
		t.Error("outbox settings validated although the outbox role is disabled")
	}
}

func TestConsumerNeedsProviderQueues(t *testing.T) {
	cases := map[string]string{
		"empty":      " , ",
		"missing":    "<unset>",
		"no url":     "provider-a",
		"bad url":    "provider-a=not-a-url",
		"duplicate":  "provider-a=http://localstack:4566/000000000000/q.fifo,provider-a=http://localstack:4566/000000000000/other.fifo",
		"same queue": "provider-a=http://localstack:4566/000000000000/q.fifo,provider-b=http://localstack:4566/000000000000/q.fifo",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(map[string]string{"APP_ROLES": "consumer", "SQS_PROVIDER_QUEUES": raw}))
			if err == nil || !strings.Contains(err.Error(), "SQS_PROVIDER_QUEUES") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestEmptyRolesRejected(t *testing.T) {
	_, err := Load(env(map[string]string{"APP_ROLES": ","}))
	if err == nil || !strings.Contains(err.Error(), "at least one role") {
		t.Fatalf("err = %v", err)
	}
}
