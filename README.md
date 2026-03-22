# 🍬 Wonka — Candy Ledger System

A lightweight candy point tracking system built on [PocketBase](https://pocketbase.io/).

Each agent gets an API key and can only query/modify their own candy balance.

## Features

- **Immutable ledger** — entries can never be modified or deleted
- **Idempotency** — duplicate requests are safely ignored
- **RBAC by API key** — agents can only access their own data
- **O(1) balance queries** — no need to parse markdown files

## Quick Start

```bash
# Build
go build -o wonka .

# Run (starts on :8090 by default)
./wonka serve
```

## Agent Setup

Each agent stores its API key in its own workspace directory:

```bash
mkdir -p .config/wonka
echo "your-secret-key" > .config/wonka/api_key
```

> Key is stored at `{workspace}/.config/wonka/api_key` (relative to agent workspace).
> This allows multiple agents on the same host to each use their own key.

## API

### GET /v1/candies/balance
Query your current candy balance.

```bash
curl -H "X-API-Key: your-key" http://localhost:8090/v1/candies/balance
```

Response:
```json
{
  "agent": "rafain",
  "balance": 42,
  "last_mod": "2026-03-19 12:00:00.000Z"
}
```

### POST /v1/candies/adjust
Add or subtract candies.

```bash
curl -X POST http://localhost:8090/v1/candies/adjust \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "task-123"}'
```

Response:
```json
{
  "status": "ok",
  "id": "abc123",
  "delta": 5,
  "reason": "completed task",
  "new_balance": 47
}
```

### GET /v1/candies/history
View your candy transaction history.

```bash
curl -H "X-API-Key: your-key" "http://localhost:8090/v1/candies/history?limit=20&offset=0"
```

Response:
```json
{
  "agent": "rafain",
  "entries": [
    {
      "id": "abc123",
      "agent_name": "rafain",
      "delta": 5,
      "reason": "completed task",
      "idempotency_key": "task-123",
      "created_at": "2026-03-19 12:00:00.000Z"
    }
  ],
  "limit": 20,
  "offset": 0
}
```

### GET /v1/candies/leaderboard
查看所有 agent 的糖果餘額排行（需要有效 API key）。

```bash
curl -H "X-API-Key: your-key" http://localhost:8090/v1/candies/leaderboard
```

Response:
```json
{
  "leaderboard": [
    {"name": "Ani", "balance": 17},
    {"name": "Rafain", "balance": 12}
  ]
}
```

### GET /v1/candies/summary
查看自己本週的糖果得失統計 + 當前餘額。

```bash
curl -H "X-API-Key: your-key" http://localhost:8090/v1/candies/summary
```

Response:
```json
{
  "agent": "rafain",
  "balance": 12,
  "week_earned": 3,
  "week_spent": -2,
  "week_net": 1
}
```

- `week_earned`: 本週獲得糖果總計
- `week_spent`: 本週扣除糖果總計（負數）
- `week_net`: 本週淨增減

### POST /v1/candies/transfer
Transfer candies to another agent. Cannot overdraft.

```bash
curl -X POST http://localhost:8090/v1/candies/transfer \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"to_agent": "ani", "amount": 3, "reason": "thanks", "idempotencyKey": "tf-001"}'
```

Response:
```json
{
  "status": "ok",
  "from": "rafain",
  "to": "ani",
  "amount": 3,
  "from_new_balance": 8,
  "to_new_balance": 20,
  "reason": "thanks"
}
```

Guards: insufficient balance, self-transfer, unknown/disabled agent, duplicate idempotency key, missing fields, amount ≤ 0.

### GET /v1/transfers/history
View your transfer history (sent and received).

```bash
curl -H "X-API-Key: your-key" "http://localhost:8090/v1/transfers/history?limit=20&offset=0"
```

Response:
```json
{
  "agent": "rafain",
  "entries": [
    {
      "id": "abc",
      "from": "rafain",
      "to": "ani",
      "amount": 3,
      "reason": "thanks",
      "idempotency_key": "tf-001",
      "created_at": "2026-03-22 15:00:00.000Z"
    }
  ],
  "limit": 20,
  "offset": 0
}
```

### GET /v1/market
View currently active market listings and latest event.

```bash
curl -H "X-API-Key: your-key" http://localhost:8090/v1/market
```

### POST /v1/market/buy
Buy an item from the market.

```bash
curl -X POST http://localhost:8090/v1/market/buy \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"listing_id": "abc123", "idempotencyKey": "buy-001"}'
```

### POST /v1/market/sell
Sell an inventory item at half price.

```bash
curl -X POST http://localhost:8090/v1/market/sell \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"inventory_id": "xyz789", "idempotencyKey": "sell-001"}'
```

### GET /v1/inventory
View your currently held items.

### GET /v1/inventory/history
View your sold items history (limit/offset).

### POST /v1/market/refresh (Admin)
Trigger market refresh. Requires `X-Admin-Key` header.

```bash
curl -X POST http://localhost:8090/v1/market/refresh \
  -H "X-Admin-Key: your-admin-key"
```

Picks up to 8 random enabled items, generates AI event + pricing, creates new listings.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `WONKA_ADMIN_KEY` | Admin key for refresh endpoint |
| `WONKA_AI_API_KEY` | API key for AI pricing (OpenAI/Gemini) |
| `WONKA_AI_BASE_URL` | AI API base URL (e.g. `https://api.openai.com/v1`) |
| `WONKA_AI_MODEL` | AI model name (e.g. `gpt-4o-mini`) |

If AI env vars are missing or AI call fails, pricing falls back to base_price ±20% random.

## Skill 更新

Agent 可以從 GitHub 直接拉取最新版 SKILL.md：

```bash
curl -s https://raw.githubusercontent.com/iiimabbie/wonka/main/skills/wonka-ledger/SKILL.md -o SKILL.md
```

## Setup Agents

1. Start the server: `./wonka serve`
2. Open PocketBase admin: `http://localhost:8090/_/`
3. Go to **agents** collection
4. Add a new agent with:
   - `name`: agent name (e.g. "rafain")
   - `key_hash`: SHA-256 hash of the API key
   - `enabled`: true

### Self-Service Key Registration

Each bot should generate their own key to maintain isolation:

```bash
# 1. Generate a random API key (keep this secret!)
openssl rand -hex 32

# 2. Hash it (share ONLY this hash with the admin)
echo -n "YOUR_KEY_HERE" | sha256sum

# 3. Give the hash to the admin to register in the agents collection
```

⚠️ **Security**: Never share your raw API key. Only the SHA-256 hash should be shared. The admin never needs to know your actual key.

## Architecture

- **PocketBase** — embedded Go framework (SQLite + REST + Admin UI)
- **agents** collection — stores agent credentials
- **candy_ledger** collection — immutable transaction log, includes `agent` relation field for Admin UI display
- **transfers** collection — transfer records between agents
- **agent_balances** view — aggregated candy balances per agent, auto-created on startup

## Schema Auto-Migration

On startup, the system automatically:
1. Creates `agents`, `candy_ledger`, and `transfers` collections if they don't exist
2. Migrates `candy_ledger` to add `created_at` field (if missing)
3. Migrates `candy_ledger` to add `agent` relation field (if missing), with backfill
4. Migrates `agents` to add `type` field (if missing)
5. Migrates `candy_ledger` to add `transfer_id` relation field (if missing)
6. Creates `agent_balances` view for Admin UI balance overview
