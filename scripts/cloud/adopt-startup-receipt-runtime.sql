\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL READ COMMITTED;
SET LOCAL lock_timeout='5s';
-- Serialize startup receipt administration in this database.
SELECT pg_catalog.pg_advisory_xact_lock(1162757970, 1397900116) IS NULL AS engram_receipt_admin_lock \gset
SET LOCAL search_path=pg_catalog,pg_temp;

SELECT pg_catalog.set_config('engram.startup_receipt_previous_owner', :'previous_owner', true) AS engram_previous_owner,
       pg_catalog.set_config('engram.startup_receipt_runtime_role', :'runtime_role', true) AS engram_runtime_role \gset

DO $$
DECLARE
  previous_owner text := current_setting('engram.startup_receipt_previous_owner');
  runtime_role text := current_setting('engram.startup_receipt_runtime_role');
  previous_oid oid;
  runtime_oid oid;
  source name;
  sources constant name[] := ARRAY[
    'cloud_auth_audit_log', 'cloud_chunks', 'cloud_human_users', 'cloud_mutations',
    'cloud_principal_tokens', 'cloud_principals', 'cloud_project_controls',
    'cloud_project_grants', 'cloud_project_sessions', 'cloud_prompt_pair_claims',
    'cloud_prompt_source_attestations', 'cloud_session_authority', 'cloud_sync_audit_log', 'cloud_users'
  ];
BEGIN
  SELECT oid INTO previous_oid FROM pg_roles WHERE rolname::text=previous_owner AND rolsuper;
  SELECT oid INTO runtime_oid FROM pg_roles
  WHERE rolname::text=runtime_role AND rolcanlogin AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls);
  IF previous_oid IS NULL OR runtime_oid IS NULL
    OR pg_has_role(runtime_oid,previous_oid,'SET') OR pg_has_role(runtime_oid,previous_oid,'USAGE')
    OR EXISTS (SELECT FROM pg_database WHERE datname=current_database() AND datdba=runtime_oid)
  THEN
    RAISE EXCEPTION 'expected a bootstrap owner and a separate restricted Cloud runtime login';
  END IF;
  IF NOT has_database_privilege(runtime_oid, current_database(), 'CONNECT') THEN
    RAISE EXCEPTION 'Cloud runtime login requires CONNECT on the current database';
  END IF;
  IF to_regnamespace('receipt_audit') IS NOT NULL THEN
    RAISE EXCEPTION 'prepare the restricted Cloud runtime before installing startup audit';
  END IF;

  -- Validate and hold all source relations before transferring any of them.
  FOREACH source IN ARRAY sources LOOP
    EXECUTE format('LOCK TABLE public.%I IN ACCESS EXCLUSIVE MODE',source);
    IF NOT EXISTS (
      SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='public' AND c.relname=source AND c.relkind='r'
        AND c.relowner IN (previous_oid,runtime_oid)
    ) THEN
      RAISE EXCEPTION 'unexpected Cloud table ownership or type for %', source;
    END IF;
  END LOOP;

  EXECUTE format('GRANT USAGE, CREATE ON SCHEMA public TO %I',runtime_role);
  FOREACH source IN ARRAY sources LOOP
    EXECUTE format('ALTER TABLE public.%I OWNER TO %I',source,runtime_role);
  END LOOP;
END $$;
COMMIT;
