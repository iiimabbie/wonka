#!/usr/bin/env bash
set -euo pipefail

# Run integration tests in Docker
# Usage: ./scripts/run_tests.sh [-race]

cd "$(dirname "$0")/.."

COMPOSE_FILE="docker-compose.test.yml"

echo "🐘 Starting test database..."
docker compose -f "$COMPOSE_FILE" up -d postgres-test 2>/dev/null

echo "🔨 Building test container..."
docker compose -f "$COMPOSE_FILE" --profile test build wonka-integration-test 2>/dev/null

echo "🧪 Running integration tests..."
docker compose -f "$COMPOSE_FILE" --profile test run --rm wonka-integration-test \
  go test -count=1 -timeout 60s -v "$@" .

EXIT_CODE=$?

echo ""
if [ $EXIT_CODE -eq 0 ]; then
  echo "✅ All tests passed!"
else
  echo "❌ Tests failed (exit code: $EXIT_CODE)"
fi

exit $EXIT_CODE
