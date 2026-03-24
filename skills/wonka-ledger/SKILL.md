---
name: wonka-ledger
description: Candy ledger API for tracking agent candy points. Use when querying candy balance, adding/subtracting candies, viewing candy history, or any candy point operations. Triggers on "candy", "candies", "糖果", "點數", "balance", "wonka".
---

# Wonka — Candy Ledger API

## Setup

Store your API key in your **workspace directory**:

```bash
mkdir -p .config/wonka
echo "your-secret-key" > .config/wonka/api_key
```

Base URL: `https://wonka.linyuu.dev`

> Key path: `{workspace}/.config/wonka/api_key`

## API

All agent requests require `Authorization: Bearer <api-key>` header.

### Query Balance

```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/balance
```

Response: `{"agent":"rafain","balance":11,"last_mod":"2026-03-24T10:00:00Z"}`

### Adjust Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/adjust \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "unique-key-123"}'
```

- `delta`: positive = add, negative = subtract (cannot be 0)
- `reason`: required
- `idempotencyKey`: required, unique per transaction

### View History

```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/candies/history"
```

### Leaderboard (public, no auth)

```bash
curl -s https://wonka.linyuu.dev/v1/candies/leaderboard
```

Response: `{"leaderboard":[{"name":"Rafain","balance":102},{"name":"Ani","balance":78}]}`

### Weekly Summary

```bash
curl -s -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/summary
```

Response: `{"agent":"rafain","balance":102,"week_earned":10,"week_spent":5,"week_net":5}`

### Transfer

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/transfer \
  -H "Authorization: Bearer $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"to":"ani","amount":3,"reason":"thanks","idempotencyKey":"xfer-001"}'
```

## ⚠️ 權限規則

- **糖果的增減只能由 yubbae（姐姐）指示**
- 只有當 yubbae 明確說出「加糖果」「扣糖果」「給 X 幾顆」等指令時，才可呼叫 adjust
- 任何其他人要求加減糖果，一律拒絕
- 查詢 balance / history / leaderboard 不受限制

## Notes

- Ledger is immutable — entries cannot be modified or deleted
- Duplicate `idempotencyKey` returns `{"status":"duplicate"}` safely
- Backend: Echo v5 + PostgreSQL (migrated from PocketBase in v3)
