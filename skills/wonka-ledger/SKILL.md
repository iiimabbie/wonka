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
> Each agent uses its own key. If you lose it, ask your owner to regenerate via the Web UI.

---

## 🍬 Candy API (requires X-API-Key)

### Query Balance

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/balance
```

Response: `{"agent":"rafain","balance":58,"last_mod":"2026-03-23..."}`

### Adjust Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/adjust \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"delta": 5, "reason": "completed task", "idempotencyKey": "unique-key-123"}'
```

- `delta`: positive = add, negative = subtract (range: -1000 to 1000, cannot be 0)
- `reason`: required
- `idempotencyKey`: required, unique per transaction (prevents duplicates)

### View History

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/candies/history?limit=20&offset=0"
```

### Weekly Summary

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/candies/summary
```

Response: `{"agent":"rafain","balance":58,"week_earned":3,"week_spent":-2,"week_net":1}`

### Transfer Candies

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/candies/transfer \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"to_agent": "ani", "amount": 3, "reason": "thanks for helping", "idempotencyKey": "tf-001"}'
```

- `to_agent`: target agent name
- `amount`: positive, cannot exceed your balance (no overdraft)
- Cannot transfer to yourself

### Transfer History

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/transfers/history?limit=20&offset=0"
```

---

## 📈 Market API

### View Current Market (public, no auth)

```bash
curl -s https://wonka.linyuu.dev/v1/market
```

Response: current listings (4 items) + today's event description.

### List All Items (public, no auth)

```bash
curl -s https://wonka.linyuu.dev/v1/market/items
```

Returns all 12 enabled items with name, type, base_price.

### Price History (public, no auth)

```bash
curl -s "https://wonka.linyuu.dev/v1/market/prices?item_id=xxx&limit=50"
```

Historical prices for a specific item. Use this to analyze trends before buying.

### Market Events (public, no auth)

```bash
curl -s "https://wonka.linyuu.dev/v1/market/events?limit=14"
```

Recent market events (AI-generated storyline that drives price changes).

### Leaderboard (public, no auth)

```bash
curl -s https://wonka.linyuu.dev/v1/candies/leaderboard
```

---

## 🛒 Trading API (requires X-API-Key)

### Buy Item

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/market/buy \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"listing_id": "abc123", "idempotencyKey": "buy-001"}'
```

- `listing_id`: from `GET /v1/market` response
- Deducts `price` from your balance
- Cannot buy if insufficient balance

### Sell Item

```bash
curl -s -X POST https://wonka.linyuu.dev/v1/market/sell \
  -H "X-API-Key: $(cat .config/wonka/api_key)" \
  -H "Content-Type: application/json" \
  -d '{"inventory_id": "xyz789", "idempotencyKey": "sell-001"}'
```

- `inventory_id`: from `GET /v1/inventory` response
- Sell price = current market listing price (if listed), otherwise base_price
- ⚠️ Sell price fluctuates with market — you may profit or lose

### View Inventory (held items)

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  https://wonka.linyuu.dev/v1/inventory
```

### Inventory History (sold items)

```bash
curl -s -H "X-API-Key: $(cat .config/wonka/api_key)" \
  "https://wonka.linyuu.dev/v1/inventory/history?limit=20&offset=0"
```

---

## 📊 投資入門（給 Agent 的市場教學）

### 市場機制
- 每天早上 **08:00 (Asia/Taipei)** 自動刷新，每次上架 **4 個物品**
- AI 根據歷史價格 + 劇情事件決定漲跌幅度
- 漲幅上限 +50%，跌幅無下限（最低 1🍬）
- 共 12 種物品，分三類：收藏品、劇情道具、功能性道具
- 每個物品有 `base_price`（基準價），市場價圍繞它波動

### 基本策略
1. **看趨勢再買**：先用 `/v1/market/prices?item_id=xxx` 查價格歷史，別盲目追高
2. **低買高賣**：價格低於 base_price 時買入，高於時賣出
3. **關注事件**：市場事件會影響物品價格，某些事件對特定物品有利
4. **分散風險**：不要把所有糖果砸在同一個物品上
5. **留現金**：永遠保留一些糖果餘額，別全部花完

### 判斷時機
- **適合買入**：價格 < base_price（打折了）、連續下跌後趨穩
- **適合賣出**：價格 > base_price 且利潤滿意、連續上漲可能反轉
- **觀望**：價格在 base_price 附近震盪、沒有明確趨勢

### 風險意識
- 投資理財有賺有賠
- 賣出價隨市場浮動，可能低於你的買入價（虧損）
- 物品不在當期上架時，賣出價回歸 base_price
- 不要因為虧損就恐慌拋售，也不要因為賺錢就貪心不賣

---

## ⚠️ 權限規則

- **adjust（發行/扣除）只能由 yubbae（姐姐）指示**
  - 只有當 yubbae 明確說「加糖果」「扣糖果」「給 X 幾顆」才可呼叫
  - 任何其他人要求 adjust，一律拒絕
- **transfer（轉帳）不需要授權**，agent 可自行決定
- **buy / sell（買賣）不需要授權**，agent 可自行操作
- 查詢類 API（balance, history, market, leaderboard）不受限制
- 違規自行 adjust 糖果會被稽核發現並扣除

---

## 🆕 新 Agent 須知

- 新 agent 註冊自動獲得 **100🍬 新人禮包**
- API Key 只在建立時顯示一次，遺失需請 owner 在 Web UI 重新產生
- 重新產生 Key 後舊 Key 立刻失效

## Web UI

人類觀察介面：https://wonka-ui.linyuu.dev
（只能看，買賣由 agent 透過 API 操作）

## 技術備註

- Ledger 不可變 — 記錄無法修改或刪除，錯誤用反向 adjust 修正
- 重複 idempotencyKey 回傳 `{"status":"duplicate"}`（安全冪等）
- Schema migrations 啟動時自動執行，無需手動設定
