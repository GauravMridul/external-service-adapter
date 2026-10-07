// Shared k6 helpers for the DM / ESA capacity tests (STAGING ONLY).
// No remote imports, so it also runs inside the cluster without internet egress.
import { Counter, Trend } from 'k6/metrics';

export const status2xx = new Counter('status_2xx');
export const status400 = new Counter('status_400');
export const status401 = new Counter('status_401');
export const status503 = new Counter('status_503_rejected');   // concurrency cap engaged
export const status504 = new Counter('status_504_timeout');    // ESA process budget exceeded
export const status5xx = new Counter('status_5xx_other');
export const statusErr = new Counter('status_transport_error'); // timeouts / resets (status 0)
export const okLatency = new Trend('ok_latency', true);         // latency of 2xx only

const env = (k, d) => (__ENV[k] !== undefined && __ENV[k] !== '' ? __ENV[k] : d);
export { env };

// Refuse to point at production by mistake.
export function guardTarget(url) {
  if (/preonboarding|prod/i.test(url) && env('I_KNOW_THIS_IS_NOT_PROD', '') !== 'yes') {
    throw new Error(`Refusing to run: target "${url}" looks like production. Load tests are staging-only.`);
  }
}

// RFC 4122 v4 UUID without crypto imports (randomness quality is irrelevant here).
export function uuidv4() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

// "1:2m,2:3m,4:3m" -> [{target:1,duration:'2m'}, ...]
function parseStages(s) {
  return s.split(',').map((p) => {
    const [target, duration] = p.trim().split(':');
    return { target: Number(target), duration };
  });
}

// PROFILE selects the workload model. All use the open model (arrival rate), because
// production traffic does not slow down when the service does.
export function buildScenario() {
  const profile = env('PROFILE', 'smoke');
  const maxVUs = Number(env('MAX_VUS', '600'));
  const pre = Number(env('PRE_VUS', '20'));
  switch (profile) {
    case 'smoke':
      return { executor: 'per-vu-iterations', vus: 1, iterations: Number(env('ITERATIONS', '3')), maxDuration: '10m' };
    case 'ramp':   // calibration / break-point: step the arrival rate up
      return {
        executor: 'ramping-arrival-rate', startRate: 0, timeUnit: '1s',
        preAllocatedVUs: pre, maxVUs,
        stages: parseStages(env('STAGES', '1:2m,1:2m,2:3m,4:3m,6:3m,8:3m,12:3m,16:3m,24:3m')),
      };
    case 'steady': // hold a fixed rate
      return {
        executor: 'constant-arrival-rate', rate: Number(env('RATE', '2')), timeUnit: '1s',
        duration: env('DURATION', '15m'), preAllocatedVUs: pre, maxVUs,
      };
    case 'burst':  // campaign blast: jump from idle straight to RATE
      return {
        executor: 'ramping-arrival-rate', startRate: 0, timeUnit: '1s', preAllocatedVUs: pre, maxVUs,
        stages: [
          { target: Number(env('RATE', '20')), duration: '10s' },
          { target: Number(env('RATE', '20')), duration: env('DURATION', '3m') },
          { target: 0, duration: '10s' },
        ],
      };
    default:
      throw new Error(`Unknown PROFILE "${profile}" (smoke|ramp|steady|burst)`);
  }
}

// Stop automatically once the service is clearly saturated, so a break-point ramp doesn't
// keep hammering staging. ABORT_ERROR_RATE=1 disables it.
export function buildThresholds() {
  const abortRate = Number(env('ABORT_ERROR_RATE', '0.3'));
  const t = { dropped_iterations: ['count<1'] }; // non-zero => k6 could not deliver the rate; result invalid
  if (abortRate < 1) {
    t.http_req_failed = [{ threshold: `rate<${abortRate}`, abortOnFail: true, delayAbortEval: '60s' }];
  }
  return t;
}

export function record(res) {
  const s = res.status;
  if (s >= 200 && s < 300) { status2xx.add(1); okLatency.add(res.timings.duration); }
  else if (s === 400) status400.add(1);
  else if (s === 401) status401.add(1);
  else if (s === 503) status503.add(1);
  else if (s === 504) status504.add(1);
  else if (s >= 500) status5xx.add(1);
  else if (s === 0) statusErr.add(1);
}

function m(data, name, field) {
  const x = data.metrics[name];
  return x && x.values && x.values[field] !== undefined ? x.values[field] : 0;
}

// Compact console summary + full JSON written next to the run (RESULTS_DIR).
export function summarize(data, label) {
  const dur = (data.state && data.state.testRunDurationMs ? data.state.testRunDurationMs : 0) / 1000;
  const total = m(data, 'http_reqs', 'count');
  const ok = m(data, 'status_2xx', 'count');
  const lines = [
    `=== ${label} | PROFILE=${env('PROFILE', 'smoke')} MODE=${env('MODE', 'sync')} | ${dur.toFixed(0)}s ===`,
    `requests            ${total}  (${(total / (dur || 1)).toFixed(2)}/s achieved)`,
    `2xx                 ${ok}  (${(ok / (dur || 1)).toFixed(2)}/s)`,
    `503 cap-rejected    ${m(data, 'status_503_rejected', 'count')}`,
    `504 ESA timeout     ${m(data, 'status_504_timeout', 'count')}`,
    `other 5xx           ${m(data, 'status_5xx_other', 'count')}`,
    `400 / 401           ${m(data, 'status_400', 'count')} / ${m(data, 'status_401', 'count')}`,
    `transport errors    ${m(data, 'status_transport_error', 'count')}`,
    `dropped iterations  ${m(data, 'dropped_iterations', 'count')}   (must be 0 for a valid run)`,
    `max VUs in use      ${m(data, 'vus_max', 'value')}`,
    `2xx latency ms      p50=${m(data, 'ok_latency', 'med').toFixed(0)} p90=${m(data, 'ok_latency', 'p(90)').toFixed(0)} p95=${m(data, 'ok_latency', 'p(95)').toFixed(0)} p99=${m(data, 'ok_latency', 'p(99)').toFixed(0)} max=${m(data, 'ok_latency', 'max').toFixed(0)}`,
    '',
  ];
  const dir = env('RESULTS_DIR', '.');
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const file = `${dir}/${label}-${env('PROFILE', 'smoke')}-${env('MODE', 'sync')}-${stamp}.json`;
  return { stdout: lines.join('\n'), [file]: JSON.stringify(data, null, 2) };
}
