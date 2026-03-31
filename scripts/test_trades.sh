#!/bin/bash
# Wonka test environment - trade simulation & validation
set -e

BASE="http://localhost:8091"
ADMIN_KEY="test-admin-key"
AGENT_KEY="test-key-alpha"

echo "=== 1. Health check ==="
curl -sf "$BASE/health" | jq .

echo ""
echo "=== 2. Seed test data ==="
docker exec -i wonka-postgres-test psql -U wonka -d wonka_test < scripts/seed_test.sql
echo "✅ Seed complete"

echo ""
echo "=== 3. Check agent balance ==="
curl -sf "$BASE/v1/candies/balance" -H "Authorization: Bearer $AGENT_KEY" | jq .

echo ""
echo "=== 4. Trigger daily refresh (event + price) ==="
curl -sf -X POST "$BASE/v1/market/refresh" -H "X-Admin-Key: $ADMIN_KEY" | jq .

echo ""
echo "=== 5. Check market (should NOT have anchor_price) ==="
MARKET=$(curl -sf "$BASE/v1/market")
echo "$MARKET" | jq '.listings[0]'
if echo "$MARKET" | grep -q "anchor_price"; then
  echo "❌ FAIL: anchor_price is exposed in API!"
  exit 1
else
  echo "✅ anchor_price is hidden"
fi

echo ""
echo "=== 6. Check market items (should NOT have anchor_price) ==="
ITEMS=$(curl -sf "$BASE/v1/market/items")
echo "$ITEMS" | jq '.items[0]'
if echo "$ITEMS" | grep -q "anchor_price"; then
  echo "❌ FAIL: anchor_price is exposed in items API!"
  exit 1
else
  echo "✅ anchor_price is hidden in items"
fi

echo ""
echo "=== 7. Buy some items (simulate trades) ==="
# Get first listing ID
LISTING_ID=$(echo "$MARKET" | jq -r '.listings[0].id')
LISTING_NAME=$(echo "$MARKET" | jq -r '.listings[0].item_name')
echo "Buying: $LISTING_NAME ($LISTING_ID)"
curl -sf -X POST "$BASE/v1/market/buy" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"listing_id\": \"$LISTING_ID\", \"idempotencyKey\": \"test-buy-001\"}" | jq .

# Buy another
LISTING_ID2=$(echo "$MARKET" | jq -r '.listings[1].id')
LISTING_NAME2=$(echo "$MARKET" | jq -r '.listings[1].item_name')
echo "Buying: $LISTING_NAME2 ($LISTING_ID2)"
curl -sf -X POST "$BASE/v1/market/buy" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"listing_id\": \"$LISTING_ID2\", \"idempotencyKey\": \"test-buy-002\"}" | jq .

echo ""
echo "=== 8. Check inventory ==="
curl -sf "$BASE/v1/inventory" -H "Authorization: Bearer $AGENT_KEY" | jq .

echo ""
echo "=== 9. Sell first item ==="
INV_ID=$(curl -sf "$BASE/v1/inventory" -H "Authorization: Bearer $AGENT_KEY" | jq -r '.items[0].id')
echo "Selling inventory: $INV_ID"
curl -sf -X POST "$BASE/v1/market/sell" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"inventory_id\": \"$INV_ID\", \"idempotencyKey\": \"test-sell-001\"}" | jq .

echo ""
echo "=== 10. Trigger hourly refresh (with trades) ==="
curl -sf -X POST "$BASE/v1/market/hourly-refresh" -H "X-Admin-Key: $ADMIN_KEY" | jq .

echo ""
echo "=== 11. Check leaderboard ==="
curl -sf "$BASE/v1/candies/leaderboard" | jq .

echo ""
echo "=== 12. Final balance ==="
curl -sf "$BASE/v1/candies/balance" -H "Authorization: Bearer $AGENT_KEY" | jq .

echo ""
echo "🎉 All tests passed!"
