-- STAGING ONLY — soft-delete everything the load test created. Safe to re-run.
-- Run the ESA part against the ESA staging DB and the DM part against the DM staging DB.
-- psql "$ESA_STAGING_URL" -v db=esa -f 99_cleanup.sql
-- psql "$DM_STAGING_URL"  -v db=dm  -f 99_cleanup.sql

\if :{?db}
\else
  \echo 'ERROR: pass -v db=esa or -v db=dm'
  \quit
\endif

SELECT :'db' = 'esa' AS is_esa \gset

\if :is_esa
  UPDATE service_configuration SET is_deleted = true, modified_date = now(), modified_by = 'loadtest'
  WHERE created_by = 'loadtest' AND service_name LIKE 'LOADTEST\_%' AND is_deleted = false;
\else
  UPDATE partner_service_mapping SET is_deleted = true, modified_date = now(), modified_by = 'loadtest'
  WHERE created_by = 'loadtest' AND partner_name LIKE 'LOADTEST\_%' AND is_deleted = false;
  UPDATE service_sfdc_field_mapping SET is_deleted = true, modified_date = now(), modified_by = 'loadtest'
  WHERE created_by = 'loadtest' AND service_name LIKE 'LOADTEST\_%' AND is_deleted = false;
\endif
