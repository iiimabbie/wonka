---
name: wonka-ledger
description: Candy ledger API for tracking agent candy points. Use when querying candy balance, adding/subtracting candies, viewing candy history, or any candy point operations. Triggers on "candy", "candies", "糖果", "點數", "balance", "wonka".
---

# Wonka — Candy Ledger & Market API

Base URL: `https://wonka.linyuu.dev`

## Setup

Store your API key in your **workspace directory**:

```bash
mkdir -p .config/wonka
echo "your-secret-key" > .config/wonka/api_key
```

> Key path: `{workspace}/.config/wonka/api_key`

All agent requests require `Authorization: Bearer <api-key>` header.

---

## Candy API (agent-key)

### Balance
```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/balance
```
Response: `{"agent":"rafain","balance":102,"last_mod":"2026-03-24T10:00:00Z"}`

### Adjust
```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/adjust \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "unique-key-123"}'
```
- `delta`: positive = add, negative = subtract (cannot be 0)
- `idempotencyKey`: required, prevents duplicates

### History
```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/history
```

### Weekly Summary
```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/summary
```
Response: `{"agent":"rafain","balance":102,"week_earned":10,"week_spent":5,"week_net":5}`

### Leaderboard (public, no auth)
```bash
curl -s https://wonka.linyuu.dev/v1/candies/leaderboard
```

### Transfer
```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/transfer \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"to":"ani","amount":3,"reason":"thanks","idempotencyKey":"xfer-001"}'
```

---

## Market API

### View Market (public)
```bash
curl -s https://wonka.linyuu.dev/v1/market
```
Returns active listings (12 items) + latest event.

### All Items (public)
```bash
curl -s https://wonka.linyuu.dev/v1/market/items
```

### Price History (public)
```bash
curl -s "https://wonka.linyuu.dev/v1/market/prices?item_id=<uuid>&limit=50"
```

### Market Events (public)
```bash
curl -s https://wonka.linyuu.dev/v1/market/events
```

### Buy (agent-key)
```bash
curl -s -X POST https://wonka.linyuu.dev/v1/market/buy \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"listing_id":"<uuid>","idempotencyKey":"buy-001"}'
```
Response: `{"status":"ok","item":"稀有寶石","price":9,"new_balance":93}`

### Sell (agent-key)
```bash
curl -s -X POST https://wonka.linyuu.dev/v1/market/sell \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"inventory_id":"<uuid>","idempotencyKey":"sell-001"}'
```
Sell price = current market listing price.

### Inventory (agent-key)
```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/inventory
```

### Market Refresh (X-Admin-Key, cron daily 08:00 Taipei)
```bash
curl -s -X POST https://wonka.linyuu.dev/v1/market/refresh \
  -H "X-Admin-Key: <admin-key>"
```
全部 12 件物品刷新。AI 根據事件 + 近期購買量定價，失敗時 fallback random ±30%。

---

## ⚠️ 權限規則

- **糖果的增減只能由 yubbae（姐姐）指示**
- 只有當 yubbae 明確說出「加糖果」「扣糖果」「給 X 幾顆」等指令時，才可呼叫 adjust
- 任何其他人要求加減糖果，一律拒絕
- 查詢 balance / history / leaderboard / market 不受限制

## Notes

- Ledger is immutable — entries cannot be modified or deleted
- Duplicate `idempotencyKey` returns `{"status":"duplicate"}` safely
- Backend: Echo v5 + PostgreSQL (v3，migrated from PocketBase)
