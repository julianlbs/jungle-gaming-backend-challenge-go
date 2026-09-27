// Package config loads and validates process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	RoleAPI      Role = "api"
	RoleConsumer Role = "consumer"
	RoleOutbox   Role = "outbox"
	RolePending  Role = "pending"
)

var allRoles = []Role{RoleAPI, RoleConsumer, RoleOutbox, RolePending}

type Roles []Role

func (r Roles) Has(role Role) bool { return slices.Contains(r, role) }

type Config struct {
	Roles           Roles
	InstanceID      string
	HTTPAddr        string
	MetricsAddr     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration

	Postgres Postgres
	Auth     Auth
	AWS      AWS
	SQS      SQS
	SNS      SNS
	Outbox   Outbox
	Pending  Pending
	Tracing  Tracing
}

type Postgres struct {
	URL              string
	MaxConns         int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	StartupTimeout   time.Duration
}

type Auth struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type AWS struct {
	Region      string
	EndpointURL string
}

// ProviderQueue binds one FIFO queue to one provider. The consumer trusts the queue,
// not the message body, for the provider id.
type ProviderQueue struct {
	ProviderID string
	URL        string
}

type SQS struct {
	Queues         []ProviderQueue
	DLQURL         string
	MaxInFlight    int
	MessageTimeout time.Duration
}

type SNS struct {
	TopicARN string
}

type Outbox struct {
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
	// Retention is how long published events are kept; zero keeps them forever.
	Retention time.Duration
}

type Tracing struct {
	// Endpoint is the OTLP/HTTP collector base URL; empty disables span export.
	Endpoint    string
	SampleRatio float64
}

type Pending struct {
	PollInterval time.Duration
	Lease        time.Duration
	BatchSize    int
	BackoffBase  time.Duration
	BackoffMax   time.Duration
	MaxAttempts  int
	TTL          time.Duration
}

// FromEnv loads the configuration from the process environment.
func FromEnv() (Config, error) {
	return Load(os.LookupEnv)
}

// Load reads every setting through lookup and returns all problems found at once.
func Load(lookup func(string) (string, bool)) (Config, error) {
	r := reader{lookup: lookup}
	cfg := Config{
		Roles:           r.roles("APP_ROLES", "api,consumer,outbox,pending"),
		InstanceID:      r.str("INSTANCE_ID", defaultInstanceID()),
		HTTPAddr:        r.str("HTTP_ADDR", ":8080"),
		MetricsAddr:     r.str("METRICS_ADDR", ":9090"),
		LogLevel:        r.level("LOG_LEVEL", "info"),
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", "30s", time.Second, 5*time.Minute),
		Postgres: Postgres{
			URL:              r.required("DATABASE_URL"),
			MaxConns:         int32(r.positive("DB_MAX_CONNS", "20", 1000)),
			LockTimeout:      r.duration("DB_LOCK_TIMEOUT", "2s", 10*time.Millisecond, time.Minute),
			StatementTimeout: r.duration("DB_STATEMENT_TIMEOUT", "5s", 10*time.Millisecond, 5*time.Minute),
			StartupTimeout:   r.duration("DB_STARTUP_TIMEOUT", "30s", time.Second, 10*time.Minute),
		},
		AWS: AWS{
			Region:      r.str("AWS_REGION", "us-east-1"),
			EndpointURL: r.optionalURL("AWS_ENDPOINT_URL"),
		},
		Outbox: Outbox{
			BatchSize:    r.positive("OUTBOX_BATCH_SIZE", "50", 1000),
			Lease:        r.duration("OUTBOX_LEASE", "30s", time.Second, time.Hour),
			PollInterval: r.duration("OUTBOX_POLL_INTERVAL", "250ms", 10*time.Millisecond, time.Minute),
			BackoffBase:  r.duration("OUTBOX_BACKOFF_BASE", "1s", time.Millisecond, time.Hour),
			BackoffMax:   r.duration("OUTBOX_BACKOFF_MAX", "5m", time.Millisecond, 24*time.Hour),
			Retention:    r.optionalDuration("OUTBOX_RETENTION", time.Hour, 365*24*time.Hour),
		},
		Pending: Pending{
			PollInterval: r.duration("PENDING_POLL_INTERVAL", "500ms", 10*time.Millisecond, time.Minute),
			Lease:        r.duration("PENDING_LEASE", "30s", time.Second, time.Hour),
			BatchSize:    r.positive("PENDING_BATCH_SIZE", "50", 1000),
			BackoffBase:  r.duration("PENDING_BACKOFF_BASE", "2s", time.Millisecond, time.Hour),
			BackoffMax:   r.duration("PENDING_BACKOFF_MAX", "5m", time.Millisecond, 24*time.Hour),
			MaxAttempts:  r.positive("PENDING_MAX_ATTEMPTS", "10", 10000),
			TTL:          r.duration("PENDING_REFERENCE_TTL", "30m", 100*time.Millisecond, 7*24*time.Hour),
		},
		Tracing: Tracing{
			Endpoint:    strings.TrimSuffix(r.optionalURL("OTEL_EXPORTER_OTLP_ENDPOINT"), "/"),
			SampleRatio: r.ratio("OTEL_TRACES_SAMPLER_ARG", "1"),
		},
	}

	if cfg.Roles.Has(RoleAPI) {
		cfg.Auth = Auth{
			Issuer:   r.requiredURL("OIDC_ISSUER"),
			JWKSURL:  r.requiredURL("OIDC_JWKS_URL"),
			Audience: r.required("OIDC_AUDIENCE"),
		}
	}
	if cfg.Roles.Has(RoleConsumer) {
		cfg.SQS = SQS{
			Queues:         r.providerQueues("SQS_PROVIDER_QUEUES"),
			DLQURL:         r.requiredURL("SQS_WAGER_DLQ_URL"),
			MaxInFlight:    r.positive("SQS_MAX_IN_FLIGHT", "10", 100),
			MessageTimeout: r.duration("SQS_MESSAGE_TIMEOUT", "30s", time.Second, 10*time.Minute),
		}
	}
	if cfg.Roles.Has(RoleOutbox) {
		cfg.SNS = SNS{TopicARN: r.required("SNS_EVENTS_TOPIC_ARN")}
		if arn := cfg.SNS.TopicARN; arn != "" && !strings.HasPrefix(arn, "arn:") {
			r.fail("SNS_EVENTS_TOPIC_ARN", "must be an ARN")
		}
	}
	if cfg.Outbox.BackoffMax < cfg.Outbox.BackoffBase {
		r.fail("OUTBOX_BACKOFF_MAX", "must not be lower than OUTBOX_BACKOFF_BASE")
	}
	if cfg.Pending.BackoffMax < cfg.Pending.BackoffBase {
		r.fail("PENDING_BACKOFF_MAX", "must not be lower than PENDING_BACKOFF_BASE")
	}

	if err := errors.Join(r.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func defaultInstanceID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "wallet"
}

type reader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (r *reader) fail(key, reason string) {
	r.errs = append(r.errs, fmt.Errorf("%s %s", key, reason))
}

func (r *reader) str(key, fallback string) string {
	if v, ok := r.lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func (r *reader) required(key string) string {
	v := r.str(key, "")
	if v == "" {
		r.fail(key, "is required")
	}
	return v
}

func (r *reader) requiredURL(key string) string {
	v := r.required(key)
	if v != "" && !validURL(v) {
		r.fail(key, "must be an absolute URL")
	}
	return v
}

func (r *reader) optionalURL(key string) string {
	v := r.str(key, "")
	if v != "" && !validURL(v) {
		r.fail(key, "must be an absolute URL")
	}
	return v
}

func validURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func (r *reader) positive(key, fallback string, hi int) int {
	raw := r.str(key, fallback)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > hi {
		r.fail(key, fmt.Sprintf("must be an integer between 1 and %d", hi))
		return 1
	}
	return n
}

func (r *reader) duration(key, fallback string, lo, hi time.Duration) time.Duration {
	raw := r.str(key, fallback)
	d, err := time.ParseDuration(raw)
	if err != nil || d < lo || d > hi {
		r.fail(key, fmt.Sprintf("must be a duration between %s and %s", lo, hi))
		return lo
	}
	return d
}

// optionalDuration returns zero when key is unset or "0".
func (r *reader) optionalDuration(key string, lo, hi time.Duration) time.Duration {
	if raw := r.str(key, "0"); raw == "0" {
		return 0
	}
	return r.duration(key, "0", lo, hi)
}

func (r *reader) ratio(key, fallback string) float64 {
	f, err := strconv.ParseFloat(r.str(key, fallback), 64)
	if err != nil || f < 0 || f > 1 {
		r.fail(key, "must be a number between 0 and 1")
		return 1
	}
	return f
}

func (r *reader) level(key, fallback string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(r.str(key, fallback))); err != nil {
		r.fail(key, "must be debug, info, warn or error")
		return slog.LevelInfo
	}
	return l
}

// providerQueues parses "provider-a=https://sqs/a,provider-b=https://sqs/b".
func (r *reader) providerQueues(key string) []ProviderQueue {
	raw := r.str(key, "")
	if strings.TrimSpace(raw) == "" {
		r.fail(key, "must list at least one provider queue as providerId=url")
		return nil
	}
	var out []ProviderQueue
	providers := map[string]struct{}{}
	urls := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		provider, queueURL, ok := strings.Cut(part, "=")
		provider, queueURL = strings.TrimSpace(provider), strings.TrimSpace(queueURL)
		switch {
		case !ok || provider == "" || !validURL(queueURL):
			r.fail(key, "must be providerId=url entries separated by commas")
			continue
		case hasKey(providers, provider):
			r.fail(key, fmt.Sprintf("repeats provider %q", provider))
			continue
		case hasKey(urls, queueURL):
			r.fail(key, "binds one queue to more than one provider")
			continue
		}
		providers[provider] = struct{}{}
		urls[queueURL] = struct{}{}
		out = append(out, ProviderQueue{ProviderID: provider, URL: queueURL})
	}
	if len(out) == 0 {
		r.fail(key, "must list at least one provider queue as providerId=url")
	}
	return out
}

func hasKey(m map[string]struct{}, key string) bool {
	_, ok := m[key]
	return ok
}

func (r *reader) list(key string) []string {
	var out []string
	for _, part := range strings.Split(r.str(key, ""), ",") {
		if p := strings.TrimSpace(part); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) roles(key, fallback string) Roles {
	var roles Roles
	for _, name := range r.list(key) {
		role := Role(name)
		if !slices.Contains(allRoles, role) {
			r.fail(key, fmt.Sprintf("has unknown role %q", name))
			continue
		}
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		if _, set := r.lookup(key); set {
			r.fail(key, "must enable at least one role")
		}
		for _, name := range strings.Split(fallback, ",") {
			roles = append(roles, Role(name))
		}
	}
	return roles
}
