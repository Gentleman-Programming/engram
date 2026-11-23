-- Install through scripts/cloud/install-startup-receipt.sql.
-- The caller sets the two role names in the same PostgreSQL transaction.
SET LOCAL search_path=pg_catalog,pg_temp;

DO $$
DECLARE runtime_role text := current_setting('engram.startup_receipt_runtime_role');
DECLARE retention_role text := current_setting('engram.startup_receipt_retention_role');
BEGIN
  IF runtime_role=retention_role OR NOT EXISTS (
    SELECT FROM pg_catalog.pg_roles WHERE rolname=runtime_role
      AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls)
  ) OR NOT EXISTS (
    SELECT FROM pg_catalog.pg_roles WHERE rolname=retention_role
  ) THEN
    RAISE EXCEPTION 'startup receipt needs separate existing Cloud runtime and retention roles';
  END IF;
END $$;

CREATE SCHEMA receipt_audit;
CREATE TABLE receipt_audit.events(xid xid8 NOT NULL, kind text NOT NULL, project text, seq bigint, id text);
CREATE INDEX ON receipt_audit.events(xid);
CREATE TABLE receipt_audit.enrollment(expected jsonb NOT NULL, epoch uuid NOT NULL, retained_from xid8 NOT NULL);

CREATE FUNCTION receipt_audit.record_insert() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  CASE TG_TABLE_NAME
  WHEN 'cloud_mutations' THEN
    INSERT INTO receipt_audit.events SELECT pg_current_xact_id(),'journal',project,seq,NULL FROM inserted_rows;
  WHEN 'cloud_chunks' THEN
    INSERT INTO receipt_audit.events SELECT pg_current_xact_id(),'cloud_chunks',project_name,NULL,chunk_id FROM inserted_rows;
  WHEN 'cloud_project_sessions' THEN
    INSERT INTO receipt_audit.events SELECT pg_current_xact_id(),'cloud_project_sessions',project_name,NULL,session_id FROM inserted_rows;
  END CASE;
  RETURN NULL;
END $$;

CREATE FUNCTION receipt_audit.record_other() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  IF TG_OP='TRUNCATE' THEN
    INSERT INTO receipt_audit.events VALUES(pg_current_xact_id(),'destructive',NULL,NULL,NULL);
  ELSIF EXISTS(SELECT FROM old_rows) THEN
    INSERT INTO receipt_audit.events VALUES(pg_current_xact_id(),'destructive',NULL,NULL,NULL);
  END IF;
  RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.record_insert(), receipt_audit.record_other() FROM PUBLIC;
DO $$
DECLARE source text;
BEGIN
  FOREACH source IN ARRAY ARRAY['cloud_mutations','cloud_chunks','cloud_project_sessions'] LOOP
    EXECUTE format('CREATE TRIGGER receipt_insert AFTER INSERT ON public.%I REFERENCING NEW TABLE AS inserted_rows FOR EACH STATEMENT EXECUTE FUNCTION receipt_audit.record_insert()',source);
    EXECUTE format('CREATE TRIGGER receipt_update AFTER UPDATE ON public.%I REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION receipt_audit.record_other()',source);
    EXECUTE format('CREATE TRIGGER receipt_delete AFTER DELETE ON public.%I REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION receipt_audit.record_other()',source);
    EXECUTE format('CREATE TRIGGER receipt_truncate AFTER TRUNCATE ON public.%I FOR EACH STATEMENT EXECUTE FUNCTION receipt_audit.record_other()',source);
    EXECUTE format('ALTER TABLE public.%I ENABLE ALWAYS TRIGGER receipt_insert, ENABLE ALWAYS TRIGGER receipt_update, ENABLE ALWAYS TRIGGER receipt_delete, ENABLE ALWAYS TRIGGER receipt_truncate',source);
  END LOOP;
END $$;

CREATE FUNCTION receipt_audit.catalog() RETURNS jsonb LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
  SELECT jsonb_agg(jsonb_build_array(source.name,c.oid,c.relnamespace,c.relfilenode,c.relpersistence,c.relkind,c.relrowsecurity,
    (SELECT jsonb_agg(jsonb_build_array(a.attnum,a.attname,a.atttypid,a.attcollation,a.attnotnull,a.attgenerated,pg_get_expr(d.adbin,d.adrelid)) ORDER BY a.attnum)
      FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
      WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
    (SELECT jsonb_agg(jsonb_build_array(t.oid,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY t.oid)
      FROM pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal)) ORDER BY source.name)
  FROM (VALUES('public.cloud_mutations'),('public.cloud_chunks'),('public.cloud_project_sessions')) source(name)
  LEFT JOIN pg_class c ON c.oid=to_regclass(source.name);
$$;
INSERT INTO receipt_audit.enrollment SELECT receipt_audit.catalog(),gen_random_uuid(),'0'::xid8;

CREATE FUNCTION receipt_audit.identity() RETURNS text LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
  SELECT system_identifier::text || ':' || database.oid::text || ':' || enrollment.epoch::text
  FROM pg_control_system() CROSS JOIN receipt_audit.enrollment
  JOIN pg_database database ON database.datname=current_database();
$$;

CREATE FUNCTION receipt_audit.record_catalog() RETURNS event_trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  IF (SELECT receipt_audit.catalog()=expected FROM receipt_audit.enrollment) IS NOT TRUE THEN
    INSERT INTO receipt_audit.events VALUES(pg_current_xact_id(),'catalog-dirty',NULL,NULL,NULL);
  END IF;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.record_catalog() FROM PUBLIC;
CREATE EVENT TRIGGER receipt_catalog_ddl ON ddl_command_end EXECUTE FUNCTION receipt_audit.record_catalog();
ALTER EVENT TRIGGER receipt_catalog_ddl ENABLE ALWAYS;
REVOKE EXECUTE ON FUNCTION receipt_audit.identity(), receipt_audit.catalog() FROM PUBLIC;

-- Retention uses MVCC DELETE. A proof already in flight can finish with its
-- old enrollment/event snapshot, and later reads observe its floor or epoch.
CREATE FUNCTION receipt_audit.prune_prefix(new_floor xid8) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE previous_floor xid8;
DECLARE removed bigint;
BEGIN
  SELECT retained_from INTO STRICT previous_floor FROM receipt_audit.enrollment FOR UPDATE;
  IF new_floor IS NULL OR new_floor<previous_floor OR new_floor>pg_snapshot_xmin(pg_current_snapshot()) THEN
    RAISE EXCEPTION USING ERRCODE='22023', MESSAGE='unsafe receipt retention floor';
  END IF;
  UPDATE receipt_audit.enrollment SET retained_from=new_floor;
  DELETE FROM receipt_audit.events WHERE xid<new_floor;
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END $$;

CREATE FUNCTION receipt_audit.reset_log() RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE removed bigint;
BEGIN
  UPDATE receipt_audit.enrollment SET epoch=gen_random_uuid();
  DELETE FROM receipt_audit.events;
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.prune_prefix(xid8),receipt_audit.reset_log() FROM PUBLIC;
DO $$
DECLARE runtime_role text := current_setting('engram.startup_receipt_runtime_role');
DECLARE retention_role text := current_setting('engram.startup_receipt_retention_role');
BEGIN
  EXECUTE format('GRANT USAGE ON SCHEMA receipt_audit TO %I, %I',runtime_role,retention_role);
  EXECUTE format('GRANT SELECT ON receipt_audit.events, receipt_audit.enrollment TO %I',runtime_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION receipt_audit.identity(), receipt_audit.catalog() TO %I',runtime_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION receipt_audit.prune_prefix(xid8),receipt_audit.reset_log() TO %I',retention_role);
END $$;

-- PostgreSQL applies the installer's default privileges when creating objects.
-- Reject grants outside the enrollment contract before exposing the feed.
DO $$
DECLARE runtime_oid oid := (SELECT oid FROM pg_roles
  WHERE rolname=current_setting('engram.startup_receipt_runtime_role'));
DECLARE retention_oid oid := (SELECT oid FROM pg_roles
  WHERE rolname=current_setting('engram.startup_receipt_retention_role'));
DECLARE audit_oid oid := 'receipt_audit'::regnamespace;
BEGIN
  IF EXISTS (
    SELECT FROM pg_namespace n
    CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a
    WHERE n.oid=audit_oid AND a.grantee<>n.nspowner
      AND (a.grantee NOT IN (runtime_oid,retention_oid) OR a.privilege_type<>'USAGE' OR a.is_grantable)
  ) OR EXISTS (
    SELECT FROM pg_class c
    CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault('r',c.relowner))) a
    WHERE c.relnamespace=audit_oid AND c.relkind='r' AND a.grantee<>c.relowner
      AND (a.grantee<>runtime_oid OR a.privilege_type<>'SELECT' OR a.is_grantable)
  ) OR EXISTS (
    SELECT FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid
    WHERE c.relnamespace=audit_oid AND a.attacl IS NOT NULL
  ) OR EXISTS (
    SELECT FROM pg_proc p
    CROSS JOIN LATERAL aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a
    WHERE p.pronamespace=audit_oid AND a.grantee<>p.proowner
      AND (a.privilege_type<>'EXECUTE' OR a.is_grantable OR NOT (
        (a.grantee=runtime_oid AND p.proname IN ('identity','catalog')) OR
        (a.grantee=retention_oid AND p.proname IN ('prune_prefix','reset_log'))
      ))
  ) THEN
    RAISE EXCEPTION 'startup receipt audit objects have unexpected default privileges';
  END IF;
END $$;
