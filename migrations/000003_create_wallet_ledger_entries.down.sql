DROP TRIGGER IF EXISTS wallets_ledger_consistency ON wallets;
DROP FUNCTION IF EXISTS wallet_ledger_consistency();
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP FUNCTION IF EXISTS ledger_chain_check();
DROP FUNCTION IF EXISTS ledger_append_only();
