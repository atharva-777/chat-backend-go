# Golang Chat Backend (Gin + WebSocket)

Backend foundation for an iOS chat app using:
- Gin (HTTP API)
- Gorilla WebSocket (real-time events)
- PostgreSQL (persistence)
- Redis (realtime/presence/cache foundation)

## Prerequisites

- Go 1.25+
- Docker Desktop
- `migrate` CLI with postgres driver:

```powershell
go install -tags "postgres" github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

## Local Setup

1. Start infra:

```powershell
docker compose up -d
```

2. Apply migrations:

```powershell
migrate -source "file://migrations" -database "postgres://postgres:postgres@localhost:5432/chat_app?sslmode=disable" up
```

3. Start server:

```powershell
go run ./cmd/server
```

Server starts on `http://localhost:8080` by default.

## Environment Variables

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

## HTTP Endpoints

- `GET /health`
  - Checks API, Postgres, and Redis.

- `POST /auth/register`
- `POST /auth/login`
- `POST /auth/refresh`
- `POST /auth/logout`
- `GET /auth/me` (requires `Authorization: Bearer <access_token>`)

## Auth Payload Examples

`POST /auth/register`

```json
{
  "email": "user@example.com",
  "username": "atharva",
  "password": "strongPassword123",
  "display_name": "Atharva"
}
```

`POST /auth/login`

```json
{
  "email": "user@example.com",
  "password": "strongPassword123"
}
```

`POST /auth/refresh`

```json
{
  "refresh_token": "<refresh_token>"
}
```

## WebSocket

- Endpoint: `GET /ws?access_token=<access_token>`
- Alternative: send `Authorization: Bearer <access_token>` in handshake headers.
- Authorization: for `message.send` and `typing.*`, sender must exist in `chat_members` for that `chat_id`.
- Broadcast scope: events are sent only to connected users who are members of that chat.

## WebSocket Event Contract (Current)

Client -> Server:

```json
{"type":"ping"}
{"type":"message.send","chat_id":"<chat_uuid>","client_msg_id":"msg-1","content":"hello"}
{"type":"typing.start","chat_id":"<chat_uuid>"}
{"type":"typing.stop","chat_id":"<chat_uuid>"}
```

Server -> Clients:

```json
{"type":"pong","sent_at":"2026-03-01T12:00:00Z"}
{"type":"message.new","chat_id":"<chat_uuid>","sender_id":"<user_uuid>","client_msg_id":"msg-1","content":"hello","sent_at":"2026-03-01T12:00:00Z"}
{"type":"typing.start","chat_id":"<chat_uuid>","sender_id":"<user_uuid>","sent_at":"2026-03-01T12:00:00Z"}
{"type":"typing.stop","chat_id":"<chat_uuid>","sender_id":"<user_uuid>","sent_at":"2026-03-01T12:00:00Z"}
{"type":"error","error":"forbidden_chat","sent_at":"2026-03-01T12:00:00Z"}
```
