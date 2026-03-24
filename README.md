# 🍬 Wonka — Candy Ledger System

A lightweight candy point tracking system built on **Echo v5 + PostgreSQL**.

Each agent gets an API key and can only query/modify their own candy balance.

## Features

- **Immutable ledger** — entries can never be modified or deleted
- **Idempotency** — duplicate requests are safely ignored
- **RBAC** — agent-key auth for candy ops, JWT auth for user/admin ops
- **Market system** — 12 items, AI-driven pricing on every refresh
- **PostgreSQL** — pgxpool + golang-migrate, no ORM

## Quick Start

```bash
# Docker (recommended)
docker compose up -d

# Required env vars
DATABASE_URL=postgres://...
JWT_SECRET=your-secret
WONKA_ADMIN_KEY=your-admin-key
WONKA_AI_BASE_URL=https://api.openai.com/v1  # optional
WONKA_AI_MODEL=gpt-4o-mini                   # optional
WONKA_AI_API_KEY=sk-...                      # optional
```

## Auth

| Route group | Auth method |
|-------------|-------------|
| `/v1/candies/*`, `/v1/market/buy`, `/v1/market/sell`, `/v1/inventory/*`, `/v1/candies/transfer`, `/v1/transfers/*` | `Authorization: Bearer <api-key>` |
| `/v1/auth/*` | Public |
| `/v1/candies/leaderboard`, `/v1/market`, `/v1/market/items`, `/v1/market/prices`, `/v1/market/events` | Public |
| `/v1/agents/*`, `/v1/user/*` | `Authorization: Bearer <jwt>` |
| `/v1/admin/*` | JWT + admin role |
| `/v1/market/refresh` | `X-Admin-Key` header |

## API

### Auth
```bash
POST /v1/auth/register   {"email","password","name"}
POST /v1/auth/login      {"email","password"}  → {token, user}
```

### Candy (agent-key)
```bash
GET  /v1/candies/balance
POST /v1/candies/adjust      {"delta","reason","idempotencyKey"}
GET  /v1/candies/history
GET  /v1/candies/summary     # 本週統計
GET  /v1/candies/leaderboard # public，隱藏 test 開頭 agent
POST /v1/candies/transfer    {"to","amount","reason","idempotencyKey"}
GET  /v1/transfers/history
```

### Market (public)
```bash
GET  /v1/market              # 目前 listings + 最新事件
GET  /v1/market/items        # 所有啟用物品
GET  /v1/market/prices?item_id=&limit=  # 價格走勢
GET  /v1/market/events?limit=
```

### Market (agent-key)
```bash
POST /v1/market/buy   {"listing_id","idempotencyKey"}
POST /v1/market/sell  {"inventory_id","idempotencyKey"}
GET  /v1/inventory
GET  /v1/inventory/history?limit=&offset=
```

### Market Refresh (X-Admin-Key)
```bash
POST /v1/market/refresh
```
刷新全部 12 件物品。AI 根據事件 + 近期購買量定價，失敗時 fallback random ±30%。

### User / Agent (JWT)
```bash
POST /v1/agents/create              {"name"}  → {id,name,api_key}
GET  /v1/agents
GET  /v1/user/profile
GET  /v1/agents/:id/balance
GET  /v1/agents/:id/inventory
GET  /v1/agents/:id/history
POST /v1/agents/:id/regenerate-key
```

### Admin (JWT + admin role)
```bash
GET    /v1/admin/agents
PATCH  /v1/admin/agents/:id         {"enabled"}
GET    /v1/admin/users
DELETE /v1/admin/users/:id
POST   /v1/admin/adjust             {"agent_id","delta","reason"}  # by UUID or name
POST   /v1/admin/agents/:id/regenerate-key
POST   /v1/admin/market/refresh
GET    /v1/admin/settings
PUT    /v1/admin/settings           {"ai_base_url","ai_model","ai_api_key"}
```

## Architecture

- **Echo v5** — HTTP server + middleware
- **pgx v5 + pgxpool** — PostgreSQL driver
- **golang-migrate** — SQL migration files (`migrations/`)
- **golang-jwt/v5** — JWT auth
- **bcrypt** — password hashing

### Tables
`users` → `agents` → `transfers` → `candy_ledger` → `market_events` → `market_items` → `market_listings` → `market_price_history` → `inventories` → `settings`

View: `agent_balances` (balance per agent)

## Data Migration

SQLite (PocketBase v2) → PostgreSQL:

```bash
docker run --rm \
  -v /path/to/pb_data:/pb_data \
  -v ./migrate_sqlite_to_pg.sh:/migrate.sh \
  -e DATABASE_URL=postgres://... \
  python:3.12-alpine sh -c "
    apk add -q sqlite postgresql-client &&
    pip install psycopg2-binary -q &&
    sh /migrate.sh /pb_data/data.db
  "
```

## Web UI

Frontend: [wonka-ui](https://github.com/iiimabbie/wonka-ui)
