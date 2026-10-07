-- STAGING ONLY — run against the DM staging database.
-- Routes partner LOADTEST_PARTNER / stage "loadtest" to the mock ESA sequence, and gives each
-- mock service an EMPTY Salesforce field mapping ('[]'). DM then runs its full pipeline (ESA call,
-- response transform, mapping merge, interpolation, Mongo audit) but builds no composite graph,
-- so no Salesforce object writes happen. (DM still makes its one Apex decision-callback per
-- decision; see the plan.)
--
--   psql "$DM_STAGING_URL" -v ON_ERROR_STOP=1 -v seq='{101,102;103;104}' -f 02_dm_mock_mapping.sql
--   (use the sequence_string printed by 01_esa_mock_services.sql)
--
-- Prints the partner_service_mapping id; that is SEQUENCE_ID for the ESA-direct k6 test.

\if :{?seq}
\else
  \echo 'ERROR: pass -v seq=<sequence_string from 01_esa_mock_services.sql>'
  \quit
\endif

BEGIN;

INSERT INTO partner_service_mapping
  (name, partner_name, program_type, business_type, sourcing_program, loan_category,
   customer_type, product_line, stage, service_sequence_string, created_date, created_by, is_deleted)
VALUES
  ('LOADTEST_SEQUENCE', 'LOADTEST_PARTNER', '', '', '', '', '', '', 'loadtest', :'seq', now(), 'loadtest', false)
RETURNING id AS sequence_id, service_sequence_string;

INSERT INTO service_sfdc_field_mapping (service_name, request_body, created_date, created_by, is_deleted)
VALUES
  ('LOADTEST_MOCK_BUREAU', '[]', now(), 'loadtest', false),
  ('LOADTEST_MOCK_KYC',    '[]', now(), 'loadtest', false),
  ('LOADTEST_MOCK_BANK',   '[]', now(), 'loadtest', false),
  ('LOADTEST_MOCK_KYC2',   '[]', now(), 'loadtest', false);

COMMIT;
