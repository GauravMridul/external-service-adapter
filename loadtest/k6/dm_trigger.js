// Decision Manager: POST /decision-manager/v1/trigger-decision   (STAGING ONLY)
//
// MODE=sync  -> sends X-Process-Mode: sync; the request is held for the full decision, so latency
//              and concurrency are real. Use for calibration (ramp/steady).
// MODE=async -> production path; DM answers 200 "processing" immediately and works in the
//              background. Use for burst/admission tests and count completions from Mongo.
//
// Required env: API_KEY, PARTNER, STAGE, and LEAD_IDS (comma-separated staging Salesforce Lead Ids).
// Optional: BASE_URL (default: in-cluster staging service), URL (full override), PROFILE, RATE, ...
import http from 'k6/http';
import { check } from 'k6';
import { env, guardTarget, uuidv4, buildScenario, buildThresholds, record, summarize } from './lib.js';

const BASE_URL = env('BASE_URL', 'http://decision-manager-nodeport-service.staging.svc.cluster.local:8080');
const URL = env('URL', `${BASE_URL}/decision-manager/v1/trigger-decision`);
const MODE = env('MODE', 'sync');
const LEADS = env('LEAD_IDS', '').split(',').map((s) => s.trim()).filter(Boolean);
const RUN_ID = env('RUN_ID', `${Date.now()}`);

// Optional traffic mix: MIX="LOADTEST_KYC:loadtest-kyc:55,LOADTEST_LOAN:loadtest-loan:45"
// (partner:stage:weight). Use production's kyc/loan split from Mongo M2. Overrides PARTNER/STAGE.
const MIX = env('MIX', '')
  .split(',').map((s) => s.trim()).filter(Boolean)
  .map((s) => { const [partner, stage, w] = s.split(':'); return { partner, stage, w: Number(w || 1) }; });
const MIX_TOTAL = MIX.reduce((a, m) => a + m.w, 0);
function pickTarget() {
  if (MIX.length === 0) return { partner: env('PARTNER', ''), stage: env('STAGE', '') };
  let x = Math.random() * MIX_TOTAL;
  for (const m of MIX) { if ((x -= m.w) < 0) return m; }
  return MIX[MIX.length - 1];
}

guardTarget(URL);

export const options = {
  scenarios: { dm: buildScenario() },
  thresholds: buildThresholds(),
  discardResponseBodies: false,
  summaryTrendStats: ['med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  if (!env('API_KEY', '')) throw new Error('API_KEY is required');
  if (MIX.length === 0 && (!env('PARTNER', '') || !env('STAGE', ''))) throw new Error('PARTNER and STAGE (or MIX) are required');
  if (MIX.some((m) => !m.partner || !m.stage || !(m.w > 0))) throw new Error('MIX entries must be partner:stage:weight');
  if (LEADS.length === 0) throw new Error('LEAD_IDS is required (comma-separated staging Lead Ids)');
}

export default function () {
  // JSON keys are the json tags on TriggerDecisionRequest (Salesforce-style names).
  const appId = `LOADTEST_${RUN_ID}_${__VU}_${__ITER}`;
  const target = pickTarget();
  const body = JSON.stringify({
    Application_Id__c: appId,                          // required; LOADTEST_ prefix => easy cleanup
    RecordId__c: LEADS[(__VU + __ITER) % LEADS.length], // required; Salesforce Lead Id on staging
    LeadSource__c: target.partner,                     // required; must match partner_service_mapping
    StageEvent__c: target.stage,                       // required
    workflowId__c: uuidv4(),                           // optional, but keys the ESA audit docs
    Program_Type__c: env('PROGRAM_TYPE', ''),
    Product_Line__c: env('PRODUCT_LINE', ''),
  });
  const headers = {
    'Content-Type': 'application/json',
    'X-Api-Key': env('API_KEY', ''),
    'X-Correlation-ID': `loadtest-${RUN_ID}-${__VU}-${__ITER}`,
  };
  if (MODE === 'sync') headers['X-Process-Mode'] = 'sync';

  const res = http.post(URL, body, { headers, timeout: env('HTTP_TIMEOUT', '150s'), tags: { mode: MODE, stage: target.stage } });
  record(res);
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 });
  if (__ENV.DEBUG === '1' && (res.status < 200 || res.status >= 300)) {
    console.warn(`status=${res.status} body=${String(res.body).slice(0, 300)}`);
  }
}

export function handleSummary(data) {
  return summarize(data, 'dm');
}
