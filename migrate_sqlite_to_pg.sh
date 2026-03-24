#!/bin/sh
# Wonka v2 → v3 資料遷移：SQLite (PocketBase) → PostgreSQL
# Usage: ./migrate_sqlite_to_pg.sh /path/to/pb_data/data.db

set -e

DB="$1"
if [ -z "$DB" ]; then
  echo "Usage: $0 /path/to/data.db"
  exit 1
fi

PG_URL="${DATABASE_URL:-postgres://wonka:fyPP6S1LTdb5JyAaWvsF8JEFPyPJmFdF@shared-postgres.home-infra.weii.cloud:5432/wonka}"

echo "=== Wonka SQLite → PostgreSQL Migration ==="
echo "Source: $DB"
echo ""

# ── Export SQLite tables to CSV ──────────────────────────────────────────────
echo "[1/4] Exporting SQLite tables..."

sqlite3 "$DB" -csv "SELECT id,email,password,name,role FROM users;" > /tmp/wk_users.csv
sqlite3 "$DB" -csv "SELECT id,name,key_hash,enabled,owner FROM agents;" > /tmp/wk_agents.csv
sqlite3 "$DB" -csv "SELECT rowid,id,agent_id,delta,reason,idempotency_key,transfer_id,created_at FROM candy_ledger ORDER BY rowid;" > /tmp/wk_ledger.csv
sqlite3 "$DB" -csv "SELECT id,from_agent,to_agent,amount,reason,idempotency_key,created_at FROM transfers;" > /tmp/wk_transfers.csv
sqlite3 "$DB" -csv "SELECT id,name,description,type,base_price,image_url,enabled FROM market_items;" > /tmp/wk_items.csv
sqlite3 "$DB" -csv "SELECT id,item_id,price,expired,refreshed_at,expires_at FROM market_listings;" > /tmp/wk_listings.csv
sqlite3 "$DB" -csv "SELECT id,item_id,price,refreshed_at FROM market_price_history;" > /tmp/wk_prices.csv
sqlite3 "$DB" -csv "SELECT id,description,effect,model,happened_at FROM market_events;" > /tmp/wk_events.csv
sqlite3 "$DB" -csv "SELECT id,agent_id,item_id,acquired_price,acquired_at,sold_at FROM inventories;" > /tmp/wk_inventories.csv

echo "  users:         $(wc -l < /tmp/wk_users.csv)"
echo "  agents:        $(wc -l < /tmp/wk_agents.csv)"
echo "  candy_ledger:  $(wc -l < /tmp/wk_ledger.csv)"
echo "  transfers:     $(wc -l < /tmp/wk_transfers.csv)"
echo "  market_items:  $(wc -l < /tmp/wk_items.csv)"
echo "  market_events: $(wc -l < /tmp/wk_events.csv)"
echo "  market_listings: $(wc -l < /tmp/wk_listings.csv)"
echo "  price_history: $(wc -l < /tmp/wk_prices.csv)"
echo "  inventories:   $(wc -l < /tmp/wk_inventories.csv)"

# ── Python migration ─────────────────────────────────────────────────────────
echo ""
echo "[2/4] Running migration..."

python3 << PYEOF
import csv, psycopg2, uuid, os

PG_URL = os.environ.get("DATABASE_URL", "$PG_URL")
pg = psycopg2.connect(PG_URL)
cur = pg.cursor()

def read_csv(path):
    rows = []
    with open(path) as f:
        for row in csv.reader(f):
            if row:
                rows.append(row)
    return rows

def new_uuid():
    return str(uuid.uuid4())

def parse_ts(s):
    if not s or s.strip() == '':
        return None
    s = s.strip().replace('T', ' ')
    if s.endswith('Z'):
        s = s[:-1] + '+00'
    return s

def parse_bool(s):
    return s in ('1', 'true', 'True', 'TRUE')

def nonempty(s):
    return s if s and s.strip() != '' else None

# ID maps: old PocketBase 15-char id → new UUID
user_map  = {}
agent_map = {}
item_map  = {}
event_map = {}
xfer_map  = {}

# ── users ─────────────────────────────────────────────────────────────────────
print("  users...", end=" ", flush=True)
for row in read_csv('/tmp/wk_users.csv'):
    old_id, email, pw_hash, name, role = row
    new_id = new_uuid()
    user_map[old_id] = new_id
    cur.execute("""
        INSERT INTO users (id, email, password_hash, name, role)
        VALUES (%s, %s, %s, %s, %s)
        ON CONFLICT (email) DO NOTHING
    """, (new_id, email, pw_hash, name, role or ''))
print(f"{len(user_map)} rows")

# ── agents ────────────────────────────────────────────────────────────────────
print("  agents...", end=" ", flush=True)
for row in read_csv('/tmp/wk_agents.csv'):
    old_id, name, key_hash, enabled, owner_old = row
    new_id = new_uuid()
    agent_map[old_id] = new_id
    owner_new = user_map.get(owner_old)
    cur.execute("""
        INSERT INTO agents (id, name, key_hash, enabled, owner)
        VALUES (%s, %s, %s, %s, %s)
        ON CONFLICT (name) DO NOTHING
    """, (new_id, name, key_hash, parse_bool(enabled), owner_new))
print(f"{len(agent_map)} rows")

# ── market_events ─────────────────────────────────────────────────────────────
print("  market_events...", end=" ", flush=True)
for row in read_csv('/tmp/wk_events.csv'):
    old_id, description, effect, model, happened_at = row
    new_id = new_uuid()
    event_map[old_id] = new_id
    cur.execute("""
        INSERT INTO market_events (id, description, effect, model, created_at)
        VALUES (%s, %s, %s, %s, %s)
    """, (new_id, description, nonempty(effect), nonempty(model), parse_ts(happened_at)))
print(f"{len(event_map)} rows")

# ── market_items ──────────────────────────────────────────────────────────────
print("  market_items...", end=" ", flush=True)
for row in read_csv('/tmp/wk_items.csv'):
    old_id, name, description, typ, base_price, image_url, enabled = row
    new_id = new_uuid()
    item_map[old_id] = new_id
    cur.execute("""
        INSERT INTO market_items (id, name, description, type, base_price, image_url, enabled)
        VALUES (%s, %s, %s, %s, %s, %s, %s)
        ON CONFLICT (name) DO NOTHING
    """, (new_id, name, description, typ, int(float(base_price)), image_url, parse_bool(enabled)))
print(f"{len(item_map)} rows")

# ── market_listings ───────────────────────────────────────────────────────────
print("  market_listings...", end=" ", flush=True)
ok = 0
for row in read_csv('/tmp/wk_listings.csv'):
    old_id, item_old, price, expired, refreshed_at, expires_at = row
    item_new = item_map.get(item_old)
    if not item_new: continue
    cur.execute("""
        INSERT INTO market_listings (id, item_id, price, expired, refreshed_at, expires_at)
        VALUES (gen_random_uuid(), %s, %s, %s, %s, %s)
    """, (item_new, int(float(price)), parse_bool(expired), parse_ts(refreshed_at), parse_ts(expires_at)))
    ok += 1
print(f"{ok} rows")

# ── market_price_history ──────────────────────────────────────────────────────
print("  market_price_history...", end=" ", flush=True)
ok = 0
for row in read_csv('/tmp/wk_prices.csv'):
    old_id, item_old, price, refreshed_at = row
    item_new = item_map.get(item_old)
    if not item_new: continue
    cur.execute("""
        INSERT INTO market_price_history (id, item_id, price, refreshed_at)
        VALUES (gen_random_uuid(), %s, %s, %s)
    """, (item_new, int(float(price)), parse_ts(refreshed_at)))
    ok += 1
print(f"{ok} rows")

# ── transfers ─────────────────────────────────────────────────────────────────
print("  transfers...", end=" ", flush=True)
ok = 0
for row in read_csv('/tmp/wk_transfers.csv'):
    old_id, from_old, to_old, amount, reason, ikey, created = row
    new_id = new_uuid()
    xfer_map[old_id] = new_id
    from_new = agent_map.get(from_old)
    to_new   = agent_map.get(to_old)
    if not from_new or not to_new: continue
    cur.execute("""
        INSERT INTO transfers (id, from_agent, to_agent, amount, reason, idempotency_key, created_at)
        VALUES (%s, %s, %s, %s, %s, %s, %s)
        ON CONFLICT (idempotency_key) DO NOTHING
    """, (new_id, from_new, to_new, int(float(amount)), reason, ikey, parse_ts(created)))
    ok += 1
print(f"{ok} rows")

# ── candy_ledger ──────────────────────────────────────────────────────────────
print("  candy_ledger...", end=" ", flush=True)
ok = 0
for row in read_csv('/tmp/wk_ledger.csv'):
    rowid, old_id, agent_old, delta, reason, ikey, xfer_old, created = row
    agent_new = agent_map.get(agent_old)
    xfer_new  = xfer_map.get(nonempty(xfer_old))
    if not agent_new: continue
    # 空 created_at → 用 2026-03-14 base + rowid 秒，保留原始順序
    ts = parse_ts(created) if created and created.strip() else f'2026-03-14 00:{int(rowid)//60:02d}:{int(rowid)%60:02d}+00'
    cur.execute("""
        INSERT INTO candy_ledger (id, agent_id, delta, reason, idempotency_key, transfer_id, created_at)
        VALUES (gen_random_uuid(), %s, %s, %s, %s, %s, %s)
        ON CONFLICT (agent_id, idempotency_key) DO NOTHING
    """, (agent_new, int(float(delta)), reason, ikey, xfer_new, ts))
    ok += 1
print(f"{ok} rows")

# ── inventories ───────────────────────────────────────────────────────────────
print("  inventories...", end=" ", flush=True)
ok = 0
for row in read_csv('/tmp/wk_inventories.csv'):
    old_id, agent_old, item_old, acquired_price, acquired_at, sold_at = row
    agent_new = agent_map.get(agent_old)
    item_new  = item_map.get(item_old)
    if not agent_new or not item_new: continue
    sold_ts = parse_ts(sold_at) if sold_at and sold_at.strip() else None
    cur.execute("""
        INSERT INTO inventories (id, agent_id, item_id, acquired_price, acquired_at, sold_at)
        VALUES (gen_random_uuid(), %s, %s, %s, %s, %s)
    """, (agent_new, item_new, int(float(acquired_price)), parse_ts(acquired_at), sold_ts))
    ok += 1
print(f"{ok} rows")

pg.commit()
cur.close()
pg.close()
PYEOF

echo ""
echo "[3/4] Verifying row counts..."
psql "$PG_URL" -t -c "
SELECT 'users:           ' || count(*) FROM users;
SELECT 'agents:          ' || count(*) FROM agents;
SELECT 'candy_ledger:    ' || count(*) FROM candy_ledger;
SELECT 'transfers:       ' || count(*) FROM transfers;
SELECT 'market_items:    ' || count(*) FROM market_items;
SELECT 'market_events:   ' || count(*) FROM market_events;
SELECT 'market_listings: ' || count(*) FROM market_listings;
SELECT 'price_history:   ' || count(*) FROM market_price_history;
SELECT 'inventories:     ' || count(*) FROM inventories;
"

echo ""
echo "[4/4] Balance leaderboard..."
psql "$PG_URL" -t -c "SELECT name, balance FROM agent_balances ORDER BY balance DESC LIMIT 10;"

echo ""
echo "=== Migration complete! ==="
