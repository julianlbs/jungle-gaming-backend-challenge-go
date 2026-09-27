CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    transaction_id UUID        REFERENCES wager_transactions (id),
    outcome        TEXT        NOT NULL CHECK (outcome IN ('PROCESSED', 'REJECTED', 'PENDING_REFERENCE', 'REPLAYED')),
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE FUNCTION inbox_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'inbox_messages is append-only: % rejected', TG_OP
        USING ERRCODE = 'restrict_violation';
END
$$;

CREATE TRIGGER inbox_no_update_delete
    BEFORE UPDATE OR DELETE ON inbox_messages
    FOR EACH ROW EXECUTE FUNCTION inbox_immutable();

CREATE TRIGGER inbox_no_truncate
    BEFORE TRUNCATE ON inbox_messages
    FOR EACH STATEMENT EXECUTE FUNCTION inbox_immutable();

CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,
    seq             BIGINT      GENERATED ALWAYS AS IDENTITY UNIQUE,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    partition_key   TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INT         NOT NULL CHECK (event_version >= 1),
    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,
    -- JSON rather than JSONB so the snapshot is republished byte for byte.
    payload         JSON        NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    last_error      TEXT,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX outbox_due_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;

CREATE FUNCTION outbox_guard() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION 'outbox_events cannot be truncated' USING ERRCODE = 'restrict_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.published_at IS NULL THEN
            RAISE EXCEPTION 'unpublished outbox event % cannot be deleted', OLD.id
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.seq IS DISTINCT FROM OLD.seq
       OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
       OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
       OR NEW.partition_key IS DISTINCT FROM OLD.partition_key
       OR NEW.event_type IS DISTINCT FROM OLD.event_type
       OR NEW.event_version IS DISTINCT FROM OLD.event_version
       OR NEW.correlation_id IS DISTINCT FROM OLD.correlation_id
       OR NEW.causation_id IS DISTINCT FROM OLD.causation_id
       OR NEW.payload::text IS DISTINCT FROM OLD.payload::text
       OR NEW.occurred_at IS DISTINCT FROM OLD.occurred_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'outbox event % content is immutable', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox event % is already published', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER outbox_guard
    BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_guard();

CREATE TRIGGER outbox_no_truncate
    BEFORE TRUNCATE ON outbox_events
    FOR EACH STATEMENT EXECUTE FUNCTION outbox_guard();
