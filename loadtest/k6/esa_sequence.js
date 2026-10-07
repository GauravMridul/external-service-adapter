// ESA: POST /external-service-adapter/v1/process-sequence/v2   (STAGING ONLY, synchronous API)
//
// Calls ESA directly (bypassing DM) to isolate ESA's own per-pod capacity.
// Required env: API_KEY, SEQUENCE_ID, SEQUENCE_STRING (e.g. "{101,102;103;104}"), PARTNER, STAGE.
import http from 'k6/http';
import { check } from 'k6';
import { env, guardTarget, uuidv4, buildScenario, buildThresholds, record, summarize } from './lib.js';

const BASE_URL = env('BASE_URL', 'http://external-service-adapter-service.staging.svc.cluster.local:8081');
const URL = env('URL', `${BASE_URL}/external-service-adapter/v1/process-sequence/v2`);
const RUN_ID = env('RUN_ID', `${Date.now()}`);

guardTarget(URL);

export const options = {
  scenarios: { esa: buildScenario() },
  thresholds: buildThresholds(),
  summaryTrendStats: ['med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  for (const k of ['API_KEY', 'SEQUENCE_ID', 'SEQUENCE_STRING', 'PARTNER', 'STAGE']) {
    if (!env(k, '')) throw new Error(`${k} is required`);
  }
}

export default function () {
  // JSON keys are the json tags on esa_request_dto.ProcessSequenceRequest.
  const id = `LOADTEST_${RUN_ID}_${__VU}_${__ITER}`;
  const body = JSON.stringify({
    applicationId: id,            // required
    customerId: id,               // required
    partnerName: env('PARTNER', ''),
    sequenceId: env('SEQUENCE_ID', ''),
    sequenceString: env('SEQUENCE_STRING', ''),
    stage: env('STAGE', ''),
    workflowId: uuidv4(),
  });
  const res = http.post(URL, body, {
    headers: {
      'Content-Type': 'application/json',
      'X-Api-Key': env('API_KEY', ''),
      'X-Correlation-ID': `loadtest-${RUN_ID}-${__VU}-${__ITER}`,
    },
    timeout: env('HTTP_TIMEOUT', '150s'),
  });
  record(res);
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 });
  if (__ENV.DEBUG === '1' && (res.status < 200 || res.status >= 300)) {
    console.warn(`status=${res.status} body=${String(res.body).slice(0, 300)}`);
  }
}

export function handleSummary(data) {
  return summarize(data, 'esa');
}
