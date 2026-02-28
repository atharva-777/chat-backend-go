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

## Endpoints

- `GET /health`
  - Checks API, Postgres, and Redis.
- `GET /ws?user_id=<your_user_id>`
  - Upgrades to WebSocket.

## WebSocket Event Contract (MVP)

Client -> Server:

```json
{"type":"ping"}
{"type":"message.send","chat_id":"chat-1","client_msg_id":"msg-1","content":"hello"}
{"type":"typing.start","chat_id":"chat-1"}
{"type":"typing.stop","chat_id":"chat-1"}
```

Server -> Clients:

```json
{"type":"pong","sent_at":"2026-02-28T12:00:00Z"}
{"type":"message.new","chat_id":"chat-1","sender_id":"u1","client_msg_id":"msg-1","content":"hello","sent_at":"2026-02-28T12:00:00Z"}
{"type":"typing.start","chat_id":"chat-1","sender_id":"u1","sent_at":"2026-02-28T12:00:00Z"}
{"type":"typing.stop","chat_id":"chat-1","sender_id":"u1","sent_at":"2026-02-28T12:00:00Z"}
{"type":"error","error":"unsupported_event_type","sent_at":"2026-02-28T12:00:00Z"}
```
