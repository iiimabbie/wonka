-- Add unique constraint for idempotency to prevent duplicate transactions
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_idempotency ON candy_ledger (agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
