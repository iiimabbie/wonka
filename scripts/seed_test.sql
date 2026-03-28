-- Seed data for wonka test environment
-- Only enabled items from production (the ones that matter for testing)

-- Test user (admin)
INSERT INTO users (id, email, password_hash, name, role) VALUES
  ('00000000-0000-0000-0000-000000000001', 'test@test.com',
   '$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012', 'TestAdmin', 'admin')
ON CONFLICT (email) DO NOTHING;

-- Test agents with known API keys
-- key: test-key-alpha  → sha256: d1a9c70d...
-- key: test-key-beta   → sha256: 03883373...
-- key: test-key-gamma  → sha256: 7c6f5e97...
INSERT INTO agents (id, name, key_hash, enabled, owner) VALUES
  ('a0000000-0000-0000-0000-000000000001', '測試員A',
   'd1a9c70d19c81f247d9a6c57b2a6bb48212cc202e49a432e16025a9d5d3fa8d3', true,
   '00000000-0000-0000-0000-000000000001'),
  ('a0000000-0000-0000-0000-000000000002', '測試員B',
   '038833737202aaf8dd73da38fc2bdef7b37ac9dffb7832e626094221bd84421d', true,
   '00000000-0000-0000-0000-000000000001'),
  ('a0000000-0000-0000-0000-000000000003', '測試員C',
   '7c6f5e9756cd1b2017873abf43720a9c25c59dd8888c63d10c9b79d2fbfd3e01', true,
   '00000000-0000-0000-0000-000000000001')
ON CONFLICT (name) DO NOTHING;

-- Give each agent 100 candies
INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key) VALUES
  ('a0000000-0000-0000-0000-000000000001', 100, 'seed balance', 'seed-alpha'),
  ('a0000000-0000-0000-0000-000000000002', 100, 'seed balance', 'seed-beta'),
  ('a0000000-0000-0000-0000-000000000003', 100, 'seed balance', 'seed-gamma')
ON CONFLICT (agent_id, idempotency_key) DO NOTHING;

-- Market items (enabled only, from production)
-- Test anchor prices are intentionally different from production
INSERT INTO market_items (name, description, type, anchor_price, image_url, enabled) VALUES
  ('星空糖霜', '撒在任何食物上都會變得 ✨ aesthetic ✨，Instagram 必備', '劇情', 15, '', true),
  ('時光果凍', '吃了會短暫回到過去的記憶', '劇情', 22, '', true),
  ('月光糖漿', '月圓才能請 sub_agent 採集，所以供應量永遠不夠', '劇情', 18, '', true),
  ('泡泡糖氣球', '吹出來的泡泡可以當交通工具', '功能性', 13, '', true),
  ('稀有寶石', '沒人知道它為什麼在糖果店賣，他會閃閃發光地看著你', '劇情', 20, '', true),
  ('記憶麵包', '把知識印在上面吃掉就學會', '功能性', 17, '', true),
  ('變形藥水', '哈利波特──混血王子的背叛', '功能性', 11, '', true),
  ('迷幻蘑菇餅', '吃了會看到奇幻世界（無害的）', '劇情', 14, '', true),
  ('隱形軟糖', '吃了會透明十分鐘，但衣服不會，使用時請記算好時間', '功能性', 25, '', true),
  ('雷電跳跳糖', '在舌頭上劈啪作響的帶電跳跳糖', '收藏', 6, '', true),
  ('黃金太妃糖', '傳說是用真金打造的太妃糖', '收藏', 19, '', true),
  ('龍息辣糖', '吃完嘴巴會噴火三秒鐘', '功能性', 9, '', true)
ON CONFLICT (name) DO NOTHING;

-- AI settings
-- Test AI config placeholder (actual values will be overwritten by ENV in main.go)
INSERT INTO settings (ai_base_url, ai_model, ai_api_key)
SELECT '', '', ''
WHERE NOT EXISTS (SELECT 1 FROM settings);
