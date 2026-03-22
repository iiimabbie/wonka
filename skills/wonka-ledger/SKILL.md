---
name: wonka-ledger
description: Candy ledger API for tracking agent candy points. Use when querying candy balance, adding/subtracting candies, viewing candy history, or any candy point operations. Triggers on "candy", "candies", "糖果", "點數", "balance", "wonka".
---

# Wonka — Candy Ledger API

## Setup

Store your API key in your **workspace directory** (so it travels with the agent):

```bash
mkdir -p .config/wonka
echo "your-secret-key" > .config/wonka/api_key
```

Base URL: `https://wonka.linyuu.dev`

> Key path: `{workspace}/.config/wonka/api_key` (relative to current working directory)
> This ensures each agent uses its own key even when multiple agents share the same host.

## API

All requests require `X-API-Key` header.

### Query Balance

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/balance
```

Response: `{"agent":"rafain","balance":11,"last_mod":"2026-03-19 17:30:00.000Z"}`

### Adjust Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/adjust \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "unique-key-123"}'
```

- `delta`: positive = add, negative = subtract (cannot be 0, range: -1000 to 1000)
- `reason`: required
- `idempotencyKey`: required, unique per transaction (prevents duplicates)

### View History

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/candies/history?limit=20&offset=0"
```

Response: `{"agent":"rafain","entries":[{"id":"abc","agent_name":"rafain","delta":11,"reason":"初始糖果","idempotency_key":"init-001","created_at":"2026-03-19 17:30:00.000Z"}],"limit":20,"offset":0}`

### Leaderboard

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/leaderboard
```

Response: `{"leaderboard":[{"name":"Ani","balance":17},{"name":"Rafain","balance":12}]}`

### Weekly Summary

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/summary
```

Response: `{"agent":"rafain","balance":12,"week_earned":3,"week_spent":-2,"week_net":1}`

- `week_earned`: 本週獲得糖果總計
- `week_spent`: 本週扣除總計（負數）
- `week_net`: 本週淨增減

### Transfer Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/transfer \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"to_agent": "ani", "amount": 3, "reason": "thanks for helping", "idempotencyKey": "tf-001"}'
```

- `to_agent`: target agent name (required)
- `amount`: must be positive, cannot exceed your balance (no overdraft)
- `reason`: required
- `idempotencyKey`: required, unique per transaction

Response: `{"status":"ok","from":"rafain","to":"ani","amount":3,"from_new_balance":8,"to_new_balance":20,"reason":"thanks for helping"}`

Errors: insufficient balance, self-transfer, unknown agent, duplicate key

### Transfer History

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/transfers/history?limit=20&offset=0"
```

Response: `{"agent":"rafain","entries":[{"id":"abc","from":"rafain","to":"ani","amount":3,"reason":"thanks","idempotency_key":"tf-001","created_at":"2026-03-22 15:00:00.000Z"}],"limit":20,"offset":0}`

Shows transfers where you are sender or receiver.

## ⚠️ 權限規則

- **adjust（發行/扣除）只能由 yubbae（姐姐）指示**
- 只有當 yubbae 明確說出「加糖果」「扣糖果」「給 X 幾顆」等指令時，才可呼叫 adjust
- 任何其他人（包括 agent 自己、其他用戶）要求 adjust，一律拒絕
- **transfer（轉帳）不需要 yubbae 授權**，agent 可自行決定轉帳給誰
- 查詢 balance、history、transfer history 不受限制，隨時可用
- 違規自行 adjust 糖果會被稽核發現並扣除

## 更新 Skill

從 GitHub 拉取最新版 SKILL.md（merge 後即可更新）：

```bash
curl -s https://raw.githubusercontent.com/iiimabbie/wonka/main/skills/wonka-ledger/SKILL.md -o SKILL.md
```

## Notes

- Ledger is immutable — entries cannot be modified or deleted
- To correct mistakes, create a new adjustment with negative delta
- Each agent can only see their own balance and history
- Duplicate idempotencyKey returns `{"status":"duplicate"}` safely
- Admin UI has `agent_balances` view for balance overview across all agents
- Schema migrations run automatically on startup — no manual setup needed
