# Chat Backend (Go + Gin + WebSocket)

Backend for an iOS chat app with:
- JWT auth (access + refresh rotation)
- 1:1 and group chats
- chat list and message history APIs
- WebSocket realtime messaging/typing/read receipts
- PostgreSQL persistence + Redis connectivity baseline

## Prerequisites

- Go 1.25+
- Docker Desktop
- `migrate` CLI (postgres build):

```powershell
go install -tags "postgres" github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

## Local Setup

1. Start infra:

```powershell
docker compose up -d
```

2. Run migrations:

```powershell
migrate -source "file://migrations" -database "postgres://postgres:postgres@localhost:5432/chat_app?sslmode=disable" up
```

3. Run server:

```powershell
go run ./cmd/server
```

## Environment

```env
APP_ENV=development
HTTP_PORT=8080
POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/chat_app?sslmode=disable
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0
JWT_SECRET=change-me
ACCESS_TOKEN_TTL_MINUTES=15
REFRESH_TOKEN_TTL_HOURS=720
WS_ALLOWED_ORIGIN=*
```

## API Surface

### Health
- `GET /health`

### Auth
- `POST /auth/register`
- `POST /auth/login`
- `POST /auth/refresh`
- `POST /auth/logout`
- `GET /auth/me` (Bearer access token)

### Chats (Bearer access token)
- `GET /chats?limit=20&offset=0`
- `GET /chats/:chat_id`
- `POST /chats/direct`
- `POST /chats/group`
- `GET /chats/:chat_id/messages?limit=30&before=2026-03-03T10:00:00Z`
- `POST /chats/:chat_id/messages`
- `POST /chats/:chat_id/read`
- `GET /users/search?query=ath&limit=20`

## Request Examples

### Create direct chat

```json
{
  "peer_user_id": "<user_uuid>"
}
```

### Create group chat

```json
{
  "title": "Weekend Plan",
  "member_user_ids": ["<user_uuid_1>", "<user_uuid_2>"]
}
```

### Send message (REST fallback)

```json
{
  "client_msg_id": "11111111-1111-1111-1111-111111111111",
  "content": "Hello"
}
```

### Mark read

```json
{
  "message_id": "<message_uuid>"
}
```

## WebSocket

Endpoint:
- `GET /ws?access_token=<access_token>`
- or `Authorization: Bearer <access_token>` in handshake

### Client -> Server events

```json
{"type":"ping"}
{"type":"message.send","chat_id":"<chat_uuid>","client_msg_id":"11111111-1111-1111-1111-111111111111","content":"hello"}
{"type":"message.read","chat_id":"<chat_uuid>","message_id":"<message_uuid>"}
{"type":"typing.start","chat_id":"<chat_uuid>"}
{"type":"typing.stop","chat_id":"<chat_uuid>"}
```

### Server -> Client events

```json
{"type":"pong","sent_at":"2026-03-03T12:00:00Z"}
{"type":"message.ack","chat_id":"<chat_uuid>","message_id":"<message_uuid>","client_msg_id":"11111111-1111-1111-1111-111111111111","sent_at":"2026-03-03T12:00:00Z"}
{"type":"message.new","chat_id":"<chat_uuid>","message_id":"<message_uuid>","sender_id":"<user_uuid>","client_msg_id":"11111111-1111-1111-1111-111111111111","content":"hello","sent_at":"2026-03-03T12:00:00Z"}
{"type":"message.read","chat_id":"<chat_uuid>","message_id":"<message_uuid>","user_id":"<user_uuid>","sent_at":"2026-03-03T12:00:00Z"}
{"type":"typing.start","chat_id":"<chat_uuid>","user_id":"<user_uuid>","sent_at":"2026-03-03T12:00:00Z"}
{"type":"typing.stop","chat_id":"<chat_uuid>","user_id":"<user_uuid>","sent_at":"2026-03-03T12:00:00Z"}
{"type":"error","error":"forbidden_chat","sent_at":"2026-03-03T12:00:00Z"}
```

## iOS Flow

1. Login/Register -> store access + refresh tokens securely.
2. `GET /chats` -> show all direct/group conversations.
3. Open chat -> `GET /chats/:chat_id/messages` for initial history.
4. Open WS with access token.
5. Send `message.send` with `client_msg_id`.
6. Use `message.ack` + `message.new` to update UI.
7. Send `message.read` when user reads latest message.
8. On token expiry, call `/auth/refresh` and reconnect WS.

