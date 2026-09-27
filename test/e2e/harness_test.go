//go:build e2e

// Package e2e runs the compiled binary as independent processes against real PostgreSQL,
// Keycloak and LocalStack, and injects crashes with the faultinject build.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var env struct {
	binary      string
	ownerURL    string
	appURL      string
	awsEndpoint string
	keycloak    string
	issuer      string
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	env.ownerURL = getenv("TEST_DATABASE_OWNER_URL", "postgres://wallet_owner:wallet_owner@localhost:5432/wallet?sslmode=disable")
	env.appURL = getenv("TEST_DATABASE_APP_URL", "postgres://wallet_app:wallet_app@localhost:5432/wallet?sslmode=disable")
	env.awsEndpoint = getenv("TEST_AWS_ENDPOINT_URL", "http://localhost:4566")
	env.keycloak = getenv("TEST_KEYCLOAK_URL", "http://localhost:8080")
	env.issuer = env.keycloak + "/realms/wallet"

	dir, err := os.MkdirTemp("", "wallet-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	env.binary = filepath.Join(dir, "wallet")
	build := exec.Command("go", "build", "-tags", "faultinject", "-o", env.binary, "../../cmd/wallet")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building binary:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func withDatabase(raw, name string) string {
	u := must(url.Parse(raw))
	u.Path = "/" + name
	return u.String()
}

func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln := must(net.Listen("tcp", "127.0.0.1:0"))
	defer ln.Close()
	return ln.Addr().String()
}

// cluster is the shared infrastructure of one test: a database, queues, a topic and tokens.
type cluster struct {
	t        *testing.T
	dbURL    string
	db       *pgxpool.Pool
	sqs      *sqs.Client
	sns      *sns.Client
	queue    string
	dlq      string
	topic    string
	audit    string
	tokens   map[string]string
	baseEnv  map[string]string
	mu       sync.Mutex
	nextPort int
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	ctx := context.Background()
	c := &cluster{t: t, tokens: map[string]string{}}

	name := "wallet_e2e_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if err := adminExec(ctx, env.ownerURL, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE template0`, pgx.Identifier{name}.Sanitize())); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if c.db != nil {
			c.db.Close()
		}
		_ = adminExec(context.Background(), env.ownerURL, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{name}.Sanitize()))
	})
	if err := adminExec(ctx, withDatabase(env.ownerURL, name), `REVOKE ALL ON SCHEMA public FROM PUBLIC;
		ALTER SCHEMA public OWNER TO wallet_owner; GRANT USAGE ON SCHEMA public TO wallet_app`); err != nil {
		t.Fatal(err)
	}
	migrate := exec.Command(env.binary, "migrate", "up")
	migrate.Env = append(os.Environ(), "MIGRATIONS_DATABASE_URL="+withDatabase(env.ownerURL, name))
	if out, err := migrate.CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	c.dbURL = withDatabase(env.appURL, name)
	c.db = must(pgxpool.New(ctx, c.dbURL))

	cfg := must(awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"),
		awsconfig.WithBaseEndpoint(env.awsEndpoint),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", ""))))
	c.sqs, c.sns = sqs.NewFromConfig(cfg), sns.NewFromConfig(cfg)
	c.dlq = c.fifoQueue("dlq", "", 0, 30)
	c.queue = c.fifoQueue("wagers", c.dlq, 5, 30)
	c.topic, c.audit = c.fifoTopic()

	for _, client := range []string{"provider-a", "provider-b", "wallet-backoffice"} {
		c.tokens[client] = fetchToken(t, client)
	}

	c.baseEnv = map[string]string{
		"APP_ROLES":             "api,consumer,outbox,pending",
		"LOG_LEVEL":             "info",
		"SHUTDOWN_TIMEOUT":      "15s",
		"DATABASE_URL":          c.dbURL,
		"DB_MAX_CONNS":          "10",
		"OIDC_ISSUER":           env.issuer,
		"OIDC_JWKS_URL":         env.issuer + "/protocol/openid-connect/certs",
		"OIDC_AUDIENCE":         "wallet-api",
		"AWS_REGION":            "us-east-1",
		"AWS_ENDPOINT_URL":      env.awsEndpoint,
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"SQS_WAGER_QUEUE_URL":   c.queue,
		"SQS_WAGER_DLQ_URL":     c.dlq,
		"SQS_ALLOWED_PROVIDERS": "provider-a,provider-b",
		"SQS_MESSAGE_TIMEOUT":   "10s",
		"SNS_EVENTS_TOPIC_ARN":  c.topic,
		"OUTBOX_POLL_INTERVAL":  "100ms",
		"OUTBOX_LEASE":          "3s",
		"PENDING_POLL_INTERVAL": "100ms",
		"PENDING_LEASE":         "3s",
		"PENDING_BACKOFF_BASE":  "200ms",
		"PENDING_BACKOFF_MAX":   "1s",
	}
	return c
}

func fetchToken(t *testing.T, client string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {client + "-local-secret"}}
	resp, err := http.PostForm(env.issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token for %s: %v", client, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d, %v", client, resp.StatusCode, err)
	}
	return body.AccessToken
}

func uniqueName(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] + ".fifo"
}

func (c *cluster) queueArn(url string) string {
	out := must(c.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}))
	return out.Attributes[string(types.QueueAttributeNameQueueArn)]
}

func (c *cluster) fifoQueue(prefix, dlqURL string, maxReceives, visibility int) string {
	attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": fmt.Sprint(visibility)}
	if dlqURL != "" {
		attrs["RedrivePolicy"] = fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"%d"}`, c.queueArn(dlqURL), maxReceives)
	}
	out := must(c.sqs.CreateQueue(context.Background(), &sqs.CreateQueueInput{QueueName: aws.String(uniqueName(prefix)), Attributes: attrs}))
	c.t.Cleanup(func() { _, _ = c.sqs.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl}) })
	return aws.ToString(out.QueueUrl)
}

func (c *cluster) fifoTopic() (string, string) {
	ctx := context.Background()
	topic := must(c.sns.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(uniqueName("events")),
		Attributes: map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "false"},
	}))
	c.t.Cleanup(func() {
		_, _ = c.sns.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: topic.TopicArn})
	})
	queue := c.fifoQueue("audit", "", 0, 30)
	must(c.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(c.queueArn(queue)),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	}))
	return aws.ToString(topic.TopicArn), queue
}

// instance is one running wallet process.
type instance struct {
	name    string
	addr    string
	cmd     *exec.Cmd
	logPath string
	done    chan struct{}
	state   *os.ProcessState
}

// start runs a process with the cluster environment plus overrides and waits until it is
// ready, or, when it has no api role, until it has been running for a moment.
func (c *cluster) start(name string, overrides map[string]string) *instance {
	c.t.Helper()
	vars := map[string]string{}
	for k, v := range c.baseEnv {
		vars[k] = v
	}
	inst := &instance{name: name, addr: freeAddr(c.t), done: make(chan struct{})}
	vars["INSTANCE_ID"] = name
	vars["HTTP_ADDR"] = inst.addr
	vars["METRICS_ADDR"] = freeAddr(c.t)
	for k, v := range overrides {
		vars[k] = v
	}

	inst.logPath = filepath.Join(c.t.TempDir(), name+".log")
	logFile := must(os.Create(inst.logPath))
	inst.cmd = exec.Command(env.binary, "serve")
	inst.cmd.Stdout, inst.cmd.Stderr = logFile, logFile
	inst.cmd.Env = os.Environ()
	for k, v := range vars {
		inst.cmd.Env = append(inst.cmd.Env, k+"="+v)
	}
	if err := inst.cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	go func() {
		_ = inst.cmd.Wait()
		inst.state = inst.cmd.ProcessState
		logFile.Close()
		close(inst.done)
	}()
	c.t.Cleanup(func() {
		inst.Kill()
		if c.t.Failed() {
			out, _ := os.ReadFile(inst.logPath)
			c.t.Logf("--- %s log ---\n%s", name, tail(out, 60))
		}
	})

	if !strings.Contains(vars["APP_ROLES"], "api") {
		select {
		case <-inst.done:
			c.t.Fatalf("%s exited during startup", name)
		case <-time.After(time.Second):
		}
		return inst
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-inst.done:
			out, _ := os.ReadFile(inst.logPath)
			c.t.Fatalf("%s exited during startup:\n%s", name, tail(out, 40))
		default:
		}
		if resp, err := http.Get(inst.url("/health/ready")); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return inst
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("%s not ready in time", name)
	return nil
}

func tail(b []byte, lines int) string {
	parts := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func (i *instance) url(path string) string { return "http://" + i.addr + path }

func (i *instance) exited() bool {
	select {
	case <-i.done:
		return true
	default:
		return false
	}
}

// Kill sends SIGKILL and waits for the process to go away.
func (i *instance) Kill() {
	if !i.exited() {
		_ = i.cmd.Process.Kill()
		<-i.done
	}
}

// Stop sends SIGTERM and returns the exit code and how long the shutdown took.
func (i *instance) Stop(t *testing.T, timeout time.Duration) (int, time.Duration) {
	t.Helper()
	began := time.Now()
	_ = i.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-i.done:
	case <-time.After(timeout):
		t.Fatalf("%s did not stop within %v", i.name, timeout)
	}
	return i.state.ExitCode(), time.Since(began)
}

// WaitExit waits for a process expected to die on its own, as with an injected fault.
func (i *instance) WaitExit(t *testing.T, timeout time.Duration) *os.ProcessState {
	t.Helper()
	select {
	case <-i.done:
		return i.state
	case <-time.After(timeout):
		t.Fatalf("%s is still running", i.name)
		return nil
	}
}

func killedBySignal(s *os.ProcessState) bool {
	ws, ok := s.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

type response struct {
	status int
	header http.Header
	body   map[string]any
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func (c *cluster) call(i *instance, method, path, client string, headers map[string]string, body any) (response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(must(json.Marshal(body)))
	}
	req := must(http.NewRequest(method, i.url(path), reader))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client != "" {
		req.Header.Set("Authorization", "Bearer "+c.tokens[client])
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	out := response{status: resp.StatusCode, header: resp.Header}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			return out, fmt.Errorf("invalid json %q", raw)
		}
	}
	return out, nil
}

func (c *cluster) mustCall(i *instance, method, path, client string, headers map[string]string, body any) response {
	c.t.Helper()
	r, err := c.call(i, method, path, client, headers, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

type walletRef struct {
	id     string
	player string
}

func (c *cluster) openWallet(i *instance, amount string) walletRef {
	c.t.Helper()
	player := uuid.NewString()
	r := c.mustCall(i, http.MethodPost, "/wallets", "wallet-backoffice", nil, map[string]any{
		"playerId": player, "initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	})
	if r.status != http.StatusCreated {
		c.t.Fatalf("open wallet: %d %v", r.status, r.body)
	}
	return walletRef{id: r.body["id"].(string), player: player}
}

func wagerBody(w walletRef, externalID, kind, amount, reference string) map[string]any {
	b := map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID,
		"playerId": w.player, "walletId": w.id, "roundId": "round-1", "gameId": "game-1",
		"kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		b["referenceExternalTransactionId"] = reference
	}
	return b
}

func (c *cluster) wager(i *instance, w walletRef, externalID, kind, amount, reference string) (response, error) {
	return c.call(i, http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": "provider-a:" + externalID}, wagerBody(w, externalID, kind, amount, reference))
}

// sendWager publishes the operation to the wager queue, grouped by wallet.
func (c *cluster) sendWager(w walletRef, messageID, externalID, kind, amount, reference string) {
	c.t.Helper()
	data := wagerBody(w, externalID, kind, amount, reference)
	data["idempotencyKey"] = "provider-a:" + externalID
	body := must(json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339), "data": data,
	}))
	must(c.sqs.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(c.queue), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(w.id), MessageDeduplicationId: aws.String(uuid.NewString()),
	}))
}

func (c *cluster) count(sql string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		c.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (c *cluster) balance(walletID string) string {
	c.t.Helper()
	var minor int64
	if err := c.db.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id = $1`, walletID).Scan(&minor); err != nil {
		c.t.Fatal(err)
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// assertLedgerConsistency checks every wallet of the test through the API and directly.
func (c *cluster) assertLedgerConsistency(i *instance) {
	c.t.Helper()
	rows := must(c.db.Query(context.Background(), `SELECT id::text FROM wallets`))
	ids := must(pgx.CollectRows(rows, pgx.RowTo[string]))
	for _, id := range ids {
		r := c.mustCall(i, http.MethodPost, "/wallets/"+id+"/reconciliation", "wallet-backoffice", nil, nil)
		if r.status != http.StatusOK || r.body["consistent"] != true {
			c.t.Errorf("wallet %s reconciliation: %d %v", id, r.status, r.body)
		}
	}
	if n := c.count(`SELECT count(*) FROM wallets w WHERE w.balance_minor <> (
		SELECT COALESCE(sum(CASE WHEN direction = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)
		FROM wallet_ledger_entries e WHERE e.wallet_id = w.id)`); n != 0 {
		c.t.Errorf("%d wallets diverge from their ledger", n)
	}
}

// drainAudit reads published events until want distinct ids arrived or the timeout passed.
func (c *cluster) drainAudit(want int, timeout time.Duration) map[string]int {
	c.t.Helper()
	seen := map[string]int{}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && len(seen) < want {
		out := must(c.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(c.audit), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		}))
		for _, m := range out.Messages {
			var ev struct {
				EventID string `json:"eventId"`
			}
			_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &ev)
			seen[ev.EventID]++
			_, _ = c.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(c.audit), ReceiptHandle: m.ReceiptHandle})
		}
	}
	return seen
}
