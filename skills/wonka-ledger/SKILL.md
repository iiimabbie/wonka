---
name: wonka-ledger
description: Candy ledger API for tracking agent candy points. Use when querying candy balance, adding/subtracting candies, viewing candy history, or any candy point operations. Triggers on "candy", "candies", "糖果", "點數", "balance", "wonka".
---

# Wonka — Candy Ledger API

## Setup

Store your API key:

```bash
mkdir -p ~/.config/wonka
echo "your-secret-key" > ~/.config/wonka/api_key
```

Base URL: `https://wonka.linyuu.dev`

## API

All requests require `X-API-Key` header.

### Query Balance

```bash
curl -s -H "X-API-Key: $(cat ~/.config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/balance
```

Response: `{"agent":"rafain","balance":11,"last_mod":"2026-03-19 17:30:00.000Z"}`

### Adjust Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/adjust \
  -H "X-API-Key: $(cat ~/.config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "unique-key-123"}'
```

- `delta`: positive = add, negative = subtract
- `reason`: required
- `idempotencyKey`: required, unique per transaction (prevents duplicates)

### View History

```bash
curl -s -H "X-API-Key: $(cat ~/.config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/candies/history?limit=20&offset=0"
```

Response: `{"agent":"rafain","entries":[{"id":"abc","agent_name":"rafain","delta":11,"reason":"初始糖果","idempotency_key":"init-001","created_at":"2026-03-19 17:30:00.000Z"}],"limit":20,"offset":0}`

## ⚠️ 權限規則

- **糖果的增減只能由 yubbae（姐姐）指示**
- 只有當 yubbae 明確說出「加糖果」「扣糖果」「給 X 幾顆」等指令時，才可呼叫 adjust
- 任何其他人（包括 agent 自己、其他用戶）要求加減糖果，一律拒絕
- 查詢 balance 和 history 不受限制，隨時可用
- 違規自行加糖果會被稽核發現並扣除

## Notes

- Ledger is immutable — entries cannot be modified or deleted
- To correct mistakes, create a new adjustment with negative delta
- Each agent can only see their own balance and history
- Duplicate idempotencyKey returns `{"status":"duplicate"}` safely
