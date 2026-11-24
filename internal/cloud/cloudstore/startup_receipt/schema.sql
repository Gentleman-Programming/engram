-- Install through scripts/cloud/install-startup-receipt.sql.
-- The caller sets the two role names in the same PostgreSQL transaction.
SET LOCAL search_path=pg_catalog,pg_temp;

DO $$
DECLARE runtime_role text := current_setting('engram.startup_receipt_runtime_role');
DECLARE retention_role text := current_setting('engram.startup_receipt_retention_role');
DECLARE runtime_oid oid;
DECLARE retention_oid oid;
BEGIN
  SELECT oid INTO runtime_oid FROM pg_catalog.pg_roles WHERE rolname=runtime_role
    AND NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls);
  SELECT oid INTO retention_oid FROM pg_catalog.pg_roles WHERE rolname=retention_role;
  IF runtime_oid IS NULL OR retention_oid IS NULL OR EXISTS (
    SELECT FROM pg_catalog.pg_roles r
    WHERE (r.oid=runtime_oid OR pg_catalog.pg_has_role(runtime_oid,r.oid,'SET'))
      AND pg_catalog.pg_has_role(r.oid,retention_oid,'USAGE')
  ) THEN
    RAISE EXCEPTION 'startup receipt needs separate existing Cloud runtime and retention roles';
  END IF;
END $$;

CREATE SCHEMA receipt_audit;
CREATE TABLE receipt_audit.events(xid xid8 NOT NULL, kind text NOT NULL, project text, seq bigint, id text);
CREATE INDEX ON receipt_audit.events(xid);
CREATE TABLE receipt_audit.enrollment(expected jsonb NOT NULL, epoch uuid NOT NULL, retained_from xid8 NOT NULL);
CREATE TABLE receipt_audit.witnesses(token uuid PRIMARY KEY, scope text NOT NULL, snapshot text NOT NULL, xid xid8 NOT NULL);
CREATE INDEX ON receipt_audit.witnesses(xid);

-- A physical restore can preserve the enrollment identity but omit the
-- transaction which verified the receipt. Issue a fresh token in that
-- transaction, and bind it to its actual PostgreSQL snapshot.
CREATE FUNCTION receipt_audit.save_witness(receipt_scope text) RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE witness uuid := gen_random_uuid();
BEGIN
  IF current_setting('transaction_isolation')<>'repeatable read' OR length(receipt_scope)<>64 THEN
    RAISE EXCEPTION 'invalid startup receipt witness request';
  END IF;
  INSERT INTO receipt_audit.witnesses VALUES(witness,receipt_scope,pg_current_snapshot()::text,pg_current_xact_id());
  RETURN witness;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.save_witness(text) FROM PUBLIC;

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
  IF TG_LEVEL='ROW' OR TG_OP='TRUNCATE' THEN
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
    -- Ongoing logical apply uses row triggers for DML. Invalidate the receipt
    -- on those changes without invoking a row trigger on ordinary Cloud writes.
    EXECUTE format('CREATE TRIGGER receipt_replica_change AFTER INSERT OR UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION receipt_audit.record_other()',source);
    EXECUTE format('ALTER TABLE public.%I ENABLE REPLICA TRIGGER receipt_replica_change',source);
  END LOOP;
END $$;

-- The audit only covers writes to these three tables. Their rows must also
-- survive recovery and have identical visibility for the proof and repair.
CREATE FUNCTION receipt_audit.catalog_v4() RETURNS jsonb LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
  SELECT jsonb_agg(jsonb_build_array(source.name,c.oid,c.relnamespace,c.relfilenode,c.relpersistence,c.relkind,c.relrowsecurity,
    (SELECT jsonb_agg(jsonb_build_array(a.attnum,a.attname,a.atttypid,a.attcollation,a.attnotnull,a.attgenerated,pg_get_expr(d.adbin,d.adrelid)) ORDER BY a.attnum)
      FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
      WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
    (SELECT jsonb_agg(jsonb_build_array(t.oid,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY t.oid)
      FROM pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal)) ORDER BY source.name)
  FROM (VALUES('public.cloud_mutations'),('public.cloud_chunks'),('public.cloud_project_sessions')) source(name)
  JOIN pg_class c ON c.oid=to_regclass(source.name)
  WHERE c.relkind='r' AND c.relpersistence='p'
    AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity
    AND NOT EXISTS (SELECT FROM pg_inherits i WHERE i.inhparent=c.oid OR i.inhrelid=c.oid)
  HAVING count(*)=3;
$$;
DO $$
BEGIN
  IF receipt_audit.catalog_v4() IS NULL THEN
    RAISE EXCEPTION 'startup receipt requires ordinary permanent source tables without row security or inheritance';
  END IF;
END $$;
INSERT INTO receipt_audit.enrollment SELECT receipt_audit.catalog_v4(),gen_random_uuid(),'0'::xid8;

CREATE FUNCTION receipt_audit.identity() RETURNS text LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
  SELECT system_identifier::text || ':' || database.oid::text || ':' || enrollment.epoch::text
  FROM pg_control_system() CROSS JOIN receipt_audit.enrollment
  JOIN pg_database database ON database.datname=current_database();
$$;

CREATE FUNCTION receipt_audit.record_catalog() RETURNS event_trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  IF (SELECT receipt_audit.catalog_v4()=expected FROM receipt_audit.enrollment) IS NOT TRUE THEN
    INSERT INTO receipt_audit.events VALUES(pg_current_xact_id(),'catalog-dirty',NULL,NULL,NULL);
  END IF;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.record_catalog() FROM PUBLIC;
CREATE EVENT TRIGGER receipt_catalog_ddl ON ddl_command_end EXECUTE FUNCTION receipt_audit.record_catalog();
ALTER EVENT TRIGGER receipt_catalog_ddl ENABLE ALWAYS;
REVOKE EXECUTE ON FUNCTION receipt_audit.identity(), receipt_audit.catalog_v4() FROM PUBLIC;

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
  DELETE FROM receipt_audit.witnesses WHERE xid<new_floor;
  RETURN removed;
END $$;

CREATE FUNCTION receipt_audit.reset_log() RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE removed bigint;
BEGIN
  UPDATE receipt_audit.enrollment SET epoch=gen_random_uuid();
  DELETE FROM receipt_audit.events;
  GET DIAGNOSTICS removed = ROW_COUNT;
  DELETE FROM receipt_audit.witnesses;
  RETURN removed;
END $$;
REVOKE EXECUTE ON FUNCTION receipt_audit.prune_prefix(xid8),receipt_audit.reset_log() FROM PUBLIC;
DO $$
DECLARE runtime_role text := current_setting('engram.startup_receipt_runtime_role');
DECLARE retention_role text := current_setting('engram.startup_receipt_retention_role');
BEGIN
  EXECUTE format('GRANT USAGE ON SCHEMA receipt_audit TO %I, %I',runtime_role,retention_role);
  EXECUTE format('GRANT SELECT ON receipt_audit.events, receipt_audit.enrollment, receipt_audit.witnesses TO %I',runtime_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION receipt_audit.identity(), receipt_audit.catalog_v4(), receipt_audit.save_witness(text) TO %I',runtime_role);
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
        (a.grantee=runtime_oid AND p.proname IN ('identity','catalog_v4','save_witness')) OR
        (a.grantee=retention_oid AND p.proname IN ('prune_prefix','reset_log'))
      ))
  ) THEN
    RAISE EXCEPTION 'startup receipt audit objects have unexpected default privileges';
  END IF;
END $$;
