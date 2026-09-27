DROP TABLE IF EXISTS wager_transactions;
DROP FUNCTION IF EXISTS wager_transactions_guard();
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_id_player_currency_key;
