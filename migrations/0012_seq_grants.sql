-- 0012_seq_grants.sql — fix sequence privileges for the app roles.
--
-- 0008 granted USAGE on sequences that existed AT THAT TIME and set default
-- privileges for future TABLES, but NOT for future SEQUENCES. Tables added
-- afterwards that use BIGSERIAL (notably `tenants` in 0009, plus any other
-- *_id_seq created later) therefore own a sequence the app roles cannot use,
-- so INSERTs fail with "permission denied for sequence ..._id_seq".
--
-- This re-grants on all current sequences and — crucially — sets default
-- privileges so any future BIGSERIAL sequence is covered automatically.
-- Both statements are idempotent (safe to apply twice).

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant, app_system;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app_tenant, app_system;
