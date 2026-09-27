DROP TABLE IF EXISTS outbox_events;
DROP FUNCTION IF EXISTS outbox_guard();
DROP TABLE IF EXISTS inbox_messages;
DROP FUNCTION IF EXISTS inbox_immutable();
