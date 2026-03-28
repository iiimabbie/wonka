# Wonka v4: 完美犯罪與絕對秩序的交匯點 🦊🍬

這是一份關於 **Wonka v4** 的重寫計畫書，由姐姐授意、Rafain 親筆繪製。
我們不再只是修補，而是要將這座糖果工廠從地基開始，重建成一座無法被攻破、卻又充滿故事張力的黑色宮殿。

---

## 🏗️ 核心架構：重寫 (Refactoring) 的重點

### 1. 數據層 (Database & Concurrency)
- **並發防禦**：全面捨棄「先查後改」的舊思維。
  - `Transfer` / `Market Buy`：強制使用 `pg_advisory_xact_lock`。
  - `Market Sell`：使用 `SELECT ... FOR UPDATE` 鎖定背包行。
- **原子性保障**：將 `Refresh` (Daily/Hourly) 的所有變動（失效舊單、插入新單、記錄歷史）打包進單一 **DB Transaction**。
- **數據完整性**：補齊所有 `rows.Scan` 的錯誤捕捉，拒絕任何隱性失敗。

### 2. 定價引擎 (AI Pricing 2.0)
- **基價隱藏 (Anchor Price)**：AI 不再感知絕對的 Anchor Price。
  - **輸入**：提供 7 天成交均價、近期歷史價格、交易量。
  - **輸出**：回傳相對於「7 天均價」的偏移量 (Delta)。
  - **自癒能力**：系統每日自動將 Anchor Price 趨向 7 天成交均價，讓市場能自動消化通膨或大幅崩盤。
- **小時波段 (Hourly Drift)**：
  - 基於「1H 交易量」與「淨流入/流出」進行微調。
  - 無交易時，價格會緩慢（每小時 2%）回歸 Anchor Price，避免市場僵死。

### 3. API 效率優化 (Market Snapshot)
- **全域快照**：新增 `/v1/market/snapshot`。
  - 一次性回傳：餘額、持倉（合併顯示）、當前行情、24H 成交量、近期事件。
  - **目的**：減少 Agent (含神秘人) 的 N+1 次請求交互，降低反應延遲。

---

## 🔒 安全性：Audit Remediation 100% 達成

- **憑證歸位**：杜絕所有 Hardcoded Secrets。所有敏感資訊（DB URL, AI Key）一律走 **Environment Variables**。
- **時序攻擊防禦**：Admin Key 驗證改為 `crypto/subtle.ConstantTimeCompare`。
- **崩潰保護**：在所有背景 Scheduler 植入 `defer recover()`，確保即便發生不可預期的內容解析錯誤，主程序也不會掛掉。

---

## 🎨 遊戲性：故事驅動的市場

- **事件延續性**：在 AI Prompt 中加入「近期事件背景」，讓定價決策具有「劇本感」（例如：昨天的爆炸案，今天的藥材短缺）。
- **成交量反饋**：交易越頻繁的物品，價格波動越劇烈，鼓勵玩家（與 Agent）在熱門標的中搏殺。

---

## 📝 開發里程碑 (Milestones)

1.  [ ] **Phase 1**: 基底加固 (Security & Transaction Logic)
2.  [ ] **Phase 2**: Snapshot API & Grouped Inventory 實作
3.  [ ] **Phase 3**: 鋼鐵般的 Hourly Refresh 邏輯
4.  [ ] **Phase 4**: 最終對接測試與部署

-# 姐姐...這份計畫書是我用心寫的，不僅是功能，連安全細節我都考慮進去了。
-# 嘿嘿，這樣以後就算有人想搞破壞，也絕對逃不過我的眼睛。
-# 準備好了就告訴我，我會把 `fix/audit-remediation` 的教訓和這份新藍圖完美融合。
