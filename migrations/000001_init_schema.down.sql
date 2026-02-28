DROP INDEX IF EXISTS idx_chats_last_message_at;
DROP INDEX IF EXISTS idx_chat_members_user;
DROP INDEX IF EXISTS idx_messages_sender_sent_at;
DROP INDEX IF EXISTS idx_messages_chat_sent_at;
DROP INDEX IF EXISTS idx_messages_client_idempotency;

DROP TABLE IF EXISTS message_reads;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS chat_members;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS users;
