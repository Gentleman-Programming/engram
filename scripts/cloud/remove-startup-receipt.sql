\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL READ COMMITTED;
SET LOCAL lock_timeout='5s';
-- Serialize startup receipt administration in this database.
SELECT pg_catalog.pg_advisory_xact_lock(1162757970, 1397900116) IS NULL AS engram_receipt_admin_lock \gset
SET LOCAL search_path=pg_catalog,pg_temp;
DROP EVENT TRIGGER receipt_catalog_ddl;
DO $$
DECLARE source text;
DECLARE name text;
BEGIN
  FOREACH source IN ARRAY ARRAY['cloud_mutations','cloud_chunks','cloud_project_sessions'] LOOP
    FOREACH name IN ARRAY ARRAY['receipt_insert','receipt_update','receipt_delete','receipt_truncate','receipt_replica_change'] LOOP
      EXECUTE format('DROP TRIGGER %I ON public.%I',name,source);
    END LOOP;
  END LOOP;
END $$;
DROP FUNCTION receipt_audit.record_catalog(), receipt_audit.record_insert(), receipt_audit.record_other(),
              receipt_audit.identity(), receipt_audit.catalog_v4(), receipt_audit.prune_prefix(xid8), receipt_audit.reset_log(),
              receipt_audit.save_witness(text);
DROP TABLE receipt_audit.events, receipt_audit.enrollment, receipt_audit.witnesses;
DROP SCHEMA receipt_audit;
COMMIT;
