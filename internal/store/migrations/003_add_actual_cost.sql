-- Add actual_cost_usd for provider-provided costs (e.g. OpenAI /costs) as cross-check
ALTER TABLE samples ADD COLUMN actual_cost_usd REAL DEFAULT 0;
