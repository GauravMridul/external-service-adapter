-- STAGING ONLY — run against the ESA staging database between test runs to change mock latency.
-- ESA staging has cache.enabled=false, so it reads service_configuration from Postgres on every
-- request: the change applies to the next request, with no restart.
--
-- "fast" profile (CPU-bound runs: find how much work one pod can do):
--   psql "$ESA_STAGING_URL" -v bureau=0.2 -v kyc=0.2 -v bank=0.2 -v kyc2=0.2 -f 03_set_mock_delays.sql
-- "real" profile (latency-bound runs): use production p50s from Mongo Q9, e.g.
--   psql "$ESA_STAGING_URL" -v bureau=1.8 -v kyc=0.6 -v bank=4.0 -v kyc2=0.3 -f 03_set_mock_delays.sql

\if :{?bureau} \else \echo 'ERROR: pass -v bureau=<s> -v kyc=<s> -v bank=<s> -v kyc2=<s>' \quit \endif
\if :{?kyc}    \else \echo 'ERROR: missing -v kyc'  \quit \endif
\if :{?bank}   \else \echo 'ERROR: missing -v bank' \quit \endif
\if :{?kyc2}   \else \echo 'ERROR: missing -v kyc2' \quit \endif

BEGIN;
UPDATE service_configuration
SET api_url = CASE service_name
      WHEN 'LOADTEST_MOCK_BUREAU' THEN 'http://mock-bureau.staging.svc.cluster.local/delay/' || :'bureau'
      WHEN 'LOADTEST_MOCK_KYC'    THEN 'http://mock-kyc.staging.svc.cluster.local/delay/'    || :'kyc'
      WHEN 'LOADTEST_MOCK_BANK'   THEN 'http://mock-bank.staging.svc.cluster.local/delay/'   || :'bank'
      WHEN 'LOADTEST_MOCK_KYC2'   THEN 'http://mock-kyc.staging.svc.cluster.local/delay/'    || :'kyc2'
    END,
    modified_date = now(), modified_by = 'loadtest'
WHERE created_by = 'loadtest' AND is_deleted = false
  AND service_name IN ('LOADTEST_MOCK_BUREAU', 'LOADTEST_MOCK_KYC', 'LOADTEST_MOCK_BANK', 'LOADTEST_MOCK_KYC2');
SELECT service_name, api_url FROM service_configuration
WHERE created_by = 'loadtest' AND is_deleted = false ORDER BY id;
COMMIT;
