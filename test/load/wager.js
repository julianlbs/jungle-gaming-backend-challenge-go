// Load test for POST /wagering/transactions across the three compose instances.
// Run with `make load-test`; see README.md for the parameters and the methodology.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const RATE = Number(__ENV.LOAD_RATE || 200);
const DURATION = __ENV.LOAD_DURATION || '60s';
const WALLETS = Number(__ENV.LOAD_WALLETS || 200);
const HOT_SHARE = Number(__ENV.LOAD_HOT_SHARE || 0.1);
const REPLAY_SHARE = Number(__ENV.LOAD_REPLAY_SHARE || 0.05);
const KEYCLOAK = __ENV.KEYCLOAK_URL || 'http://localhost:8080';
const APIS = (__ENV.API_URLS || 'http://localhost:8081,http://localhost:8082,http://localhost:8083').split(',');
const METRICS = (__ENV.METRICS_URLS || 'http://localhost:9091,http://localhost:9092,http://localhost:9093').split(',');
const DRAIN_TIMEOUT_S = Number(__ENV.LOAD_DRAIN_TIMEOUT_S || 120);

const wagerDuration = new Trend('wager_duration', true);
const unexpected = new Rate('wager_unexpected');
const processed = new Counter('wager_processed');
const rejected = new Counter('wager_rejected');
const unavailable = new Counter('wager_unavailable');
const idempotencyConflicts = new Counter('wager_idempotency_conflicts');
const replays = new Counter('wager_replays');
const outboxAge = new Trend('outbox_oldest_pending_age', true);
const outboxPending = new Trend('outbox_pending_events');
const outboxDrain = new Trend('outbox_drain_time', true);
const serverConflicts = new Counter('server_concurrency_conflicts');
const serverRetries = new Counter('server_db_retries');
const consistent = new Rate('reconciliation_consistent');

export const options = {
  scenarios: {
    wagers: {
      executor: 'constant-arrival-rate',
      exec: 'wager',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.max(20, RATE / 2),
      maxVUs: RATE * 2,
    },
    outbox: {
      executor: 'constant-vus',
      exec: 'sampleOutbox',
      vus: 1,
      duration: DURATION,
    },
    drain: {
      executor: 'per-vu-iterations',
      exec: 'drainOutbox',
      vus: 1,
      iterations: 1,
      startTime: DURATION,
      maxDuration: `${DRAIN_TIMEOUT_S + 30}s`,
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    wager_unexpected: ['rate<0.01'],
    dropped_iterations: ['count==0'],
    outbox_drain_time: [`max<${DRAIN_TIMEOUT_S * 1000}`],
    reconciliation_consistent: ['rate==1'],
  },
};

function token(client) {
  const res = http.post(`${KEYCLOAK}/realms/wallet/protocol/openid-connect/token`, {
    grant_type: 'client_credentials',
    client_id: client,
    client_secret: `${client}-local-secret`,
  });
  if (res.status !== 200) {
    throw new Error(`token for ${client}: ${res.status}`);
  }
  return res.json('access_token');
}

function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function scrape() {
  const totals = {};
  for (const url of METRICS) {
    const res = http.get(`${url}/metrics`, { tags: { name: 'metrics' } });
    if (res.status !== 200) {
      throw new Error(`metrics ${url}: ${res.status}`);
    }
    for (const line of res.body.split('\n')) {
      const m = line.match(/^(outbox_oldest_pending_age_seconds|outbox_pending_events|wallet_concurrency_conflicts_total|db_transaction_retries_total)(\{[^}]*\})? ([0-9.e+-]+)$/);
      if (m) {
        const name = m[1];
        const value = Number(m[3]);
        // Every instance reports the same shared backlog; counters are per instance.
        totals[name] = name.startsWith('outbox_') ? Math.max(totals[name] || 0, value) : (totals[name] || 0) + value;
      }
    }
  }
  return totals;
}

export function setup() {
  for (const c of [processed, rejected, unavailable, idempotencyConflicts, replays]) {
    c.add(0);
  }
  const backoffice = token('wallet-backoffice');
  const wallets = [];
  for (let i = 0; i < WALLETS + 1; i++) {
    const playerId = uuid();
    const res = http.post(`${APIS[i % APIS.length]}/wallets`,
      JSON.stringify({ playerId, initialBalance: { amount: '1000000.00', currency: 'BRL' } }),
      { headers: { Authorization: `Bearer ${backoffice}`, 'Content-Type': 'application/json' }, tags: { name: 'setup' } });
    if (res.status !== 201) {
      throw new Error(`open wallet: ${res.status} ${res.body}`);
    }
    wallets.push({ id: res.json('id'), playerId });
  }
  return { hot: wallets[0], wallets: wallets.slice(1), baseline: scrape(), run: uuid().slice(0, 8) };
}

// Per VU: the previous request, resent as an idempotent replay, and a provider token renewed
// before the 300 s lifespan of the realm ends.
const wagerState = { last: null, token: null, tokenAt: 0 };

function providerToken() {
  if (wagerState.token === null || Date.now() - wagerState.tokenAt > 240000) {
    wagerState.token = token('provider-a');
    wagerState.tokenAt = Date.now();
  }
  return wagerState.token;
}

export function wager(data) {
  const replay = wagerState.last !== null && Math.random() < REPLAY_SHARE;
  let body = replay ? wagerState.last.body : null;
  let key = replay ? wagerState.last.key : null;
  if (!replay) {
    const wallet = Math.random() < HOT_SHARE ? data.hot : data.wallets[Math.floor(Math.random() * data.wallets.length)];
    const ext = `load-${data.run}-${__VU}-${__ITER}`;
    key = `provider-a:${ext}`;
    body = JSON.stringify({
      providerId: 'provider-a',
      externalTransactionId: ext,
      playerId: wallet.playerId,
      walletId: wallet.id,
      roundId: `round-${__VU}-${__ITER}`,
      gameId: 'load-test',
      kind: 'BET',
      money: { amount: '0.01', currency: 'BRL' },
    });
  }
  const res = http.post(`${APIS[__ITER % APIS.length]}/wagering/transactions`, body, {
    headers: { Authorization: `Bearer ${providerToken()}`, 'Content-Type': 'application/json', 'Idempotency-Key': key },
    tags: { name: 'POST /wagering/transactions' },
  });
  wagerState.last = { body, key };
  wagerDuration.add(res.timings.duration);

  switch (res.status) {
    case 200:
      if (res.json('idempotentReplay')) {
        replays.add(1);
      } else {
        processed.add(1);
      }
      unexpected.add(false);
      break;
    case 422:
      rejected.add(1);
      unexpected.add(false);
      break;
    case 409:
      idempotencyConflicts.add(1);
      unexpected.add(true);
      break;
    case 503:
      unavailable.add(1);
      unexpected.add(true);
      break;
    default:
      unexpected.add(true);
  }
  check(res, { 'processed or rejected': (r) => r.status === 200 || r.status === 422 });
}

export function sampleOutbox() {
  const m = scrape();
  outboxAge.add((m.outbox_oldest_pending_age_seconds || 0) * 1000);
  outboxPending.add(m.outbox_pending_events || 0);
  sleep(1);
}

export function drainOutbox(data) {
  const start = Date.now();
  let m = scrape();
  while ((m.outbox_pending_events || 0) > 0 && Date.now() - start < DRAIN_TIMEOUT_S * 1000) {
    sleep(0.5);
    m = scrape();
  }
  outboxDrain.add(Date.now() - start);
  serverConflicts.add((m.wallet_concurrency_conflicts_total || 0) - (data.baseline.wallet_concurrency_conflicts_total || 0));
  serverRetries.add((m.db_transaction_retries_total || 0) - (data.baseline.db_transaction_retries_total || 0));

  const backoffice = token('wallet-backoffice');
  for (const wallet of [data.hot, ...data.wallets.slice(0, 20)]) {
    const res = http.post(`${APIS[0]}/wallets/${wallet.id}/reconciliation`, null,
      { headers: { Authorization: `Bearer ${backoffice}` }, tags: { name: 'reconciliation' } });
    consistent.add(res.status === 200 && res.json('consistent') === true);
  }
}
