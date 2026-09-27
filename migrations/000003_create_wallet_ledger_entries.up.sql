CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL,
    direction            TEXT        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    currency             CHAR(3)     NOT NULL,
    amount_minor         BIGINT      NOT NULL CHECK (amount_minor > 0),
    balance_before_minor BIGINT      NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor  BIGINT      NOT NULL CHECK (balance_after_minor >= 0),
    wallet_version       BIGINT      NOT NULL CHECK (wallet_version >= 1),
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_wallet_fk
        FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT ledger_transaction_fk
        FOREIGN KEY (transaction_id, wallet_id) REFERENCES wager_transactions (id, wallet_id),
    CONSTRAINT ledger_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_wallet_version_key UNIQUE (wallet_id, wallet_version),
    CONSTRAINT ledger_arithmetic CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
        OR (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
    )
);

CREATE FUNCTION ledger_append_only() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % rejected', TG_OP
        USING ERRCODE = 'restrict_violation';
END
$$;

CREATE TRIGGER ledger_no_update_delete
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

CREATE TRIGGER ledger_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_append_only();

-- Each entry must continue from the balance left by the previous one.
CREATE FUNCTION ledger_chain_check() RETURNS trigger
    LANGUAGE plpgsql AS
$$
DECLARE
    prev_version BIGINT;
    prev_balance BIGINT;
BEGIN
    SELECT wallet_version, balance_after_minor
      INTO prev_version, prev_balance
      FROM wallet_ledger_entries
     WHERE wallet_id = NEW.wallet_id
     ORDER BY wallet_version DESC
     LIMIT 1;
    IF FOUND THEN
        IF NEW.wallet_version <= prev_version OR NEW.balance_before_minor <> prev_balance THEN
            RAISE EXCEPTION 'ledger chain broken for wallet % at version %', NEW.wallet_id, NEW.wallet_version
                USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.balance_before_minor <> 0 THEN
        RAISE EXCEPTION 'first ledger entry of wallet % must start from zero', NEW.wallet_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_chain
    BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_chain_check();

-- Checked at commit: every stored wallet balance must match a ledger entry for its version.
CREATE FUNCTION wallet_ledger_consistency() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    IF NEW.version = 1 AND NEW.balance_minor = 0 THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1
          FROM wallet_ledger_entries
         WHERE wallet_id = NEW.id
           AND wallet_version = NEW.version
           AND balance_after_minor = NEW.balance_minor
    ) THEN
        RAISE EXCEPTION 'wallet % version % has no matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER wallets_ledger_consistency
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_consistency();
