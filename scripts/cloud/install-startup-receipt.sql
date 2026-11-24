\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL READ COMMITTED;
SET LOCAL lock_timeout='5s';
-- Serialize startup receipt administration in this database.
SELECT pg_catalog.pg_advisory_xact_lock(1162757970, 1397900116) IS NULL AS engram_receipt_admin_lock \gset
SELECT pg_catalog.set_config('engram.startup_receipt_runtime_role', :'runtime_role', true) AS engram_runtime_role,
       pg_catalog.set_config('engram.startup_receipt_retention_role', :'retention_role', true) AS engram_retention_role \gset
\ir ../../internal/cloud/cloudstore/startup_receipt/schema.sql
COMMIT;
