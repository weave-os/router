BEGIN;

-- Precise inverse of the up. Refilling NULL with 'cluster' before reimposing the
-- constraint is required: NULL is exactly what the up introduced, and SET NOT NULL
-- would otherwise fail on those rows. This restores the pre-edit 0034 shape, so an
-- environment rolled back here resumes stamping new installations 'cluster'.
UPDATE router.model_router_installations
SET routing_strategy = 'cluster'
WHERE routing_strategy IS NULL;

ALTER TABLE router.model_router_installations
  ALTER COLUMN routing_strategy SET DEFAULT 'cluster',
  ALTER COLUMN routing_strategy SET NOT NULL;

COMMIT;
