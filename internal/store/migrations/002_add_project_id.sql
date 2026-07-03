-- Add project_id for OpenAI project-based grouping and enforcement (optional, nullable)
ALTER TABLE samples ADD COLUMN project_id TEXT;
