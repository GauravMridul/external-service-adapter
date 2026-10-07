-- STAGING ONLY — run against the ESA staging database.
-- Creates 4 mock services that call the in-cluster mock provider (k8s/mock-provider.yaml)
-- instead of real third parties. Set the delays to the production p50 latencies from query Q9
-- so ESA spends realistic time waiting, while doing its real work (config load from Postgres,
-- templating, goroutines, Mongo audit writes).
--
--   psql "$ESA_STAGING_URL" -v ON_ERROR_STOP=1 -f 01_esa_mock_services.sql
--
-- Copy the sequence_string printed at the end into 02_dm_mock_mapping.sql.
-- Cleanup: 99_cleanup.sql

\set d_bureau '1.8'
\set d_kyc    '0.6'
\set d_bank   '4.0'
\set d_kyc2   '0.3'

BEGIN;

CREATE TEMP TABLE _lt(slot int, id bigint) ON COMMIT DROP;

WITH ins AS (
  INSERT INTO service_configuration
    (service_name, api_url, headers, request_body, request_method, response_body,
     send_response, timeout, additional_config, created_date, created_by, is_deleted)
  VALUES
    ('LOADTEST_MOCK_BUREAU', 'http://mock-bureau.staging.svc.cluster.local/delay/' || :'d_bureau', '{}', '{}', 'GET', '{}', false, 0, '{}', now(), 'loadtest', false),
    ('LOADTEST_MOCK_KYC',    'http://mock-kyc.staging.svc.cluster.local/delay/'    || :'d_kyc',    '{}', '{}', 'GET', '{}', false, 0, '{}', now(), 'loadtest', false),
    ('LOADTEST_MOCK_BANK',   'http://mock-bank.staging.svc.cluster.local/delay/'   || :'d_bank',   '{}', '{}', 'GET', '{}', false, 0, '{}', now(), 'loadtest', false),
    ('LOADTEST_MOCK_KYC2',   'http://mock-kyc.staging.svc.cluster.local/delay/'    || :'d_kyc2',   '{}', '{}', 'GET', '{}', false, 0, '{}', now(), 'loadtest', false)
  RETURNING id, service_name
)
INSERT INTO _lt
SELECT CASE service_name WHEN 'LOADTEST_MOCK_BUREAU' THEN 1 WHEN 'LOADTEST_MOCK_KYC' THEN 2
                         WHEN 'LOADTEST_MOCK_BANK' THEN 3 ELSE 4 END, id
FROM ins;

-- Shape: {bureau,kyc ; bank ; kyc2}  -> group 1 runs 2 calls in parallel, then 1, then 1.
-- Wall time ~ max(1.8,0.6) + 4.0 + 0.3 = 6.1s plus ESA overhead. Adjust to match Q10/Q11.
SELECT '{' || max(id) FILTER (WHERE slot = 1) || ',' || max(id) FILTER (WHERE slot = 2) || ';'
           || max(id) FILTER (WHERE slot = 3) || ';' || max(id) FILTER (WHERE slot = 4) || '}'
       AS sequence_string
FROM _lt;

COMMIT;
