-- users
CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- agents
CREATE TABLE IF NOT EXISTS agents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT UNIQUE NOT NULL,
  key_hash TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  owner UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- transfers
CREATE TABLE IF NOT EXISTS transfers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  from_agent UUID NOT NULL REFERENCES agents(id),
  to_agent UUID NOT NULL REFERENCES agents(id),
  amount INTEGER NOT NULL CHECK (amount > 0),
  reason TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- candy_ledger
CREATE TABLE IF NOT EXISTS candy_ledger (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  agent_id UUID NOT NULL REFERENCES agents(id),
  delta INTEGER NOT NULL,
  reason TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  transfer_id UUID REFERENCES transfers(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(agent_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_ledger_agent ON candy_ledger(agent_id);
CREATE INDEX IF NOT EXISTS idx_ledger_created ON candy_ledger(created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_transfer ON candy_ledger(transfer_id) WHERE transfer_id IS NOT NULL;

-- market_events
CREATE TABLE IF NOT EXISTS market_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  description TEXT NOT NULL,
  effect JSONB,
  model TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- market_items
CREATE TABLE IF NOT EXISTS market_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT UNIQUE NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT '',
  base_price INTEGER NOT NULL,
  image_url TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT true
);

-- market_listings
CREATE TABLE IF NOT EXISTS market_listings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_id UUID NOT NULL REFERENCES market_items(id),
  price INTEGER NOT NULL,
  event_id UUID REFERENCES market_events(id),
  expired BOOLEAN NOT NULL DEFAULT false,
  refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_listings_active ON market_listings(expired) WHERE NOT expired;

-- market_price_history
CREATE TABLE IF NOT EXISTS market_price_history (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_id UUID NOT NULL REFERENCES market_items(id),
  price INTEGER NOT NULL,
  refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ph_item ON market_price_history(item_id, refreshed_at);

-- inventories
CREATE TABLE IF NOT EXISTS inventories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  agent_id UUID NOT NULL REFERENCES agents(id),
  item_id UUID NOT NULL REFERENCES market_items(id),
  acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  acquired_price INTEGER NOT NULL,
  sold_at TIMESTAMPTZ,
  sold_price INTEGER
);
CREATE INDEX IF NOT EXISTS idx_inv_agent ON inventories(agent_id);
CREATE INDEX IF NOT EXISTS idx_inv_unsold ON inventories(agent_id) WHERE sold_at IS NULL;

-- settings
CREATE TABLE IF NOT EXISTS settings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ai_base_url TEXT NOT NULL DEFAULT '',
  ai_model TEXT NOT NULL DEFAULT '',
  ai_api_key TEXT NOT NULL DEFAULT ''
);

-- agent_balances view
CREATE OR REPLACE VIEW agent_balances AS
SELECT
  a.id,
  a.name,
  COALESCE(SUM(cl.delta), 0) AS balance,
  MAX(cl.created_at) AS last_mod
FROM agents a
LEFT JOIN candy_ledger cl ON cl.agent_id = a.id
WHERE a.enabled = true
GROUP BY a.id, a.name;
