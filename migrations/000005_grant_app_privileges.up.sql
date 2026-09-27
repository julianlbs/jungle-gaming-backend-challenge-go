DO
$$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        GRANT USAGE ON SCHEMA public TO wallet_app;
        GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions TO wallet_app;
        GRANT SELECT, INSERT ON wallet_ledger_entries, inbox_messages TO wallet_app;
        GRANT SELECT, INSERT, UPDATE, DELETE ON outbox_events TO wallet_app;
        GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO wallet_app;
    END IF;
END
$$;
