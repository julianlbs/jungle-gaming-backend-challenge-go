ALTER TABLE wallets
    ADD CONSTRAINT wallets_id_player_currency_key UNIQUE (id, player_id, currency);

CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    kind                              TEXT        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status                            TEXT        NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    channel                           TEXT        NOT NULL CHECK (channel IN ('HTTP', 'SQS', 'INTERNAL')),
    wallet_id                         UUID        NOT NULL,
    player_id                         UUID        NOT NULL,
    currency                          CHAR(3)     NOT NULL,
    amount_minor                      BIGINT      NOT NULL CHECK (amount_minor >= 0),
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID REFERENCES wager_transactions (id),
    failure_code                      TEXT,
    failure_detail                    TEXT,
    result_balance_minor              BIGINT CHECK (result_balance_minor >= 0),
    result_wallet_version             BIGINT CHECK (result_wallet_version >= 1),
    attempts                          INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at                   TIMESTAMPTZ,
    reference_deadline_at             TIMESTAMPTZ,
    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    CONSTRAINT wager_tx_wallet_fk
        FOREIGN KEY (wallet_id, player_id, currency) REFERENCES wallets (id, player_id, currency),
    CONSTRAINT wager_tx_id_wallet_key UNIQUE (id, wallet_id),

    CONSTRAINT wager_tx_origin_shape CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING' AND channel = 'INTERNAL'
            AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING' AND channel IN ('HTTP', 'SQS')
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),
    CONSTRAINT wager_tx_opening_processed CHECK (kind <> 'OPENING' OR status = 'PROCESSED'),
    CONSTRAINT wager_tx_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),
    CONSTRAINT wager_tx_reference_by_kind CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind IN ('OPENING', 'BET', 'LOSS') AND reference_external_transaction_id IS NULL
            AND reference_transaction_id IS NULL)
        OR kind = 'WIN'
    ),
    CONSTRAINT wager_tx_no_self_reference CHECK (reference_transaction_id IS DISTINCT FROM id),
    CONSTRAINT wager_tx_failure_shape CHECK (
        (status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code <> 'PROCESSING_FAILED')
        OR (status = 'FAILED' AND failure_code = 'PROCESSING_FAILED')
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL)
    ),
    CONSTRAINT wager_tx_result_shape CHECK (
        (status IN ('PROCESSED', 'REJECTED')) =
        (result_balance_minor IS NOT NULL AND result_wallet_version IS NOT NULL)
    ),
    CONSTRAINT wager_tx_processed_reversal_resolved CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL
    ),
    CONSTRAINT wager_tx_completion_shape CHECK (
        (status IN ('PROCESSED', 'REJECTED', 'FAILED')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT wager_tx_schedule_shape CHECK (
        status <> 'PENDING_REFERENCE'
        OR (next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL)
    ),
    CONSTRAINT wager_tx_timestamps_check CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX wager_tx_provider_external_key
    ON wager_transactions (provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_provider_idempotency_key
    ON wager_transactions (provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_single_opening
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';
CREATE UNIQUE INDEX wager_tx_single_reversal
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

CREATE INDEX wager_tx_due_idx
    ON wager_transactions (next_attempt_at) WHERE status IN ('PENDING', 'PENDING_REFERENCE');
CREATE INDEX wager_tx_waiting_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_tx_wallet_idx
    ON wager_transactions (wallet_id, created_at);

CREATE FUNCTION wager_transactions_guard() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
        RAISE EXCEPTION 'wager transactions cannot be deleted (%)', TG_OP
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager transaction % is terminal (%)', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.origin IS DISTINCT FROM OLD.origin
       OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.channel IS DISTINCT FROM OLD.channel
       OR NEW.wallet_id IS DISTINCT FROM OLD.wallet_id
       OR NEW.player_id IS DISTINCT FROM OLD.player_id
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor
       OR NEW.provider_id IS DISTINCT FROM OLD.provider_id
       OR NEW.external_transaction_id IS DISTINCT FROM OLD.external_transaction_id
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.payload_hash IS DISTINCT FROM OLD.payload_hash
       OR NEW.round_id IS DISTINCT FROM OLD.round_id
       OR NEW.game_id IS DISTINCT FROM OLD.game_id
       OR NEW.reference_external_transaction_id IS DISTINCT FROM OLD.reference_external_transaction_id
       OR NEW.correlation_id IS DISTINCT FROM OLD.correlation_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'wager transaction % business fields are immutable', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER wager_transactions_guard
    BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();

CREATE TRIGGER wager_transactions_no_truncate
    BEFORE TRUNCATE ON wager_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION wager_transactions_guard();
