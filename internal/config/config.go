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

type SQS struct {
	QueueURL         string
	DLQURL           string
	AllowedProviders []string
	MaxInFlight      int
	MessageTimeout   time.Duration
}

type SNS struct {
	TopicARN string
}

type Outbox struct {
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
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
			MaxConns:         int32(r.integer("DB_MAX_CONNS", "20", 1, 1000)),
			LockTimeout:      r.duration("DB_LOCK_TIMEOUT", "2s", 10*time.Millisecond, time.Minute),
			StatementTimeout: r.duration("DB_STATEMENT_TIMEOUT", "5s", 10*time.Millisecond, 5*time.Minute),
			StartupTimeout:   r.duration("DB_STARTUP_TIMEOUT", "30s", time.Second, 10*time.Minute),
		},
		AWS: AWS{
			Region:      r.str("AWS_REGION", "us-east-1"),
			EndpointURL: r.optionalURL("AWS_ENDPOINT_URL"),
		},
		Outbox: Outbox{
			BatchSize:    r.integer("OUTBOX_BATCH_SIZE", "50", 1, 1000),
			Lease:        r.duration("OUTBOX_LEASE", "30s", time.Second, time.Hour),
			PollInterval: r.duration("OUTBOX_POLL_INTERVAL", "250ms", 10*time.Millisecond, time.Minute),
		},
		Pending: Pending{
			PollInterval: r.duration("PENDING_POLL_INTERVAL", "500ms", 10*time.Millisecond, time.Minute),
			Lease:        r.duration("PENDING_LEASE", "30s", time.Second, time.Hour),
			BatchSize:    r.integer("PENDING_BATCH_SIZE", "50", 1, 1000),
			BackoffBase:  r.duration("PENDING_BACKOFF_BASE", "2s", time.Millisecond, time.Hour),
			BackoffMax:   r.duration("PENDING_BACKOFF_MAX", "5m", time.Millisecond, 24*time.Hour),
			MaxAttempts:  r.integer("PENDING_MAX_ATTEMPTS", "10", 1, 10000),
			TTL:          r.duration("PENDING_REFERENCE_TTL", "30m", 100*time.Millisecond, 7*24*time.Hour),
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
			QueueURL:         r.requiredURL("SQS_WAGER_QUEUE_URL"),
			DLQURL:           r.requiredURL("SQS_WAGER_DLQ_URL"),
			AllowedProviders: r.list("SQS_ALLOWED_PROVIDERS"),
			MaxInFlight:      r.integer("SQS_MAX_IN_FLIGHT", "10", 1, 100),
			MessageTimeout:   r.duration("SQS_MESSAGE_TIMEOUT", "30s", time.Second, 10*time.Minute),
		}
		if len(cfg.SQS.AllowedProviders) == 0 {
			r.fail("SQS_ALLOWED_PROVIDERS", "must list at least one provider")
		}
	}
	if cfg.Roles.Has(RoleOutbox) {
		cfg.SNS = SNS{TopicARN: r.required("SNS_EVENTS_TOPIC_ARN")}
		if arn := cfg.SNS.TopicARN; arn != "" && !strings.HasPrefix(arn, "arn:") {
			r.fail("SNS_EVENTS_TOPIC_ARN", "must be an ARN")
		}
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

func (r *reader) integer(key, fallback string, lo, hi int) int {
	raw := r.str(key, fallback)
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		r.fail(key, fmt.Sprintf("must be an integer between %d and %d", lo, hi))
		return lo
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

func (r *reader) level(key, fallback string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(r.str(key, fallback))); err != nil {
		r.fail(key, "must be debug, info, warn or error")
		return slog.LevelInfo
	}
	return l
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
