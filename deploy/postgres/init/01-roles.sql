-- Local development roles. wallet_owner owns the schema and runs migrations;
-- wallet_app is the runtime role and never owns tables, so it cannot disable
-- triggers, alter the schema or bypass the ledger protections.
CREATE ROLE wallet_owner LOGIN CREATEDB PASSWORD 'wallet_owner';
CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';

ALTER ROLE wallet_app SET idle_in_transaction_session_timeout = '15s';

CREATE DATABASE wallet OWNER wallet_owner;

\connect wallet

REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO wallet_owner;
GRANT USAGE ON SCHEMA public TO wallet_app;
