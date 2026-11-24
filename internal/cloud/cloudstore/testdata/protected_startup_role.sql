-- Evaluated after startup has pinned its PostgreSQL search path and snapshot.
WITH attainable AS (
  SELECT r.* FROM pg_catalog.pg_roles r
  WHERE r.rolname IN (current_user,session_user) OR pg_catalog.pg_has_role(session_user,r.oid,'SET')
), audit AS (
  SELECT oid,nspowner FROM pg_catalog.pg_namespace WHERE nspname='receipt_audit'
), tables AS (
  SELECT c.oid,c.relname,c.relowner FROM pg_catalog.pg_class c JOIN audit ON audit.oid=c.relnamespace
  WHERE c.relname IN ('events','enrollment','witnesses') AND c.relkind='r'
), routines AS (
  SELECT p.oid,p.proname,p.proowner,p.pronargs,p.proargtypes,p.prorettype,p.prolang,p.prosecdef,p.proconfig,p.prosrc
  FROM pg_catalog.pg_proc p JOIN audit ON audit.oid=p.pronamespace
), trigger AS (
  SELECT e.evtowner FROM pg_catalog.pg_event_trigger e JOIN routines r ON r.oid=e.evtfoid
  WHERE e.evtname='receipt_catalog_ddl' AND e.evtevent='ddl_command_end'
    AND e.evtenabled='A' AND e.evttags IS NULL
    AND r.proname='record_catalog' AND r.proowner=e.evtowner AND r.pronargs=0
    AND r.prorettype='pg_catalog.event_trigger'::pg_catalog.regtype AND r.prosecdef
    AND r.prolang=(SELECT oid FROM pg_catalog.pg_language WHERE lanname='plpgsql')
    AND r.proconfig=ARRAY['search_path=pg_catalog']::pg_catalog.text[]
    -- Exact record_catalog body in startup_receipt/schema.sql (LF normalized).
    AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(
      pg_catalog.replace(r.prosrc,E'\r\n',E'\n'),'UTF8')),'hex')
      ='6482f5bae6c1b9d012436039efa9d292008019da53be009262989976977f67e6'
), protected AS (
  SELECT (SELECT oid FROM tables WHERE relname='events') AS events,
         (SELECT oid FROM tables WHERE relname='enrollment') AS enrollment,
         (SELECT oid FROM tables WHERE relname='witnesses') AS witnesses,
         (SELECT oid FROM routines WHERE proname='reset_log' AND pronargs=0) AS reset,
         (SELECT oid FROM routines WHERE proname='prune_prefix' AND pronargs=1 AND proargtypes[0]='pg_catalog.xid8'::pg_catalog.regtype) AS prune,
         (SELECT oid FROM routines WHERE proname='identity' AND pronargs=0) AS identity,
         (SELECT oid FROM routines WHERE proname='catalog_v4' AND pronargs=0) AS catalog,
         (SELECT oid FROM routines WHERE proname='save_witness' AND pronargs=1
            AND proargtypes[0]='pg_catalog.text'::pg_catalog.regtype) AS save_witness,
         (SELECT oid FROM audit) AS ns
), protected_owners(owner) AS (
  SELECT datdba FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()
  UNION SELECT evtowner FROM trigger
  UNION SELECT nspowner FROM audit
  UNION SELECT relowner FROM tables
  UNION SELECT proowner FROM routines
), privileged AS (
  SELECT a.oid FROM attainable a CROSS JOIN protected p
  WHERE a.rolsuper OR a.rolcreaterole OR a.rolreplication OR a.rolbypassrls
     OR EXISTS (SELECT FROM protected_owners o WHERE pg_catalog.pg_has_role(a.oid,o.owner,'USAGE'))
     OR pg_catalog.has_schema_privilege(a.oid,p.ns,'CREATE')
     OR pg_catalog.has_table_privilege(a.oid,p.events,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
     OR pg_catalog.has_table_privilege(a.oid,p.enrollment,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
     OR pg_catalog.has_table_privilege(a.oid,p.witnesses,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
     OR pg_catalog.has_any_column_privilege(a.oid,p.events,'INSERT,UPDATE')
     OR pg_catalog.has_any_column_privilege(a.oid,p.enrollment,'INSERT,UPDATE')
     OR pg_catalog.has_any_column_privilege(a.oid,p.witnesses,'INSERT,UPDATE')
     OR pg_catalog.has_function_privilege(a.oid,p.reset,'EXECUTE') OR pg_catalog.has_function_privilege(a.oid,p.prune,'EXECUTE')
     OR pg_catalog.pg_has_role(a.oid,'pg_write_server_files','USAGE') OR pg_catalog.pg_has_role(a.oid,'pg_execute_server_program','USAGE')
     OR EXISTS (SELECT FROM pg_catalog.pg_settings s WHERE name IN ('session_replication_role','event_triggers')
         AND (pg_catalog.has_parameter_privilege(a.oid,s.name,'SET') OR pg_catalog.has_parameter_privilege(a.oid,s.name,'ALTER SYSTEM')))
)
SELECT coalesce(
    pg_catalog.current_setting('session_replication_role')='origin' AND coalesce(pg_catalog.current_setting('event_triggers',true)::boolean,true)
    AND NOT EXISTS(SELECT FROM privileged) AND EXISTS(SELECT FROM trigger)
    AND reset IS NOT NULL AND prune IS NOT NULL
    AND pg_catalog.has_schema_privilege(current_user,ns,'USAGE')
    AND pg_catalog.has_table_privilege(current_user,events,'SELECT') AND pg_catalog.has_table_privilege(current_user,enrollment,'SELECT')
    AND pg_catalog.has_table_privilege(current_user,witnesses,'SELECT')
    AND pg_catalog.has_function_privilege(current_user,save_witness,'EXECUTE')
    AND pg_catalog.has_function_privilege(current_user,identity,'EXECUTE') AND pg_catalog.has_function_privilege(current_user,catalog,'EXECUTE'),false)
FROM protected;
