# Festive Season Load / Bulk Test & Scaling Plan — DM + ESA

**Status:** Executing. **Part A is the authoritative two-day runbook** (deadline: end of day 2). Part B, §1 onward, is the background analysis Part A relies on.
**Scope:** Decision Manager (DM) and External Service Adapter (ESA). Testing on **staging only**; production is read-only (Mongo read replica, Grafana).
**Supersedes / extends:** `docs/Load_Testing_Plan.md` (v1.0, 2026-02).
**Tooling:** `loadtest/` in this repo: k6 scripts, k8s manifests, SQL, samplers, sizing calculator. Validated locally (§A.10).

---

# Part A — Two-day execution plan

## A.0 Confirmed inputs (no further asks)

| Item | Confirmed position |
|---|---|
| Deployment YAMLs | `deployment.yml` (staging) and `deployment-prod.yml` (prod) in both repos are the live versions. |
| HPA | Production HPA exists and works as defined in `deployment-prod.yml` (min 2, max 7, CPU and memory at 70%). Grafana confirms it (§A.1). |
| Production nodes, DBs, Redis, ingress | **Infra's domain.** We hand over requirements and assumptions (§A.7), not specifics. |
| MongoDB | **v8**, read replica, read-only user. **Only the fast queries M0–M5** (query doc §F) are used: they window on `_id` and carry a 60s time limit. Q1–Q18 aren't run on production. Field names were confirmed by your Q0b/Q0c output; `workFlowId` is empty in production. |
| Production config | **Snapshot** of `partner_service_mapping`, `service_configuration`, `service_sfdc_field_mapping`, `query_object_relationship_map` (secrets redacted). Used for sequence shapes, provider hosts and SF mapping size (§A.1b). |
| Live traffic | **kyc-decision:** ZestMoney, Wishfin, SuperMoney, Oppo, Billcut, Vivo, Flipkart, Ola, DigitMoney, Realme, Turno, Tecno. **loan-decision:** GooglePay, Finnable, AirtelFinance, Oppo, Vivo, Realme, Tecno. |
| ESA Redis | Disabled in production. We only recommend it, as a §A.7 item. |
| Properties | The repo `*_production.properties` are taken as current, except DM's Mongo keys (§8A.6). This assumption is stated in the report. |
| Production | No changes, no load. Grafana and the Mongo read replica are read-only. |

## A.1 What production already shows (Grafana, read from your screenshots)

Prometheus has data only from **about 18 Sep**, so "90 days" is really about 17 days. Utilisation % is **relative to requests** (200m CPU; 512Mi DM / 256Mi ESA), not limits.

| | DM | ESA |
|---|---|---|
| CPU utilisation, mean / max | 19.1% / **78%**, so ~38m / **~156m per pod** | 19.1% / **92%**, so ~38m / **~184m per pod** |
| Memory utilisation, mean / max | 15.5% / 39%, so ~79Mi / **~200Mi per pod** | 35.7% / **107%**, so ~91Mi / **~274Mi per pod** |
| Replicas, mean / max reached | 2.16 / **4** | 2.27 / **4** |
| HPA conditions | AbleToScale yes, ScalingActive yes, **ScalingLimited no** | same |
| Requests / limits (pod dashboard) | 200m, 512Mi / 1 core, 1000Mi | 200m, 268MB (=256Mi) / 1 core, 1.05GB (=1000Mi) |

**What this means:**

- **Production peaks use about 16–18% of a pod's CPU limit and 20–27% of its memory limit.** The fleet has never reached `maxReplicas` (peak 4 of 7).
- **The HPA scales out long before a pod is busy.** It triggers at 140m CPU, or 179Mi memory (ESA) / 358Mi memory (DM). That's conservative, and fine.
- **ESA scale-outs are driven by memory more than CPU** (memory max 107% of request vs CPU 92%). Go releases heap memory slowly, so memory-triggered scale-outs tend to persist after the load spike has passed.
- **What production can't tell us** is how much one pod can actually process. Nothing has pushed a pod past ~18% CPU. That number comes from the staging tests (§A.5).

## A.1a Measured production baseline (Mongo read replica, 6 Oct)

| Measure | Value | Source |
|---|---|---|
| Decisions, 14 full days (22 Sep–5 Oct) | 845,054; about 60,400/day | M0 (DM) |
| Busiest day | Mon 5 Oct: 74,462 (0.86/s averaged over 24 h) | M0 |
| Week over week | 54,247/day → 66,475/day (**+22.5%**) | M0 |
| ESA documents, 5 Oct | **1,972,155** | ESA one-day count |
| ESA documents per decision | **26.5** (total Mongo documents per decision: **27.5**) | ESA count ÷ DM count |
| Mongo documents written, 5 Oct | 2,046,617; about **23.7/s** averaged over 24 h (busy-hour rate is higher) | sum |
| **Peak minute** | **3.42 decisions/s** (205 in 5 Oct 16:14). Bursts last 5–15 min (5 Oct 16:14–16:18; 6 Oct 08:37–08:53, ~2.5–3/s) | M1 |
| **Peak hour** | **2.10 decisions/s** (7,560 in 5 Oct 19:00). Peak minute ÷ peak hour = 1.6; peak hour ÷ day average = 2.4 | M1 |
| Decision time, loan-decision, 5 Oct 11:00 (not peak) | p50 **5.7–10.2 s**, p95 **10.2–21.1 s**; slowest is Oppo (p50 10.2 s, p95 21.1 s). **~80% is ESA**, Salesforce composite ~1.1 s p50, DM itself ~0.3 s | M2 |
| Peak in flight, fleet-wide, 5 Oct 11:00 | **22** decisions (at ~0.94/s) | M5 |
| **DM audit document size** | **avg 923 KB, p95 3.2 MB, max 12.2 MB** (16 MB is MongoDB's hard limit) | M4 |
| ESA audit document size | avg 37 KB, p95 169 KB, max 5.7 MB | M4 |
| **Mongo data written, 5 Oct** | ≈ 65.5 GiB DM + 70.2 GiB ESA ≈ **136 GiB/day uncompressed BSON** (sample-based estimate) | M0 × M4 |
| Indexes | **No index on `createdAt`, and no TTL index**, on either audit collection. That explains why Q1 scanned the whole collection, and it means audit data is never expired. | `getIndexes()` |

**Why 26.5 ESA documents per decision (M6, 5 Oct 11:00–11:10 IST):** ESA `loan-decision` 13,361 docs, `kyc-decision` 941, fan-out `per_call` + `aggregate` 14 (0.1%), no other stages; DM decisions 563, so **25.4 per decision**.

- Fan-out is negligible, and ESA has no callers other than DM.
- **Traffic is about 89% loan-decision.** At ~15 documents per kyc request, 941 kyc documents ≈ 63 kyc decisions, leaving ≈ 500 loan decisions at ≈ 27 documents each. That's consistent with traffic weighted towards GooglePay (30 services) and AirtelFinance (25–26).
- **Measured at the peak hour (M7, 5 Oct 19:00–19:15): 1,679 loan vs 147 kyc decisions = 92% / 8%.** Size for the loan path. The k6 `MIX` is **92/8 loan/kyc**, and the GooglePay loan clone is the primary test sequence.

**What the size and concurrency figures mean:**

- **The concurrency caps are nowhere near binding.** At the peak minute, Little's law gives ≈ 3.42/s × ~9 s ≈ 31 decisions in flight fleet-wide, with peaks perhaps 2–3× that. Across the 2–4 replicas observed, that's tens per pod, against caps of 200 (DM) and 266 (ESA).
- **DM's audit document duplicates ESA's.** `decisionManagerLog.EsaResponseBody = responseString` (`trigger_decision_service.go:564`) embeds ESA's **entire** response in the DM document, and ESA has already stored each service response in its own documents (~27 × 37 KB ≈ 1 MB, matching the 923 KB DM average). **Roughly half the Mongo storage and write bandwidth is a second copy.** Storing a reference instead (correlation id, or just the fields DM needs) would roughly halve the data-tier load. That's an application-side change.
- **Documents are already at MongoDB's 16 MB limit (M7, peak hour 5 Oct 19:00–19:15, all 1,826 DM documents):**

  | stage | docs | avg | p50 | p95 | p99 | max | > 4 MB | > 8 MB | > 12 MB |
  |---|---|---|---|---|---|---|---|---|---|
  | loan-decision | 1,679 | 1.07 MB | 562 KB | 3.8 MB | 9.1 MB | **14.4 MB** | 76 (4.5%) | 23 | **6** |
  | kyc-decision | 147 | 166 KB | 93 KB | 409 KB | 524 KB | 1.6 MB | 0 | 0 | 0 |

  Six loan documents in 15 minutes were over 12 MB, and the largest was 1.6 MB under the limit. Documents **above** 16 MB can't be stored, so they don't appear here at all: DM logs `Failed to insert ESA log` with `an inserted document is too large` (driver `ErrDocumentTooLarge`), the decision itself completes, and its audit record is lost. **Confirmed 6 Oct: no production document has exceeded 16 MB, so no audit records are being lost yet.** Headroom at peak is ~1.6 MB (largest 14.4 MB), so a festive payload growth of ~10% on the largest responses would start losing them. The size is all on the loan path (ESA's full response embedded at `trigger_decision_service.go:564`). kyc is ~7× smaller.
- **Large documents cost memory per decision, not just storage.** DM holds the ESA response as a string, parses it into Go maps (typically several times the JSON size), and builds the audit document from it. A 14 MB response can mean tens to ~100 MB of transient heap for one decision, against `GOMEMLIMIT=900MiB`. A few such decisions at once can push a pod into heavy GC or OOM. **The staging mocks return tiny bodies, so they won't show this.** T1-DM needs at least one run with production-sized ESA responses (p95 ≈ 4 MB, p99 ≈ 9 MB) before its memory figures can be trusted.
- **The audit queues can hold a lot of memory.** DM's async audit queue holds up to **1,000** full documents (`DefaultAsyncDMLogQueueSize`; production sets no override). If Mongo writes slow down and the queue fills, that's ~0.9 GB at the average size, against a **1000 Mi** pod limit. ESA's 500-request queue holds ~27 documents per request, ~0.5 GB. A slow Mongo can therefore OOM-kill DM and ESA pods. Recommend reducing the queues to what a few seconds of peak traffic needs, and adding a byte-based cap.
- **Write bandwidth:** at the peak minute ≈ 3.42 × (0.9 MB + 27 × 37 KB) ≈ **6.4 MB/s and ~95 documents/s**; at 10× ≈ **64 MB/s and ~950 documents/s**. Add **no expiry** (no TTL index) and +22.5% week-over-week growth.

## A.1b What the production config snapshot shows

Derived from `local/prod_db_snapshot/` (gitignored; not in the repo), restricted to the live partner × stage pairs in §A.0. All 19 live pairs have a mapping row.

**Sequence shapes**

| | kyc-decision (13 mappings, 12 partners) | loan-decision (9 mappings, 7 partners) |
|---|---|---|
| Services per request | **11–18** (Oppo, Realme, Tecno and Vivo are the largest at 18) | **14–30** (GooglePay 30, AirtelFinance 25–26, Oppo/Vivo 24) |
| Groups (run one after another) | 3: two services → one wide parallel group (8–15) → `Actico_kyc` | 7–9, widest group 6–13 |
| ESA goroutines per request | up to 18 | up to 30 (and more where `KarzaGSTNonOTP` fans out) |

**Provider hosts, and why `MaxConnsPerHost=25` matters**

| Host | Calls in one request (max) | Live sequences using it | Note |
|---|---|---|---|
| `apim.quickwork.co` | **8** (GooglePay and AirtelFinance loan) | **22 of 22** | One third-party gateway fronting **14 services**. The most concentrated dependency, and the likely per-pod ceiling for loan-decision. |
| `name-match-v3-gtway…gateway.dev` | **4** (kyc: Oppo, Vivo, Realme, Tecno, Flipkart, Turno) | 18 | The likely per-pod ceiling for kyc-decision. |
| `api.karza.in` | 3 | 12 | |
| `3.111.20.117:5000` | 3 | 11 | **Raw IP.** Name-match DA. Possibly a single instance. |
| `decider.example.com:1024` (Actico) | 2 | **22 of 22** | **Internal** decision engine, on the path of every decision. |
| `13.232.8.18:8084` | 1 | 13 | **Raw IP.** Protean PAN. |
| 26 others | 1 | 1–15 | Including internal model APIs on GCP Cloud Run / API Gateway and AWS API Gateway. |

Each call holds a connection for its whole duration. Per pod, the ceiling for a sequence is therefore **25 ÷ (summed call-seconds to its busiest host per request)**. Example: if the 8 Quickwork calls in a GooglePay loan request average 0.7s, one ESA pod tops out at **25 ÷ 5.6 ≈ 4.5 GooglePay loan requests/s** however much CPU it has. Fleet-wide, 7 pods can hold **175** concurrent connections to Quickwork. `loadtest/scripts/gen_mock_from_snapshot.py` computes this for every live sequence from the M3 latencies (`analysis.txt`).

**Measured per-pod connection ceiling (M3: 5 Oct 11:00–11:05, 248 loan + 29 kyc sequences; `analysis.txt`).** Delays are weighted by how often production actually calls each service (`live_calls ÷ calls`, because `pre_execution` skips many). The p50 and p95 columns bracket the realistic range:

| Sequence | Binding host | Connection-s per request (p50 … p95) | Max requests/s per ESA pod (p50 … p95) |
|---|---|---|---|
| loan Oppo | Quickwork | 6.7 … 15.7 | **3.7 … 1.6** |
| loan GooglePay / AirtelFinance | Quickwork | 3.7 … 8.1 | 6.7 … 3.1 |
| loan Vivo | Quickwork | 3.7 … 7.6 | 6.8 … 3.3 |
| loan Realme / Tecno | Quickwork | 2.7 … 5.0 | 9.3 … 5.1 |
| loan Finnable | BureauBS_Ascore gateway | 1.4 … 3.9 | 17.9 … 6.4 |
| kyc (most partners) | Quickwork | 4.2 … 11.3 | 5.9 … 2.2 |
| kyc Turno / ZestMoney | Quickwork | 1.0 … 4.9 | 24 … 5.1 |

- **Quickwork binds every sequence except Finnable.** Its slow services: `TartanPANService` (p50 4.2 s, p95 11.3 s; Oppo loan), `phoneIntelligence_Datasutram` (4.8 s), `DataSutramPanProfile_kyc` (3.2 s, p95 10 s), `PANITRService`.
- **Today at the peak minute (3.42 decisions/s), the fleet holds ≈ 13 … 28 concurrent Quickwork calls.** At 10× that's ≈ 130 … 280, against the 175 that 7 pods permit. **The Quickwork question for festive is whether it tolerates ~150–300 concurrent calls without slowing down.** If it slows, connection-seconds rise and every pod's ceiling falls with them.
- The mocks' p50 wall time (e.g. Oppo 6.4 s) is ~2 s below production's ESA p50 (8.7 s), because a sum of per-service medians understates a sequence's median and ESA's own overhead is excluded. Run T1-ESA-R once with the **p95** profile as well (`--percentile p95`).

**Salesforce mapping size:** 63 live mappings with **149 sub-request templates**; the largest are CRIF (16) and CIBIL (12). That templating is DM's main CPU cost on loan-decision. The mock sequences use empty mappings (no Salesforce writes), so they **don't** exercise it. T1-DM-R (§A.5) measures it on one real partner.

## A.2 Grafana — which pod? None: aggregate by container

Pod names change on every scale event (`external-service-adapter-6d746fbfd-xxxxx`; `6d746fbfd` is the ReplicaSet), so a per-pod dashboard isn't the right view. Run these in **Grafana → Explore** against `prometheus-eks-glimmerTech`, time range = last 90 days. They're read-only. For DM, replace `container="external-service-adapter"` with `container="decision-manager"`.

```promql
# P1  Fleet CPU in cores (sum of all pods) — peak = festive baseline for "how much CPU we use today"
sum(rate(container_cpu_usage_seconds_total{namespace="preonboarding", container="external-service-adapter"}[5m]))

# P2  Busiest single pod CPU (cores)
max(rate(container_cpu_usage_seconds_total{namespace="preonboarding", container="external-service-adapter"}[5m]))

# P3  Busiest single pod memory (MiB, working set — what the OOM killer and HPA see)
max(container_memory_working_set_bytes{namespace="preonboarding", container="external-service-adapter"}) / 1048576

# P4  CPU throttling ratio across the fleet (>0.05 sustained = CPU-starved; tests plan G2 on prod data)
sum(rate(container_cpu_cfs_throttled_periods_total{namespace="preonboarding", container="external-service-adapter"}[5m]))
  / sum(rate(container_cpu_cfs_periods_total{namespace="preonboarding", container="external-service-adapter"}[5m]))

# P5  Restarts and OOMKills in the window
sum(increase(kube_pod_container_status_restarts_total{namespace="preonboarding", container="external-service-adapter"}[90d]))
count(max_over_time(kube_pod_container_status_last_terminated_reason{namespace="preonboarding", container="external-service-adapter", reason="OOMKilled"}[90d]) > 0)

# P6  Replicas over time (to convert Mongo M5 fleet concurrency into per-pod)
kube_horizontalpodautoscaler_status_current_replicas{namespace="preonboarding", horizontalpodautoscaler="external-service-adapter-hpa"}
```

To get a single peak number for P1–P4, set the panel legend to show **Max**, or wrap the query: `max_over_time(( <query> )[90d:5m])`. If a cAdvisor metric returns nothing, the Prometheus setup may label containers differently. Check `container_cpu_usage_seconds_total{namespace="preonboarding"}` in Explore and copy the label it uses.

## A.3 What is delivered by end of day 2

1. **Per-pod capacity** for DM and ESA: decisions/sec at the **knee** (latency starts rising, or 503s/CPU saturation) and the **safe planning value** (70% of the knee). Includes CPU and memory per pod at both points, and **which resource binds** (CPU, connections-per-provider, concurrency cap, or data tier).
2. **Current production ceiling:** `7 pods × safe per-pod rate`, expressed as **X× today's peak** (Mongo M1, top row).
3. **Sizing table** for 1×, 2×, 3×, 5× and 10× today's peak: DM pods, ESA pods, CPU/memory to reserve, Postgres connections, Mongo writes/sec and GB/day. Generated by `loadtest/scripts/size_fleet.py`.
4. **Application-side recommendations:** `maxReplicas`, pod requests, the GOMAXPROCS result, DB pool sizes, concurrency caps.
5. **Infra raise list** (§A.7) with the numbers from item 3 filled in.

## A.4 Day 1 (today) — baseline and staging setup  ·  ~3.5 h

**1. Mongo baseline on the production read replica (~20 min).** Use **`Baseline_Extraction_Queries.md` §F**. If the earlier Q1 is still running, stop it first with §F.0; your own read-only user can do this. Then run, in order:

- **M0**, **M1**. Index only, safe over 14 days. Note today's peak decisions/sec and the busiest hours.
- Set `WIN_START` to the start of one of those busy hours.
- **M2**, **M3**, **M4**, then optionally **M5**. If any returns `MaxTimeMSExpired`, halve `HOURS`.

From the results, note:

- **Today's peak, decisions/sec:** M1, top row.
- **`T_hold` p95 and the kyc/loan split:** M2.
- **Per-service p50/p95:** M3. **Save M3's raw shell output to `~/m3.txt`**; the generator in step 5 reads it.
- **Document sizes:** M4.
- **Documents per decision:** M0, `esa_docs ÷ dm_docs + 1`.
- **Sequence shapes and provider hosts** come from the snapshot (§A.1b), not Mongo.

**2. Grafana P1–P6 (~20 min).** Record the peak of each.

**3. Staging pre-flight (~15 min).**

```bash
kubectl config current-context                     # must be the STAGING cluster
kubectl -n staging get deploy,svc,cm | grep -E "decision-manager|external-service-adapter"
kubectl top nodes; kubectl describe nodes | grep -A6 "Allocated resources"   # headroom for up to 7+7 pods @ 1 core / 1000Mi
mkdir -p ~/loadtest-backup && kubectl -n staging get deploy decision-manager-uat external-service-adapter-uat -o yaml > ~/loadtest-backup/deploy.yaml \
  && kubectl -n staging get cm decision-manager-config external-service-adapter-config -o yaml > ~/loadtest-backup/cm.yaml
```

Also check **Salesforce UAT API headroom** (Setup → System Overview → API Requests, Last 24 Hours). Each test decision makes **one Apex callback** to the sandbox (`/services/apexrest/decisioncallbackapi`). The day-2 plan generates roughly **20–40k decisions**. If the sandbox's daily allowance can't absorb that alongside other UAT users, see the option in step 6.

**4. Make one staging pod production-shaped (~30 min).** Per-pod capacity is only valid if a staging pod is resourced and configured exactly like production.

```bash
# Resources and runtime env = deployment-prod.yml
kubectl -n staging set resources deploy/decision-manager-uat       --requests=cpu=200m,memory=512Mi --limits=cpu=1,memory=1000Mi
kubectl -n staging set resources deploy/external-service-adapter-uat --requests=cpu=200m,memory=256Mi --limits=cpu=1,memory=1000Mi
kubectl -n staging set env deploy/decision-manager-uat       GOMEMLIMIT=900MiB
kubectl -n staging set env deploy/external-service-adapter-uat GOMEMLIMIT=900MiB MEMORY_LIMIT_BYTES=1048576000
```

Then edit the two staging ConfigMaps (`kubectl -n staging edit cm …`) so the keys below match production (from §5.5.1):

| Service | Key | Staging → set to |
|---|---|---|
| both | `log.level` | `debug` → **`1`** (debug logging is a large CPU cost; leaving it on understates capacity) |
| both | `database.maxOpenConnections` / `maxIdleConnections` | 200/20–21 → **500/50** |
| DM | `server.max_concurrent_trigger_decisions` | 15 → **200** |
| DM | `server.max_parallel_dynamic_json_template_goroutines` | 8 → **remove** (prod uses the default, 20) |
| DM | `server.dynamic_json_expr_cache_max_entries` | 200 → **remove** (prod default, 500) |
| DM | `server.timeout` | 30 → **60** |
| ESA | `server.timeout` / `RestExecuteTimeoutInSeconds` | 30/45 → **120/60** |
| ESA | `server.concurrency_base_reserve_mb` / `_min` / `_max` | 128 / 10 / 150 → **200 / 20 / 400** (effective cap 266) |
| ESA | `audit.log.async.esa_queue_size` | 100 → **500** |
| both | `telemetry.*` | **keep staging values** (DM telemetry on). The one deliberate difference from prod. |

Restart so the new caps and pools take effect (the limiters are sized at startup), then confirm:

```bash
kubectl -n staging rollout restart deploy/decision-manager-uat deploy/external-service-adapter-uat
kubectl -n staging rollout status  deploy/decision-manager-uat && kubectl -n staging rollout status deploy/external-service-adapter-uat
kubectl -n staging logs deploy/external-service-adapter-uat | grep -m1 "Sequence request limiter initialized"   # expect 266
kubectl -n staging logs deploy/decision-manager-uat       | grep -m1 "Trigger decision limiter initialized"      # expect 200
```

**5. Production-shaped mock sequences, so load never reaches real providers (~30 min).**

Generate clones of the two largest live sequences from the snapshot: **Tecno kyc-decision (18 services) and GooglePay loan-decision (30 services)**. They keep the same group and parallel structure, the same provider-host grouping (one mock hostname per real host, so the 25-per-host limit binds exactly as in production) and production p50 delays from M3. Output goes to `~/loadtest-generated/`, outside the repo, because it contains production service and host names.

```bash
cd external-service-adapter/loadtest
./scripts/gen_mock_from_snapshot.py --snapshot ../local/prod_db_snapshot --latencies ~/m3.txt
#   prints analysis.txt: per live sequence -> services, groups, wall time, binding host, per-pod req/s ceiling
#   --clone 14:kyc,1:loan  picks other sequences (partner_service_mapping ids from §A.1b)

kubectl apply -n staging -f k8s/mock-provider.yaml               # mock pods (+3 simple Services)
kubectl apply -n staging -f ~/loadtest-generated/mock-hosts.yaml  # 32 Services, one per production host
kubectl -n staging rollout status deploy/loadtest-mock-provider

psql "$ESA_STAGING_URL" -v ON_ERROR_STOP=1 -f ~/loadtest-generated/esa_mock.sql
#   prints two rows:  kyc | Tecno / kyc-decision | <seq_kyc>    and    loan | GooglePay / loan-decision | <seq_loan>
psql "$DM_STAGING_URL" -v ON_ERROR_STOP=1 -v seq_kyc='<seq_kyc>' -v seq_loan='<seq_loan>' -f ~/loadtest-generated/dm_mock.sql
#   prints sequence ids -> SEQUENCE_ID for ESA-direct tests
```

This creates partners **`LOADTEST_KYC` / stage `loadtest-kyc`** and **`LOADTEST_LOAN` / stage `loadtest-loan`**. The DM side gets **empty Salesforce field mappings (`[]`)**, so DM runs its full pipeline but performs **no Salesforce object writes**. Verified against DM's real merge and interpolation code: 0 sub-requests, and DM then skips the composite call (`salesforce_client.go:232`).

**Switching latency profiles between runs** needs no restart, because ESA reads config per request with Redis off:

- **Fast profile** (0.2s everywhere, for CPU-bound runs):
  ```bash
  ./scripts/gen_mock_from_snapshot.py --snapshot ../local/prod_db_snapshot --profile fast --out ~/loadtest-generated/fast
  psql "$ESA_STAGING_URL" -f ~/loadtest-generated/fast/set_delays.sql
  ```
- **Back to real latencies:** `psql "$ESA_STAGING_URL" -f ~/loadtest-generated/set_delays.sql`

Validated: generator run on your snapshot; generated SQL run against the repos' migrations. The group structure was preserved exactly (kyc `[2,15,1]`, loan `[4,1,7,1,13,1,1,1,1]`), DM's lookup finds the clones, and `99_cleanup.sql` soft-deletes all of it.

*Simpler fallback:* `sql/01_esa_mock_services.sql` and `sql/02_dm_mock_mapping.sql` create one small 4-service sequence (`LOADTEST_PARTNER` / `loadtest`). Use it if the generated clones hit any problem.

**6. Smoke test in-cluster (~20 min).**

```bash
kubectl -n staging create configmap k6-scripts --from-file=k6/lib.js --from-file=k6/dm_trigger.js --from-file=k6/esa_sequence.js
kubectl -n staging create secret generic k6-keys --from-literal=DM_API_KEY='<staging Dm_Api_Key>' --from-literal=ESA_API_KEY='<staging EsaApiKey>'
# in k8s/k6-runner.yaml set: PROFILE=smoke, MODE=sync, PARTNER=LOADTEST_KYC, STAGE=loadtest-kyc, LEAD_IDS=00QLOADTEST0000001
# then repeat with PARTNER=LOADTEST_LOAN, STAGE=loadtest-loan
kubectl -n staging apply -f k8s/k6-runner.yaml && kubectl -n staging logs -f k6-run
```

Expect 3 × 2xx each. Then confirm in **staging** Mongo that `decision-manager-log` has documents with `esaRequestBody.applicationid` starting `LOADTEST_`, and that ESA wrote one `esa-log` document per service (18 for kyc, 30+ for loan) with `serviceName` starting `LOADTEST_KYC_` / `LOADTEST_LOAN_`.

A service whose production config uses `pre_execution` or `PreExecution` rules is cloned **without** them; the clone always calls. That slightly overstates load compared with production, where some calls are skipped. It's the conservative direction.

**Use a dummy `RecordId__c` (e.g. `00QLOADTEST0000001`), not a real staging lead.** DM's Apex callback for a non-existent lead fails, and DM only logs a warning and completes the decision (`trigger_decision_service.go:658-670`). That way no staging lead record is touched.

*Optional, only if staging can be reserved for the test window:* point staging DM's `salesforce.baseURL` at `http://mock-kyc.staging.svc.cluster.local/anything` so the Apex callback also hits the mock. That removes Salesforce from the loop entirely, but it also stops staging DM writing to Salesforce for every other UAT user while it's set. Revert straight after.

## A.5 Day 2 — the tests  ·  ~6 h including analysis

Run every test **in-cluster** with `k8s/k6-runner.yaml`, and run the sampler on your laptop for the whole session:

```bash
./loadtest/scripts/sample_staging.sh ~/loadtest-results/day2 15        # leave running; Ctrl-C at the end
# after each test:
./loadtest/scripts/summarize_samples.sh ~/loadtest-results/day2 <start ISO> <end ISO>
kubectl -n staging logs k6-run > ~/loadtest-results/day2/<test>.k6.txt
```

**Valid-run rule:** `dropped iterations` must be 0. If it isn't, k6 couldn't keep up; raise `MAX_VUS` and re-run.

| # | Test | Setup | k6 env | Time | Read off |
|---|---|---|---|---|---|
| **T1-ESA** | ESA per-pod **CPU** knee | ESA **1 replica**; **fast profile** (0.2s everywhere, `fast/set_delays.sql`) | `esa_sequence.js`, `SEQUENCE_ID`/`SEQUENCE_STRING` = **loan clone** (heaviest, 30 services), `PROFILE=ramp`, `STAGES=1:2m,2:3m,4:3m,6:3m,8:3m,12:3m,16:3m,24:3m` | 30 min | Highest rate before p95 rises >50% over its low-load value, or 503s appear, or CPU >900m / throttling >5%. CPU (m) and memory per pod at 50% and 100% of that rate. Repeat briefly (10 min) with the **kyc clone** for its own figure. |
| **T1b** | GOMAXPROCS A/B | Same as T1-ESA (loan clone) plus `kubectl -n staging set env deploy/external-service-adapter-uat GOMAXPROCS=1` | Repeat T1-ESA | 30 min | Knee and p95 vs T1-ESA. A clearly higher knee or lower p95 means the plan's G2 finding holds; recommend `GOMAXPROCS`/`automaxprocs` (images are Go 1.23, so the runtime doesn't do this itself). Unset afterwards. |
| **T1-ESA-R** | ESA with **real latencies** | ESA 1 replica; delays = **production p50s from M3** (`set_delays.sql`) | Loan clone, `PROFILE=ramp`, `STAGES=0.5:2m,1:3m,2:3m,3:3m,4:3m,6:3m,8:3m`; then the kyc clone | 30 min | Knee under realistic waiting. Expect the 25-connections-per-host limit to bind first, at roughly `pod req/s cap` from `analysis.txt` (Quickwork for loan, the name-match gateway for kyc). If the measured knee matches that column, the connection limit is the binding constraint. |
| **T1-DM** | DM per-pod knee (mock) | DM **1 replica**, ESA **3 replicas** (so ESA isn't the bottleneck); fast profile | `dm_trigger.js`, `MODE=sync`, `MIX=LOADTEST_KYC:loadtest-kyc:<kyc %>,LOADTEST_LOAN:loadtest-loan:<loan %>` (split from M2), `PROFILE=ramp`, `STAGES` as T1-ESA | 30 min | Same as T1-ESA, for DM. **Excludes Salesforce templating** (empty mappings), so it's an upper bound. |
| **T1-DM-R** | DM CPU per **real** decision | One real staging partner/stage (e.g. GooglePay loan-decision as configured on staging) with its real SF mappings; DM 1 replica | `dm_trigger.js`, `MODE=sync`, `PARTNER`/`STAGE` = that partner, `LEAD_IDS` = a few real staging leads, `PROFILE=steady`, **`RATE=0.3`**, `DURATION=10m` | 15 min | DM CPU-ms per decision = (avg DM CPU m − idle m) ÷ rate. Compare with T1-DM at the same rate; **DM safe rate = T1-DM safe rate × (mock CPU-ms ÷ real CPU-ms)**. About 180 real decisions; this hits UAT providers and the SF sandbox, so choose the partner and timing. |
| **T-PE** | **Production-equivalent fleet** | DM and ESA at **N replicas** (7 if staging fits, else 3–4); real-latency profile | `dm_trigger.js`, `MODE=sync`, `MIX` as T1-DM, `PROFILE=steady`, `RATE = N × safe DM rate`, `DURATION=15m`; then RATE ×1.3 for 10m | 30 min | Does throughput scale linearly? Staging Postgres `pg_stat_activity` count during the run (query doc §8); staging Mongo write rate. Any data-tier errors in logs. |
| **T5** | **Burst / campaign blast** | `kubectl apply -n staging -f k8s/staging-hpa.yaml`; both at 2 replicas; real-latency profile | `dm_trigger.js`, `MODE=async`, `MIX` as T1-DM, `PROFILE=burst`, `RATE = 3 × (2 × safe DM rate)`, `DURATION=3m` | 30 min | 503 count, `kubectl -n staging get hpa -w` (time to scale out), and **completions**: staging Mongo DM docs for the run vs k6 2xx. Delete the HPA afterwards. |

Stop a ramp early once the knee is clear. The scripts also auto-abort if more than 30% of requests fail for 60s (`ABORT_ERROR_RATE`). The rate at which a pod is knee-bound is the result; going further only adds noise.

**If time is short:** T1-ESA, T1-DM, T1-DM-R and T-PE are the minimum; they produce deliverables 1–3. T1b, T1-ESA-R and T5 make the recommendations stronger.

## A.6 Turning results into numbers

- **Safe per-pod rate** = 0.7 × knee rate. That leaves headroom for variance and is the planning number.
- **Per-pod CPU and memory to reserve** = usage measured at the safe rate. Today's requests (200m) reserve far less than a busy pod uses. The scheduler then packs pods onto nodes that can't give each one its full 1-core limit at once, so **requests should rise to the measured value** before festive (an Infra and app item).
- **Run the calculator:**

```bash
./loadtest/scripts/size_fleet.py --peak <M1 top per_sec> --dm-pod <safe DM> --esa-pod <safe ESA> \
  --docs <M0: esa_docs/dm_docs + 1> --doc-kb <M4: DM avg_kb + (docs-1) x ESA avg_kb, / docs> --dm-pg <pg conns/DM pod> --esa-pg <pg conns/ESA pod> \
  --dm-cpu <m at safe> --esa-cpu <m at safe> --dm-mem <Mi at safe> --esa-mem <Mi at safe>
```

  It prints the current 7-pod ceiling as a multiple of today's peak, plus the 1–10× sizing table.
- **Latency-bound check:** per-pod capacity is the **smallest** of: (a) the CPU knee (T1-ESA / T1-DM, adjusted by T1-DM-R); (b) the connection ceiling, `pod req/s cap` in `analysis.txt`, per sequence, confirmed by T1-ESA-R; (c) the concurrency cap, `C_cap ÷ T_hold_p95` (DM: 200 ÷ M2 `p95_ms`/1000; ESA: 266 ÷ M2 `esa_p95`/1000). Report which one binds for kyc and for loan.
- **Caveat on mocks:** mock responses are small and empty Salesforce mappings skip DM's templating. T1-DM-R corrects DM for the second. For the first, compare real ESA document sizes (M4) with the staging mock-run documents; if real ones are much larger, report ESA's CPU knee as optimistic by roughly that ratio for JSON-heavy services.

## A.7 Raise to Infra (from the application's point of view)

The application can be scaled to the pod counts in the sizing table. **These dependencies must scale with it, or correctly sized services will still fail.** Numbers come from the §A.6 output. Timings are standard AWS / MongoDB Atlas behaviour, for planning.

| Area | What we need from Infra | Why (dev view) | Typical change lead time |
|---|---|---|---|
| **EKS capacity** | Node pool can hold **max DM pods + max ESA pods × the reserved CPU/memory** from the sizing table, with HPA headroom. Confirm node autoscaling (Cluster Autoscaler or Karpenter) and its max. | HPA adds pods in seconds, but only if a node has room. | Pod scale-out **~30–90 s** (HPA sync 15 s + metrics + start + readiness). New node: **~1–2 min with Karpenter, ~2–5 min with Cluster Autoscaler + managed node group.** For known campaign windows, **raise `minReplicas` beforehand** instead of relying on reaction time. |
| **Aurora PostgreSQL** (DM and ESA DBs) | Confirm `max_connections` and instance class can serve **"PG conns" from the sizing table**; say whether DM and ESA share a cluster; consider RDS Proxy. | Each pod is configured for up to **500** connections (`maxOpenConnections`). At 7 pods that's 3,500 per service, while the default Aurora limit is `LEAST(DBInstanceClassMemory/9531392, 5000)`, about 1,800 for a 16 GiB instance. ESA also reads config from Postgres on **every** request, because Redis is off. | Instance-class change causes downtime on that instance (minutes). With a reader in the cluster and failover, the writer interruption is typically tens of seconds. Adding a reader takes ~10–20 min. Schedule **before** festive. |
| **MongoDB Atlas (v8)** | Tier, IOPS and storage able to take **"Mongo w/s" and "GB/day"** from the table for the festive window; per-node connection limit covering all pods. | Every decision writes **1 DM + about S ESA documents** (M0), with full payloads embedded (M4). Measured: **27.5 documents per decision, ≈2.05M a day and ≈136 GiB/day uncompressed BSON on 5 Oct**. DM documents average 923 KB, max 12.2 MB, near the 16 MB limit. Peak-minute write rate ≈ 6.4 MB/s; at 10× ≈ 64 MB/s. **No TTL index** on either audit collection, so nothing expires; a retention policy is needed before festive. Ask for current disk used vs provisioned, and write IOPS headroom. DM's Mongo pool is **unbounded** by config. | Tier change is a rolling upgrade with no downtime, typically tens of minutes, with a brief primary election (seconds) that the driver retries. **Compute auto-scaling is reactive: it acts after sustained high usage, on the order of an hour, so it won't catch a short burst. Pre-scale.** Storage auto-scales near full. M10/M20 also cap new connections at 15/s per node, which matters when many pods start at once. |
| **Redis (ElastiCache)** | DM uses it today. If ESA caching is re-enabled for festive (recommended below), size the instance and `maxclients` for **(DM pods + ESA pods) × 50** connections. | Turning on ESA's cache removes most per-request Postgres reads, the cheapest way to cut DB load at 5–10×. | Node-type change: minutes to tens of minutes (online for replicated clusters). New cluster: ~10–15 min. |
| **Ingress / ALB** | Any rate limits on the DM route; if a sudden 10× is expected, discuss pre-warming. | An ingress limit would cap everything before our services see it. | ALB scales automatically, over minutes, for gradual growth. |
| **Internal services in every decision path** (from the snapshot) | Confirm these scale with festive load, or give their limits: **Actico decision engine** (`decider.example.com:1024`, `actico.example.com:1024`, called by all 22 live sequences); internal model APIs (`model-api-1`, `model-api-2`, `aa-calc-api` on `example.com`; GCP Cloud Run / API Gateway; AWS API Gateway `execute-api`); and two endpoints addressed by **raw IP**, `3.111.20.117:5000` (name-match DA, 11 sequences) and `13.232.8.18:8084` (Protean PAN, 13 sequences). | Scaling DM/ESA multiplies calls to these one-for-one: every decision calls Actico once or twice. A raw-IP endpoint is usually one instance with no load balancer, so it is a single point of failure and capacity. | Depends on each service's platform; owners to confirm. |
| **Third-party gateways** (provider/partner management, not Infra) | Festive concurrency allowance from **Quickwork** (`apim.quickwork.co`: 14 services, up to 8 calls per request, all 22 sequences) and the **name-match gateway** (up to 4 per request, 18 sequences). | Our per-pod limit of 25 connections per host means **7 pods = 175** concurrent calls to each of these. If their contracted limit is lower, raising `maxReplicas` will get us throttled. | Contractual. |

**Application-side changes we'll recommend in the report** (not applied to production by this exercise):

- `database.maxOpenConnections` from 500 to about 2× the measured per-pod peak.
- Set `mongo.maxPoolSize` for DM.
- Raise pod CPU/memory **requests** to the measured safe-rate usage.
- Raise `maxReplicas` to the sizing-table value.
- `GOMAXPROCS`, if T1b shows a gain.
- Re-enable ESA Redis, subject to the FRDP-104 history.

## A.8 Cleanup at the end of day 2

```bash
kubectl -n staging delete pod k6-run --ignore-not-found
kubectl -n staging delete -f loadtest/k8s/staging-hpa.yaml --ignore-not-found
kubectl -n staging delete -f loadtest/k8s/mock-provider.yaml
kubectl -n staging delete configmap k6-scripts; kubectl -n staging delete secret k6-keys
psql "$ESA_STAGING_URL" -v db=esa -f loadtest/sql/99_cleanup.sql
psql "$DM_STAGING_URL"  -v db=dm  -f loadtest/sql/99_cleanup.sql
kubectl -n staging set env deploy/external-service-adapter-uat GOMAXPROCS-                  # if T1b was run
kubectl apply -f ~/loadtest-backup/cm.yaml && kubectl apply -f ~/loadtest-backup/deploy.yaml  # restore staging
kubectl -n staging rollout restart deploy/decision-manager-uat deploy/external-service-adapter-uat
```

Staging Mongo test documents (`LOADTEST_` prefix) can be soft-deleted with the §10.7 statements.

## A.9 Known limits of the result (state these in the report)

- **Data tier:** staging Postgres, Mongo and Redis are not production-sized. Per-pod application capacity transfers; data-tier behaviour at scale doesn't. That's why §A.7 exists.
- **Providers are mocked.** Latency is modelled on production p50s; payload size and provider-side throttling aren't. The real-partner calibration in §A.6 narrows this.
- **Salesforce:** composite writes are skipped (empty mapping); the Apex callback still hits the sandbox, unless the optional mock in §A.4 step 6 is used.
- **Production history covers about 17 days** in Prometheus. Mongo covers whatever the audit collections retain.

## A.10 What was validated before handing this over

| Artifact | Validation |
|---|---|
| `mongo/fast_baseline.js` (query doc §F) | MongoDB **8.0.15**, logged in as a **`read`-only** user. All 9 statements return rows. M0/M1 read the index only (`docsExamined = 0`); M2–M5 read only their window. `maxTimeMS` stops a heavy query at the limit. Finding and killing your **own** running query works as `read`-only. Every pipeline passes Compass's parser. The doc text is byte-identical to the tested file. |
| `scripts/gen_mock_from_snapshot.py` | Run on **your production snapshot**. The generated SQL ran against the repos' migrations: group structure preserved exactly (kyc `[2,15,1]`, loan `[4,1,7,1,13,1,1,1,1]`), DM's lookup finds the clones, missing-variable guards fire, and `99_cleanup.sql` (now matching every `LOADTEST_` partner) soft-deletes everything. The 32 generated Services parse. |
| `k6/dm_trigger.js` `MIX` option | 55/45 mix at 20/s delivered 114/87 kyc/loan requests with 0 dropped iterations; plain `PARTNER`/`STAGE` still works. |
| `k6/dm_trigger.js`, `k6/esa_sequence.js`, `k6/lib.js` | Run with k6 v2.3.0 against a local stand-in that enforces the real request contracts (DM's `Application_Id__c` / `LeadSource__c` / `RecordId__c` / `StageEvent__c`; ESA's required keys; `X-Api-Key`) and a non-blocking cap that returns 503. Results: smoke 3/3 OK; ramp past the cap correctly counted 25 × 503 with 0 dropped iterations; async burst at 20/s; ESA steady 5/s. The production guard refuses `preonboarding`/`prod` targets. |
| `k8s/mock-provider.yaml` | go-httpbin **2.25.0**, the same version, run locally. GET and POST `/delay/<s>` honour fractional seconds; delays above the default 10s cap work with `MAX_DURATION=120s`; 200 concurrent 2s delays completed in 2s wall time. Manifests parse. |
| `sql/01`, `02`, `03`, `99` | Run against PostgreSQL 17 with the tables created from **the repos' own goose migrations**. Inserts, the sequence-string output, DM's exact `partner_service_mapping` lookup query, the delay update and the soft-delete cleanup all behave as intended; missing-variable guards fire. |
| Empty SF mapping (`[]`) | Unit-checked against DM's real `FastMergeServiceSfdcFieldMappingRequestBody` + `InterpolateValuesWithArrayExpansion`: 0 sub-requests, no error. |
| `scripts/sample_staging.sh`, `summarize_samples.sh`, `size_fleet.py` | Syntax-checked; refuses any namespace other than `staging`; summariser and calculator checked on sample inputs. |
| **Not validated** | Anything against your live staging cluster: resource headroom, ConfigMap key layout, network policies, image pulls from Docker Hub. The smoke test (§A.4 step 6) is the first live check. If staging can't pull from Docker Hub, mirror `grafana/k6:2.3.0` and `mccutchen/go-httpbin:2.25.0` into your registry. |

---

# Part B — Background analysis and reference

---

## 1. Executive summary

The method in the earlier plan is correct and still applies. What it did not have is a capacity model tied to the actual binding constraints in the code.

**The objective here is narrow and explicit:** determine how many cases one pod can process in parallel, how the system scales, and therefore how many pods to provision for a festive projection that does not exist yet. Everything below serves that. Limits that are set higher than the system ever reaches are not problems in themselves — the problem is not knowing where the real ceiling is.

### What decides festive capacity

| # | Constraint | Status | Bearing on sizing |
|---|---|---|---|
| 1 | **`T_hold`** — time a concurrency slot is held | Measurable **today** from Mongo, no load test needed | The throughput term. `λ_pod = C_cap / T_hold`. Dominates everything else. |
| 2 | **Per-provider connection limit** (ESA `MaxConnsPerHost=25`) | **Deliberate** provider protection | Per-pod ceiling per provider. **Enforced per pod, so fleet pressure = 25 × replicas** — see §6-G4. This is the one that needs attention before scaling out. |
| 3 | **CPU per pod** (1 core, `GOMAXPROCS` unset) | Misconfiguration | Likely the real per-pod ceiling. CFS throttling inflates `T_hold`, which reduces throughput quadratically through term 1. |
| 4 | **DM→ESA connection pooling** | Misconfiguration (`UseConnectionPool: false`) | Adds avoidable latency to every decision, inflating `T_hold`. |
| 5 | **HPA scale-out threshold** | Works, but triggers against `requests` not `limits` | Autoscaling **is** functioning (confirmed by your observation). The open question is whether `maxReplicas` is high enough, not whether it scales. See §5.2. |
| 6 | **Concurrency caps** (DM 200, ESA 266) | Possibly never reached | Query doc **Q8/Q14** measure whether they were ever approached. If not, they are harmless and not the thing to tune. |
| 7 | **Data tier** — Postgres, Mongo, Redis | Outside the services; scales only when Infra scales it | Every pod brings its own connection pool and write share, so 2 → 7 pods is **3.5x** the data-tier demand with no autoscaling of its own. **§8A** — the most likely way a correct service-scaling exercise still ends in an incident. |

### Two questions this answers

1. **What can production absorb right now?** Replicate prod config, resources, and `replicas=7` on staging and ramp to break — **T-PE, §5.5**. No production traffic. Run it *before* the fixes, and again after, to quantify each fix.
2. **What must we provision for festive?** `λ_pod` from single-pod calibration, then `N = λ_peak × margin / λ_pod`, validated against provider budgets **and the data-tier demand model in §8A.2**.

### Sequencing

1. **Verify the live ConfigMap matches the repo** (§8A.6). Two minutes, and it gates every config-derived conclusion here.
2. **Phase 0 — measure what exists.** `T_hold`, `λ_peak`, observed peak concurrency, per-provider latency, services per sequence, document sizes, current data-tier usage. All from Mongo/Postgres right now, zero risk. Queries: `Baseline_Extraction_Queries.md`.
3. **Hand the §8A.4 table to Infra early.** Data-tier scaling has a much longer lead time than pod scaling and may need a maintenance window.
4. **T-PE** — the current production ceiling, on staging.
5. **Fix items 3 and 4.** Both inflate `T_hold` with no upside. Re-run T-PE to measure the gain.
6. **Calibrate one pod** for `λ_pod` and the saturation order (CPU vs memory vs cap vs connections vs data tier).
7. **Size the fleet** when projections arrive, then validate `N` against both provider and data-tier limits.

Corrections from your review are folded in throughout; the two substantive ones are §5.2 (autoscaling) and §6-G4 (the 25-connection limit).

### Production boundary

Production is serving live traffic and is **off limits for any change or test**. In this plan, production is touched in exactly two ways, both read-only:

1. **You** run the read-only aggregations in `Baseline_Extraction_Queries.md` against the production **Mongo read replica**.
2. **Infra** answers the read-only enquiries in **§10.1 (E1–E11)**: `get`/`describe`, metrics, and catalog `SELECT`s. Nothing that applies, scales, restarts or writes.

All load testing, config changes, telemetry enablement and cluster commands are **staging only**. The plan's outputs are stated as *"staging measured X; production, as configured in E1/E3, therefore handles about Y; festive load Z needs N pods plus data-tier changes W."* Every changed production setting is a recommendation for whoever owns the production rollout, not something this exercise applies.

---

## 2. Verified architecture (what load actually flows through)

### 2.1 Decision Manager

`POST /decision-manager/v1/trigger-decision` — the only functional endpoint
(`internal/app/api/router/router.go:80`)

- **Auth:** `X-Api-Key`, constant-time compare (`trigger_decision_controller.go:78-84`).
- **Admission:** `TryAcquireTriggerDecision()` — non-blocking channel acquire. At capacity → **`503` + `Retry-After: 10`**, no queue (`common_modules.go:1167-1177`, controller `:104-112`).
- **Default mode is ASYNC** (`trigger_decision_controller.go:137-141`): responds `200 {"status":"processing"}` **immediately**, then does the work in a background goroutine. **The limiter slot is held for the entire background duration**, not for the HTTP response.
- **Sync mode:** header `X-Process-Mode: sync` (`constants.go:34`, controller `:115-116`). This is the load-test lever.
- **Per decision:** exactly **one blocking ESA call** (`trigger_decision_service.go:541-545`) via `RestExecuteWithConnectionRetry`, timeout `RestExecuteTimeoutInSeconds` (**prod 120s**), 2 connection retries. Retries are **connection-errors only** — explicitly *not* on timeouts or 5xx (`api_client.go:132-166`). Then Salesforce composite-graph writes + Mongo audit.
- **Per-request parallelism:** `dynamic_json_updater` template goroutines. Prod leaves `server.max_parallel_dynamic_json_template_goroutines` unset → default **20** (`constants.go:DefaultMaxParallelTemplateGoroutines`). Staging explicitly sets **8**. Inner cap and global cap both `0` = **unlimited**.

### 2.2 External Service Adapter

`POST /external-service-adapter/v1/process-sequence/v2` — sync only
(`internal/app/api/router/router.go:108`)

- **Admission:** `TryAcquireSequenceRequest()`, non-blocking → **`503` + `Retry-After: 10`** (`sequence_controller.go:84-92`).
- **Budget:** `server.process_sequence_max_seconds=120` → **`504`** with timeout diagnostics on exceed (`sequence_controller.go:96-124`).
- **Concurrency cap (prod):** memory-based — `(1000Mi − 200 reserve) / 3 MB = 266`, clamped 20–400 → **266/pod** (`common_modules.go:1285-1311`).
- **DAG execution:** services run in groups. For each service in a group a goroutine is started **first**, and it *then* blocks on `groupSem` (`sequence_service.go:613-623`). So **goroutine count scales with total services in the sequence**; `max_services_per_group` (default **15**) only caps how many *execute* at once.
- **Unset in prod, so running on defaults:** `max_services_per_group=15`, `fan_out_max_concurrent=5`, `max_sf_chunk_parallel=4`.
- **HTTP client:** `UseConnectionPool: true`, `MaxIdleConns=100`, `MaxIdleConnsPerHost=10`, **`MaxConnsPerHost=25`**, `DefaultRetryCount=3` (`common_modules.go:1017-1026`).
- **Redis is OFF:** `cache.enabled=false` in **both** prod and staging (FRDP-104). Every request resolves service config + query objects from Postgres.
- **Postgres:** `maxOpenConnections=500`, `maxIdleConnections=50` — **per pod**.
- **Mongo audit:** async, `esa_workers=2`, `esa_queue_size=500`, inline fallback 500ms.

### 2.3 The capacity model

Both services gate on a **non-blocking semaphore held for the full processing duration**. That makes this Little's Law, and it is the single most useful formula for this exercise:

```
λ_pod  =  min( C_cap / T_hold ,  λ_cpu ,  λ_conn )

C_cap   concurrency cap        DM: 200 (prod)   ESA: 266 (prod)
T_hold  mean seconds a slot is held  (≈ full decision time, incl. ESA + SF)
λ_cpu   CPU ceiling            = cores_available / CPU-seconds per request
λ_conn  connection ceiling     = MaxConnsPerHost / mean_call_seconds  (per provider host)

Replicas needed:   N = ceil( λ_peak × (1 + burst_margin) / λ_pod )
```

Two consequences worth stating plainly:

- **The caps are not the throughput knob people assume.** At `T_hold = 120s` (the timeout ceiling), DM's cap of 200 permits **1.67 decisions/sec/pod**. Raising the cap does not raise throughput — it only raises how many requests are simultaneously in flight, i.e. memory and CPU contention. Throughput is governed by `T_hold`.
- **`λ_conn` is currently the tightest term for ESA.** `MaxConnsPerHost=25` against a 266 cap means a sequence dominated by one provider saturates at 25 concurrent calls per pod, and the other 241 slots queue inside the 120s budget.

---

## 3. Pre-test baseline from data you already have (do this first, zero risk)

Before generating any load, mine what production has already recorded. This costs nothing, carries no risk, and produces the `T_hold` and `λ_peak` that everything else depends on.

**From DM Mongo (`decision-manager-log`):**
- `triggerDecisionTime` distribution — p50/p90/p95/p99/max. **This is `T_hold`.**
- Decisions per minute, by hour and day-of-week, over the last 60–90 days → **current `λ_peak`** and the diurnal curve.
- Failure/exception-log rate, and count of ESA 504s already occurring.

**From ESA Mongo (`esa-log`):**
- Total execution time per request, and per-service `timeTaken` → which provider dominates `T_hold`.
- Services-per-sequence distribution → drives the goroutine/memory model in §2.2.

**Where each part comes from (production is read-only, §1 "Production boundary"):**

| Source | Who runs it | What |
|---|---|---|
| Production **Mongo read replica** | You, via Compass | All of the above. Exact, verified queries are in `Baseline_Extraction_Queries.md`, Q0–Q18. |
| Production **cluster and data tier** | **Infra**, read-only | Replica history, deployed spec, HPA, node pool, resource usage, restarts, Postgres/Mongo/Redis limits. Enquiry list **§10.1, E1–E11**. |
| **Staging** cluster and DBs | You | Everything else: config drift, GOMAXPROCS/throttling (same image as prod), Postgres baseline, all load tests. §10.2–10.7. |

**Deliverable:** a one-page baseline: current peak throughput, `T_hold` percentiles, dominant provider, observed peak concurrency, current replica behaviour. Every target below is expressed as a multiple of this. Template: query doc §9.

---

## 4. Test approach

### 4.1 Principles

1. **Calibrate on one pod.** With an autoscaler active, an aggregate test measures the autoscaler's reaction rather than per-pod capacity — replicas change underneath you and `λ_pod` can't be recovered. Detach the HPA, pin to `replicas: 1`, and measure `λ_pod` and `C_cap` directly (§5.3). Highest-value test in the plan.
2. **Test ESA standalone before end-to-end.** ESA is the inner service and has its own 503/504 semantics. Isolate it, then layer DM on top.
3. **Run both modes, for different reasons.** `X-Process-Mode: sync` is the only way to see real latency and true backpressure. But **async is what production runs**, and async has different failure behaviour (caller gets `200` regardless). Both must be exercised.
4. **In async mode, measure completion from Mongo, not HTTP.** The HTTP `200` is meaningless — it precedes all work. Correctness and latency must come from audit records keyed on `correlation_id` / `lead_id`.
5. **Stub the providers for capacity tests.** See §9-D. Hitting the Salesforce UAT sandbox measures the sandbox's limits, not DMI's.
6. **One variable per run.** Record config, image tag, replica count, and `MEMORY_LIMIT_BYTES` for every run.

### 4.2 The `X-Process-Mode: sync` header — assessment

You flagged that you added this last time for load testing, and that real traffic uses the webhook-style async path. **Keep it, and use it for calibration. It is the right tool and the reasoning holds up.** Verified in code at `trigger_decision_controller.go:115-116` (`constants.ProcessModeHeaderKey = "X-Process-Mode"`).

Why it is necessary rather than merely convenient:

- In async mode, DM responds `200 {"status":"processing"}` **before any work happens** (`:137`). A load generator therefore measures admission only. k6 would see ~5ms responses, conclude there is spare capacity, and ramp arrival rate without bound — you'd measure how fast DM can accept requests, which is not a useful number.
- Sync mode holds the connection for the real duration, so client-side concurrency equals server-side concurrency. That makes the VU model meaningful and produces genuine latency percentiles.
- **The concurrency semantics are identical in both modes.** In both cases the limiter slot is acquired before the branch and held for the full processing duration — async releases it in the background goroutine's `defer` (`:145`), sync in the handler's `defer` (`:119`). So `T_hold` and `C_cap` measured in sync mode **transfer directly** to async production behaviour. This is the key property that makes sync-mode calibration valid.

Two caveats to design around:

- **`server.timeout=60` (prod) is shorter than the ESA call timeout of 120s** (G6). In sync mode a slow decision can have its HTTP response cut at 60s while the work continues, so k6 records a failure for a decision that actually succeeded. Reconcile k6 results against Mongo completions, or raise `server.timeout` on staging for the test window.
- **Async has one behaviour sync cannot show:** no caller backpressure. In production the caller cannot slow down because it already got its `200`. So async must still be exercised for the burst test (T5) and the admission/503 behaviour — see below.

**Use both, for different purposes:**

| Mode | Tests | Measures |
|---|---|---|
| **Sync** (`X-Process-Mode: sync`) | T1, T2, T3, T4, T6, T7 | `λ_pod`, `T_hold`, latency percentiles, saturation order |
| **Async** (production path) | T5, T8, T9 | Admission/503 behaviour under burst, work completion vs acceptance, autoscaler reaction |

For async runs, success must be counted from Mongo, not HTTP — §4.4.

### 4.3 Tooling — k6

**Yes, Grafana k6.** Open-source (AGPL-3.0) load-testing tool, now under Grafana Labs. Scripts are JavaScript; the engine is Go. Runs entirely locally as a single binary — no account, no cloud, nothing sent anywhere unless you explicitly opt into Grafana Cloud k6. Works fine on your Mac.

```bash
brew install k6
k6 version
```

Chosen over Postman Collection Runner for three reasons that matter here: it supports an **open workload model** (`constant-arrival-rate` — fixed requests/sec regardless of response time), which is how festive traffic actually behaves and which a closed VU model cannot represent once latency starts rising; it reports **native percentiles**; and it has **thresholds** for pass/fail criteria. Postman's runner is closed-model and sequential per runner, so it under-loads a slowing service — which is precisely when you most need accurate numbers.

Run it from the VPN-connected machine (your laptop is fine if it reaches the cluster). A starter script for the two modes:

```javascript
// dm-loadtest.js
import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';
import { Counter } from 'k6/metrics';

const rejected = new Counter('rejected_503');
const timedOut = new Counter('gateway_timeout_504');

export const options = {
  scenarios: {
    // Open model: fixed arrival rate. Use for T3/T4/T5.
    festive: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RPS || 5),
      timeUnit: '1s',
      duration: __ENV.DURATION || '10m',
      preAllocatedVUs: 50,
      maxVUs: 500,          // must exceed rate × p99_seconds
    },
    // Ramp model for T1 calibration: swap scenarios via --env
    // calibrate: {
    //   executor: 'ramping-arrival-rate',
    //   startRate: 1, timeUnit: '1s',
    //   preAllocatedVUs: 50, maxVUs: 500,
    //   stages: [
    //     { target: 2,  duration: '3m' }, { target: 5,  duration: '3m' },
    //     { target: 10, duration: '3m' }, { target: 20, duration: '3m' },
    //     { target: 40, duration: '3m' },
    //   ],
    // },
  },
  thresholds: {
    http_req_duration: ['p(95)<30000'],   // set from measured T_hold
    rejected_503: ['count<1'],            // any 503 = lost work
  },
};

export default function () {
  const headers = {
    'Content-Type': 'application/json',
    'X-Api-Key': __ENV.DM_API_KEY,
    // Sync for calibration (T1-T4, T6, T7); omit for async burst tests (T5, T8, T9)
    ...(__ENV.SYNC === 'true' ? { 'X-Process-Mode': 'sync' } : {}),
  };

  // Correlation id lets you join k6 results to Mongo audit records.
  const correlationId = `loadtest-${__VU}-${__ITER}-${Date.now()}`;
  headers['X-Correlation-Id'] = correlationId;

  // JSON keys are the Salesforce-style tags on TriggerDecisionRequest
  // (decision_manager_request_dto/trigger_decision_request_dto.go). Required: Application_Id__c,
  // LeadSource__c, RecordId__c, StageEvent__c.
  // LeadSource__c + StageEvent__c must match a partner_service_mapping row, otherwise no sequence runs.
  // RecordId__c is a Salesforce Lead Id: DM writes decision results back to that record in the
  // UAT sandbox, so use real staging leads (cycled from a CSV) or the SF stub from §9-D.
  const payload = JSON.stringify({
    Application_Id__c: `LOADTEST_${__VU}_${__ITER}`,
    RecordId__c: __ENV.LEAD_ID,                 // or pick from a SharedArray of staging lead ids
    LeadSource__c: __ENV.PARTNER,               // a real partner name that has a mapping on staging
    StageEvent__c: __ENV.STAGE || 'kyc',
    // Optional for both DM and ESA, but set it: ESA's audit documents carry it as workFlowId,
    // and the baseline/T-PE queries key each request on workFlowId + createdAt.
    workflowId__c: uuidv4(),
  });

  const res = http.post(`${__ENV.DM_BASE_URL}/decision-manager/v1/trigger-decision`,
                        payload, { headers, timeout: '130s' });

  if (res.status === 503) rejected.add(1);
  if (res.status === 504) timedOut.add(1);
  check(res, { 'accepted': (r) => r.status === 200 });
}
```

```bash
# T1 calibration, sync, single pod
k6 run --env SYNC=true --env RPS=2 --env DURATION=5m \
       --env DM_BASE_URL=https://staging.example.com \
       --env DM_API_KEY=... \
       --out json=t1_results.json dm-loadtest.js

# T5 burst, async (production path)
k6 run --env SYNC=false --env RPS=80 --env DURATION=3m dm-loadtest.js
```

`maxVUs` must exceed `rate × p99_seconds` or k6 itself becomes the bottleneck and silently under-delivers the requested rate. Watch the `dropped_iterations` metric — non-zero means k6 could not keep up and the run is invalid.

Use `LOADTEST_` prefixes on lead IDs so synthetic records are identifiable and removable afterwards.

### 4.4 Instrumentation

Required alongside every run:
- k6: RPS achieved, p50/p90/p95/p99, 503/504/5xx counts, `dropped_iterations`.
- Pod sampling every **15s** (the v1 plan's 60s misses bursts) → §10.2.
- `kubectl get pods -w` for restarts/OOMKills.
- Mongo for the window: completion count and `triggerDecisionTime` percentiles — **the authoritative result in async mode**.
- Postgres `pg_stat_activity` sampled, plus `max_connections`.
- CPU throttling from `cpu.stat` — directly validates G2.

**ESA telemetry — implemented.** Per your point 8, ESA now has the same runtime telemetry contract as DM:
- `internal/app/service/sequence_service/telemetry.go` (new)
- `SequenceLimiterStats()` accessor in `internal/app/init/common_modules.go`
- Telemetry config keys in `internal/app/constants/constant.go`
- An `asyncEsaLogInlineFallback` counter on the queue-full path

Emits every `telemetry.interval_seconds`: **`sequence_in_flight` / `sequence_limiter_cap` / `sequence_limiter_util_pct`** (how close the pod is to its ceiling — the per-pod capacity signal that was previously unobservable), `async_esa_queue_len`/`cap`/`inline_fallback_total` (G11 backpressure), `go_goroutines` (G9), `go_maxprocs` vs `go_numcpu` (**confirms or refutes G2 directly from the running pod**), heap/stack/sys MB, `go_num_gc`, `go_gc_cpu_fraction`.

**Disabled by default** (`telemetry.enabled=false`), so the change is inert until switched on — enable per environment with no redeploy:

```properties
telemetry.enabled=true
telemetry.interval_seconds=30
telemetry.memstats.enabled=true
```

Builds clean and existing tests pass. Also confirm the 503 `Warnw` calls (`sequence_controller.go:85`, `trigger_decision_controller.go:105`) are not suppressed at prod `log.level=1`.

---

## 5. Test ladder

Let **P** = projected festive peak arrival rate (decisions/sec), from business numbers + §3 baseline.

| ID | Test | Load | Duration | Environment | Answers |
|----|------|------|----------|-------------|---------|
| **T0** | Idle baseline | none | 30 min | UAT | Idle memory/CPU per pod. Denominator for `memory_per_request_mb`. |
| **T-PE** | **Prod-equivalent ceiling** | ramp to break | ~90 min | UAT, **prod config + resources + `replicas=7`** | **"What can production take right now?"** Answers the current-state capacity question without touching prod. See §5.5. |
| **T1** | **Single-pod calibration** | ramp 1→break | ~60 min | UAT, `replicas=1` | `λ_pod`, `C_cap` actual, `T_hold`, first-503 point, CPU vs memory saturation order. **Run for ESA alone, then DM→ESA.** |
| **T2** | Multi-pod linearity | 2 pods, then 4 | 30 min each | UAT | Does throughput scale linearly? Exposes shared-dependency saturation (Postgres, SF, Mongo). |
| **T3** | Steady soak | 1.0 × P | 4 h | UAT | Leaks, goroutine growth, connection growth, audit-queue drift, Mongo growth rate. |
| **T4** | Sustained peak | 1.25 × P | 60 min | UAT | Headroom at the target operating point. |
| **T5** | **Burst / bulk blast** | 3 × P for 3 min, from idle | 3 min ×3 | UAT | The actual festive risk. See §5.4. |
| **T6** | Break point | ramp to failure | until 503/504/OOM | UAT | Absolute ceiling and **failure mode** — graceful 503 or cascade? |
| **T7** | Provider degradation | 1.0 × P, provider latency ×3 | 30 min | UAT | `T_hold` inflates → slot exhaustion. Most likely real festive incident. |
| **T8** | Deploy under load | 1.0 × P during rolling restart | 20 min | UAT | In-flight loss during pod churn. Critical given §6-G10 (DM has no readiness gate). |
| **T9** | Autoscaler reaction | step 0.3 → 1.5 × P | 30 min | UAT | How fast does the HPA reach `N`? Measures scale-out lag against burst duration. Replaces the production test. |

**Production is explicitly out of scope.** All testing on staging (UAT), per your point 7. T9 replaces the prod-validation test with an autoscaler-reaction test on staging, which answers the more useful question anyway. The cost is that absolute numbers must be extrapolated from staging — see §5.1 for how, and §6-G12 for the caveats.

### 5.1 Local vs staging — where each is valid

You asked whether a local run could substitute. Partly, and it's worth using for the parts where it's valid.

**Local is genuinely useful for:**
- **CPU profiling.** `go tool pprof` on a local run will identify the hot paths in expression/template/JSON processing far faster than any cluster test. Given CPU is the likely per-pod ceiling, this is the highest-value local activity. ESA already has a `docker-compose.yml`.
- **Relative comparisons.** "Is the DM connection-pool fix actually faster?" — a local A/B answers this cleanly without cluster noise.
- **Shakeout.** Validating k6 scripts, payloads, auth headers, and the sync header before consuming a staging window.
- **Goroutine and memory behaviour under load** (G9) — `pprof` heap and goroutine profiles are more informative than `kubectl top`.

**Local cannot produce the numbers you need, because:**
- No cgroup CPU quota, so the `GOMAXPROCS`/CFS-throttling behaviour (G2) — plausibly the dominant per-pod effect — does not reproduce at all. You can simulate it with `docker run --cpus=1 --memory=1000m`, which gets much closer and is worth doing.
- Provider latency differs; `T_hold` is mostly provider wait time, so `λ_pod` won't transfer.
- Local Postgres/Mongo/Redis have different latency and pool behaviour, and the ESA Redis-disabled Postgres load (G7) won't be representative.
- A laptop's core count and memory bandwidth differ from the node type.

**Recommendation:** profile and shake out locally under `docker --cpus=1 --memory=1000m`; take all capacity numbers from staging. Before T1, align staging config to production (§6-G12) and note staging is exactly half of prod on CPU and memory (512Mi/500m vs 1000Mi/1000m) — so extrapolate per-pod capacity with care rather than a flat 2x, since CPU scaling is not linear once throttling is involved. The cleanest option, if the cluster allows it, is to temporarily raise one staging pod to prod-equivalent resources (1000Mi/1000m) for T1 and remove the extrapolation entirely.

### 5.2 Autoscaling — corrected

**An earlier draft of this plan claimed autoscaling was effectively pinned at `maxReplicas`. That was wrong, and your observation of DM scaling up and down is the correct account.** The correction matters because it changes what needs fixing.

What the manifests actually imply. HPA `averageUtilization` is computed against **`requests`**, not `limits`:

| Service | Memory request | Memory limit | Target | Scale-out above | CPU request | CPU limit | Scale-out above |
|---|---|---|---|---|---|---|---|
| ESA | 256Mi | 1000Mi | 70% | **~179Mi** | 200m | 1000m | **~140m** |
| DM | 512Mi | 1000Mi | 70% | **~358Mi** | 200m | 1000m | **~140m** |

My error was assuming pods sit near `GOMEMLIMIT` (900MiB). They don't — `GOMEMLIMIT` is a soft GC target, not an allocation floor, and the one real measurement on record (`CONFIG_DEFAULTS_AND_PROPOSED.md` §6: ESA staging idling at **27–38Mi**) shows actual usage is far below the request. At those levels utilization sits below the 70% target and the HPA scales on genuine load, which is what you observed.

**What remains true and still matters:**

- The scale-out **trigger** is ~179Mi (ESA) / ~358Mi (DM) — a small fraction of the 1000Mi limit. So pods scale out while individually still having large headroom. That is a *conservative* configuration, not a broken one. It trades node efficiency for latency safety, which for festive is arguably the right trade. **No change recommended before measurement.**
- The consequence for capacity planning: because scale-out happens early, **`maxReplicas` is reached sooner than per-pod resource exhaustion would suggest.** So the binding question is whether **`maxReplicas = 7`** is sufficient, not whether the HPA works. That is a §9-E node-capacity question.
- **The HPA in `deployment-prod.yml` may not be what is live.** You noted scaling may be configured at the AWS level. Commands in §10.1 confirm what is actually applied, including whether it is this HPA, a separately-managed HPA, KEDA, or node-level Cluster Autoscaler/Karpenter reacting to pending pods.

### 5.3 Why single-pod calibration is still the key test

Independent of the HPA. With an autoscaler active, an aggregate load test measures *the autoscaler's reaction*, not per-pod capacity — replica count changes underneath you mid-test and `λ_pod` cannot be recovered from the result.

To get `λ_pod`, the denominator of every sizing decision, you need one pod with a fixed replica count:

```bash
# Staging has no HPA (deployment.yml sets replicas: 2), so scaling is enough there:
kubectl -n staging scale deploy/decision-manager --replicas=1

# Only if an HPA exists in the target namespace (prod names: decision-manager-hpa,
# external-service-adapter-hpa). Detach for calibration, then restore min 2 / max 7.
kubectl -n <ns> patch hpa decision-manager-hpa -p '{"spec":{"minReplicas":1,"maxReplicas":1}}'
```

This is the single highest-value test in the plan. Once `λ_pod` is known, fleet sizing is arithmetic and the autoscaler's job is simply to reach `N` fast enough.

### 5.4 The bulk/burst scenario (T5) — the actual festive risk

Festive load is not a smooth ramp. It is a partner campaign blast: a large batch of leads pushed in a short window.

This interacts badly with the current design:

- DM's admission is **immediate-reject, no queue**. A 5,000-lead blast against 7 pods × 200 slots = 1,400 slots. If `T_hold` is 30s, steady capacity is ~47 decisions/sec; a 5,000-in-60s blast (83/sec) **503s roughly 45% of them**.
- The caller received `200 {"status":"processing"}` for the accepted ones and `503` for the rest. There is **no retry, no queue, no DLQ** (§6-G8).
- HPA reacts on a ~15s metrics window plus stabilization; a 3-minute blast is largely over before new pods are Ready.

T5 must therefore measure: 503 percentage, whether HPA reacted at all within the burst, and **how many decisions were actually completed** (from Mongo, not HTTP). Expect a decision on queueing/replay (§9-C) to come out of this.

### 5.5 T-PE — establishing the current production ceiling, on staging

A distinct and separately useful question from fleet sizing: **what can the production system, exactly as deployed today, actually absorb?** That number is what tells you whether festive projections are already covered, need modest scaling, or need a different architecture. It is also the only capacity figure you can state today with evidence.

Method: make staging config- and resource-identical to production, scale to production's `maxReplicas`, ramp to break, record the ceiling. No production traffic involved.

**Run T-PE before the hardening changes in §7**, and re-run after. That gives you two numbers — "what prod can take today" and "what prod can take after the fixes" — which quantifies the benefit of each fix and makes the case for deploying them.

#### 5.5.1 Replication checklist

Every one of these differs between staging and prod today. All must be aligned or the result is not a production ceiling. Derived from the config diff (§10.5) and both `deployment.yml` files.

> **The "Set to (prod)" columns come from the `configurations` repo and `deployment-prod.yml`. The repo is known to be out of sync for DM (§8A.6).** Before building the T-PE replica, replace these values with the live ones from Infra (**E1** ConfigMaps, **E3** deployment spec), and re-diff with §10.5. Treat the tables below as the list of keys to check, not as confirmed values.

**Staging capacity for T-PE.** Seven pods at 1000Mi / 1000m on each of DM and ESA need about **14Gi and 14 vCPU of limits** on staging nodes (requests are lower, but budget for limits under load). Check before scheduling: `kubectl describe nodes` (staging), Allocatable and Allocated sections. If staging can't fit it, run T-PE at the largest replica count that fits and scale linearly *only* after T2 has confirmed linear scaling.

**Deployment (staging `deployment.yml` → prod values)**

| Setting | Staging now | Set to (prod) |
|---|---|---|
| `limits.memory` | 512Mi | **1000Mi** |
| `limits.cpu` | 500m | **1000m** |
| `requests.memory` | 256Mi | 256Mi ESA / **512Mi** DM |
| `requests.cpu` | 250m | **200m** |
| `replicas` | 2 (fixed, no HPA) | **7** (prod `maxReplicas`) |
| `GOMEMLIMIT` | *(verify value)* | **900MiB** |
| `MEMORY_LIMIT_BYTES` (ESA) | 536870912 | **1048576000** |
| `terminationGracePeriodSeconds` | 60 DM / 90 ESA | **90 both** |

> Staging has **no HPA** — `deployment.yml` sets `replicas: 2` with no `HorizontalPodAutoscaler`. So staging replica count is manual, which is convenient for T-PE and T1 but means T9's autoscaler-reaction test needs an HPA created on staging first, mirroring prod's (min 2, max 7, CPU+memory 70%).

**ESA properties**

| Key | Staging now | Set to (prod) |
|---|---|---|
| `server.timeout` | 30 | **120** |
| `RestExecuteTimeoutInSeconds` | 45 | **60** |
| `log.level` | debug | **1** |
| `concurrency_base_reserve_mb` | 128 | **200** |
| `max_concurrent_sequence_requests_min` / `_max` | 10 / 150 | **20 / 400** |
| `database.maxOpenConnections` | 200 | **500** |
| `database.maxIdleConnections` | 21 | **50** |
| `audit.log.async.esa_queue_size` | 100 | **500** |
| `cache.enabled` | false | false *(already matches)* |

**DM properties**

| Key | Staging now | Set to (prod) |
|---|---|---|
| `server.timeout` | 30 | **60** |
| `log.level` | debug | **1** |
| `server.max_concurrent_trigger_decisions` | **15** | **200** |
| `max_parallel_dynamic_json_template_goroutines` | 8 | **unset** (code default 20) |
| `max_parallel_dynamic_json_goroutines` | 0 | unset (0) |
| `max_global_worker_goroutines` | 0 | unset (0) |
| `dynamic_json_expr_cache_max_entries` | 200 | **unset** (code default 500) |
| `database.maxOpenConnections` | 200 | **500** |
| `database.maxIdleConnections` | 20 | **50** |
| `server.shutdown.*` | set | **unset** in prod |

**Deliberate exceptions — keep these at staging values:**

- `telemetry.enabled=true` (prod has it off; T-PE needs the instrumentation, and the overhead is a periodic log line)
- Salesforce / provider endpoints stay on UAT, or stubs per §9-D
- Database and Mongo hosts stay on staging infrastructure — **which is the main caveat, see below**

#### 5.5.2 The load you must apply

Production `maxReplicas` is 7, so T-PE must drive **aggregate** load across 7 pods, not per-pod. Ramp until the first of:

- sustained 503s on DM or ESA (concurrency cap engaged),
- 504s on ESA (120s budget exceeded),
- p95 latency breaching the §9-I SLO,
- OOMKill or pod restart,
- **a data-tier limit** — Postgres connection rejection, Mongo write saturation (§8A).

Record which one fires first. That is the current production bottleneck, and it may well be the data tier rather than the services.

#### 5.5.3 The one thing T-PE cannot replicate

Staging points at **staging Postgres, Mongo, and Redis**, which are almost certainly smaller than production's. So T-PE gives you a truthful *application-tier* ceiling but an optimistic-or-pessimistic *data-tier* one, depending on relative sizing.

Two ways to handle it, in preference order:

1. **Get the sizing of both tiers from Infra (§8A) and state the caveat explicitly.** If staging Postgres is smaller than prod, a data-tier limit hit during T-PE is a staging artifact — but it still proves the services can push hard enough to saturate a database, which is itself the finding to take to Infra.
2. **Temporarily scale staging's data tier to prod-equivalent** for the T-PE window. Cleanest result, needs Infra involvement and budget.

Either way, report T-PE as two numbers: the application-tier ceiling, and which limit actually fired. Do not report a single "production can handle X" figure without stating which tier bound it.

### 5.6 Recommended volumes

- **Calibrate** (T1) until the first 503 or CPU saturation, whichever comes first. Do not go straight to high load.
- **Soak** (T3) at **1.0 × P**.
- **Operate** at ≤ **0.6 × λ_fleet_max** so a single pod loss doesn't cascade.
- **Break-point** (T6) to **3 × P** or first hard failure.
- **Size the fleet for 2 × P**, not 1 × P. Justification: business projections for a first festive season carry wide error bars, and the 503-means-lost-work property (§6-G8) makes under-provisioning expensive in a way that over-provisioning is not.

---

## 6. Bottleneck inventory (code-verified)

Ordered by expected impact at festive volume. G1–G4 are the pre-test hardening set.

**G1 — HPA scale-out triggers early; `maxReplicas=7` is the real ceiling.** *(corrected — autoscaling works)*
Thresholds are computed against `requests` (256Mi/512Mi, 200m) rather than `limits` (1000Mi, 1000m), so pods scale out at ~179Mi/~358Mi and ~140m. This is conservative rather than broken, and autoscaling is confirmed working. Consequence for festive: the fleet hits `maxReplicas` well before per-pod resources are exhausted, so **7 is the number to validate** (§9-E). Full detail and the retracted claim in §5.2. **No config change recommended pre-measurement.**

**G2 — `GOMAXPROCS` unset, no `automaxprocs`.**
Verified absent across both services, `go.mod`, Dockerfiles, and deployment env. Go sets `GOMAXPROCS` from **node** core count; the cgroup grants **1 core** (`limits.cpu: 1000m`). The runtime schedules N OS threads into 1 core of quota → CFS throttling, long GC pauses, p99 latency inflation. On a JSON/expression/template-heavy workload this is likely the largest single latency contributor. `GOMEMLIMIT` was set correctly; `GOMAXPROCS` was the other half of that change.

**G3 — DM→ESA has no connection pool.**
`decision-manager/internal/app/init/common_modules.go:944` sets `UseConnectionPool: false`, so `c.transport` stays nil and `RestExecuteWithTimeout` falls through to `http.DefaultTransport` (`api_client.go:258-261`) — `MaxIdleConnsPerHost = 2`. All DM→ESA traffic targets one host. At 200 concurrent decisions, ~198 connections are established and torn down per wave: TIME_WAIT accumulation, conntrack pressure, handshake latency on every call. ESA sets this correctly; DM does not.

**G4 — `MaxConnsPerHost=25` is deliberate provider protection, and it does not survive horizontal scaling.** *(reframed — this is a designed control, not a defect)*

Understood and agreed: a sequence issues multiple calls, sometimes several to the same provider, and 25 concurrent calls per provider is an intentional ceiling so provider systems aren't overwhelmed. `common_modules.go:1024`. Nothing to "fix" in the value itself.

The issue is **scope**. The transport is constructed once per pod, so `MaxConnsPerHost` is enforced **per pod, not per fleet**:

| Replicas | Concurrent calls to one provider |
|---|---|
| 1 | 25 |
| 2 (current min) | 50 |
| 7 (current max) | **175** |
| 12 (if raised for festive) | **300** |

So the protection holds at fixed replica count and silently weakens every time you scale out — which is exactly what festive traffic will cause. If a provider's real tolerance is ~25 concurrent, that is already being exceeded by 7x at current `maxReplicas`.

Two consequences for this exercise:

1. **Per-pod capacity is bounded by this, and that is fine and intended.** It gives a clean `λ_conn` term: `25 / p95_call_seconds` calls/sec per pod per provider. Query doc **Q12** computes this per provider from production data. This is a genuine input to "how much can one pod handle", not an obstacle.
2. **Fleet-level provider limits need a different mechanism.** Per-pod caps cannot express a fleet-wide budget. Options, in increasing order of effort: derive per-pod value from expected replica count (`ceil(provider_budget / maxReplicas)`, simple, wastes capacity at low replica counts); a Redis-backed distributed token bucket (Redis is already a dependency on DM, though disabled on ESA); or a shared egress proxy that enforces per-provider concurrency centrally.

**This needs a decision before `maxReplicas` is raised** (§9-B). It is the one place where scaling out to serve festive traffic could damage a provider relationship rather than just cost money.

**G5 — Prod concurrency caps are far above staging's tuned values, but may never be reached.** *(reframed per your point 3)*

The facts: staging `max_concurrent_trigger_decisions` was moved **80 → 15** (commit `a0c8963`) at 512Mi. Prod remains **200** at 1000Mi. Prod also leaves `max_parallel_dynamic_json_template_goroutines` at default **20** vs staging's explicit **8**, with no global goroutine cap. Nominal worst case is 200 in-flight × 20 template goroutines on 1 core.

Your point stands: **a cap that is never reached costs nothing.** If real concurrency peaks at, say, 12 per pod, then 200 vs 30 is immaterial and not worth spending time on. The caps only matter in two situations, and both are testable:

- **As a safety net** — if something upstream bursts, does the cap engage before the pod OOMs or becomes unresponsive? T6 answers this. A cap set above the pod's actual survivable concurrency provides no protection, which is the only real risk here.
- **As the binding constraint** — if `λ_pod` turns out to be cap-limited rather than CPU-limited. Query doc **Q8** (DM) and **Q14** (ESA) settle this from history: it reconstructs observed peak concurrent decisions from `triggerDecisionStartTime`/`EndTime`. **Run that before spending any effort on cap tuning.**

Expected finding, to be confirmed: observed concurrency is well below 200, CPU saturates first, and the caps are irrelevant to throughput while also being ineffective as a safety net. The action in that case is to set the cap *near the measured survivable ceiling* from T6 — not to lower it for its own sake.

**G6 — Inverted timeout ladder.**
DM `server.timeout=60` < DM→ESA `RestExecuteTimeoutInSeconds=120` = ESA `server.timeout=120` = ESA `process_sequence_max_seconds=120`. The outer budget is shorter than the inner. In sync mode DM can cut the response while ESA keeps working. In async mode the background goroutine is not bound by `server.timeout` at all, so a slot can be held ~120s+. Ladder should be strictly decreasing outward-in.

**G7 — Redis disabled on ESA; Postgres connection math doesn't close.**
`cache.enabled=false` in prod and staging (FRDP-104). Service configuration and query objects — read-mostly reference data, the ideal cache target — are read from Postgres on **every** request. Meanwhile `maxOpenConnections=500` **per pod** × 7 pods = **3,500** potential connections, before DM's own 500×7. Typical Postgres `max_connections` is 100–500. **This needs checking against the actual server setting and probably a PgBouncer discussion before festive.** Note DM has Redis enabled — only ESA is uncached.

**G8 — 503 means lost work, not deferred work.**
DM does not retry ESA on 503/5xx (connection-errors only, `api_client.go:132-166`). DM returns `200 {"status":"processing"}` before any work, so downstream failure is invisible to the caller. No queue, DLQ, or replay path exists. At peak, backpressure silently drops decisions. This is the most consequential *design* gap for festive, distinct from the config issues.

**G9 — ESA goroutine count scales with sequence size, not with the semaphore.**
`sequence_service.go:613-623` — `StartTrackedGoroutine` is called per service, and the goroutine *then* acquires `groupSem`. A 100-service group spawns 100 goroutines immediately; 15 run. Memory therefore tracks total services per sequence, which the flat `3 MB/request` model does not capture. Feed the §3 services-per-sequence distribution into the memory model.

**G10 — DM has no readiness gate.**
DM registers only `/health` (`router.go:106`), and `HealthController.Status` returns the version file — it is **not** shutdown-aware. The readinessProbe points at `decision-manager/health`, so DM pods never go NotReady during shutdown and keep receiving traffic through rolling deploys and scale-down. ESA has a correct shutdown-aware `/ready` (`health_controller.go:29-35`). This makes T8 important, and it will bite during any festive scaling event.

**G11 — Audit write path converts to request latency under load.**
2 Mongo writer goroutines per pod (ESA queue 500, DM queue 1000). On queue-full the write happens **inline on the request path** with a 500ms (ESA) / 200ms (DM) timeout. Currently unmeasured — will present as unexplained latency. Instrument queue depth (§4.4).

**G12 — Staging cannot be extrapolated cleanly.**
Staging is exactly half of prod on memory and CPU (512Mi/500m vs 1000Mi/1000m) — convenient. But staging runs `log.level=debug` vs prod `log.level=1`, ESA staging `RestExecuteTimeoutInSeconds=45` vs prod `60`, DM staging `server.timeout=30` vs prod `60`, and staging has the goroutine caps and telemetry that prod lacks. **Align UAT config to prod (except telemetry) before T1**, or results won't transfer.

---

## 7. Pre-test hardening (recommended before load testing)

These are high-confidence fixes for defects, not tuning choices. Testing the current config measures something you intend to change.

**I am not applying any of these** — they touch production. Listed for approval.

| # | Change | Where | Risk | Status |
|---|---|---|---|---|
| 1 | Set `GOMAXPROCS` to match `limits.cpu`, or adopt `go.uber.org/automaxprocs` | both `deployment-prod.yml`, `deployment.yml` | Low. `automaxprocs` is the durable fix as limits change. | **Recommended — do before T1** |
| 2 | `UseConnectionPool: true` on DM's API client, `MaxIdleConnsPerHost` ≈ expected per-pod concurrency | `decision-manager/.../common_modules.go:940-945` | Low. Code change, needs a release. | **Recommended — do before T1** |
| 3 | Fix the timeout ladder to strictly decrease outward-in (see G6) | DM + ESA properties | Low. Also removes the sync-mode measurement artifact (§4.2). | **Recommended — do before T1** |
| 4 | Align staging config to prod (G12) so results extrapolate | staging properties | Low. | **Required before T1** |
| 5 | ESA runtime telemetry | `sequence_service/telemetry.go` + 2 files | None when disabled (default off) | **Implemented** — §4.4 |
| 6 | Enable `telemetry.enabled=true` on DM and ESA for the test window | **staging** properties | Low. Log volume only. Production stays off unless its owner decides otherwise. | Recommended |
| 7 | Add a shutdown-aware `/ready` to DM; repoint readinessProbe | DM router + deployment | Low. Code change. Prevents in-flight loss during scale events. | Recommended |
| 8 | Raise `maxReplicas` above 7 | `deployment-prod.yml` | Low **only after** §9-B provider decision and §9-E node check | **Blocked** |
| 9 | Fleet-aware per-provider concurrency budget (G4) | design decision | Medium–High | **Blocked on §9-B** |
| 10 | Re-evaluate ESA Redis (`cache.enabled=false`) | ESA prod properties | Needs FRDP-104 context | **Blocked on §9-F** |
| 11 | HPA `requests`/target retuning | `deployment-prod.yml` | Medium | **Not recommended pre-measurement** — autoscaling works (§5.2) |
| 12 | Concurrency cap retuning (DM 200 / ESA 266) | properties | Low | **Defer** — measure first (§6-G5); set from T6's survivable ceiling |

---

## 8. From results to scaling decisions

### 8.1 Horizontal vs vertical

| Observation at break point | Read | Action |
|---|---|---|
| CPU at limit, memory well under, latency climbing | CPU-bound (expected for this workload) | **Vertical first:** `limits.cpu` 1000m → 2000m, with `GOMAXPROCS` matched. Then horizontal. |
| OOMKill or memory at limit, CPU moderate | Memory-bound | **Vertical:** raise memory limit + `MEMORY_LIMIT_BYTES` + `GOMEMLIMIT` together, or lower concurrency cap. |
| 503s while CPU and memory both low | Cap-bound (G5) or connection-bound (G4) | Raise the binding cap — not resources. Re-measure. |
| Per-pod resources fine, throughput plateaus as pods are added | Shared dependency saturated (Postgres/SF/Mongo) | Neither. Fix the dependency (G7, PgBouncer, caching, provider limits). |
| Latency fine, need fault tolerance / deploy headroom | — | **Horizontal.** |

**Expectation to test, not assume:** these are CPU-heavy Go services on 1 core with an unset `GOMAXPROCS`. The likely outcome of T1 is CPU saturation well before the 266/200 concurrency caps are approached — which would mean **vertical CPU first, then horizontal**, and that the memory-based concurrency model is tuning a non-binding constraint. T1 will confirm or refute this.

### 8.2 Deriving the fleet

```
1. T_hold          ← §3 Mongo (p95, not mean — p95 drives slot occupancy)
2. λ_pod           ← T1, at the point before latency degrades
3. λ_fleet_needed  = 2 × P            (§5.6 rationale)
4. N               = ceil(λ_fleet_needed / λ_pod)
5. minReplicas     = ceil(0.4 × N)    so a burst needs only a modest scale-out
6. maxReplicas     = ceil(1.5 × N)    burst headroom
7. C_cap per pod   = λ_pod × T_hold_p95 × 1.2
8. Verify:  N × per-pod Postgres/Mongo/SF connections < server limits   ← G7
```

Step 8 is the one that is currently violated and must be checked before raising `N`.

### 8.3 Alerting (post-test)

Derive thresholds from measured values, not guesses:
- **503 rate > 0 sustained 1 min** — page. With no queue, 503 = lost decisions.
- **504 rate > 0** — ESA exceeding its 120s budget.
- **Memory > 80% of limit** (absolute, not request-relative).
- **CPU throttling ratio > 5%** — directly catches G2 recurrence.
- **Async audit queue depth > 50% / any inline fallback** — catches G11.
- **Postgres connections > 70% of `max_connections`** — catches G7.
- **Completion-vs-acceptance gap** (Mongo completions vs HTTP 200s) — the only way to see async failures (G8).

---

## 8A. Data-tier scaling — what to check, set, and hand to Infra

**The failure mode this section exists to prevent:** the microservices are sized correctly, the HPA scales them, and the platform still fails at festive peak because Postgres, MongoDB, or Redis were not scaled in step. Application scaling *multiplies* data-tier demand — every new pod brings its own connection pool, its own write rate, and its own share of query load. A service tier that scales 2 → 7 pods is a data tier facing 3.5x the connections and writes, with no autoscaling of its own.

This is the most likely way a well-executed festive scaling exercise still results in an incident, because the two tiers are owned by different teams and the coupling is implicit.

### 8A.1 Configured demand today — per pod, and at `maxReplicas`

All values code- or config-verified. These are **configured ceilings**, not observed usage; §8A.3 measures the actual.

| Dependency | Service | Per pod | × 7 pods (prod `maxReplicas`) | Source |
|---|---|---|---|---|
| **Postgres** max open | ESA | 500 | **3,500** | `..._production.properties` |
| **Postgres** max open | DM | 500 | **3,500** | `..._production.properties` |
| **Postgres** idle retained | ESA / DM | 50 each | **350 each** | `maxIdleConnections` |
| **Postgres** conn lifetime | both | 24h | — | `connMaxLifetimeInHours` |
| **Mongo** max pool | ESA | **10** | **70** | code default, `mongo.go:48` — not set in config |
| **Mongo** max pool | DM | **unlimited** | **unbounded** | `mongo.maxPoolSize` unset → `GetConfigInt` returns 0 → driver treats 0 as no limit |
| **Mongo** writers | ESA | 2 | 14 | `audit.log.async.esa_workers` |
| **Mongo** writers | DM | 2 | 14 | `DefaultAsyncDMLogWorkers` |
| **Redis** pool | DM | 50 (20 min idle) | **350** (140 idle) | `cache.pool_size` |
| **Redis** pool | ESA | 50 configured, **cache disabled** | 0 | `cache.enabled=false` |

**Three things stand out:**

1. **Postgres is over-subscribed by configuration.** Managed Postgres typically allows 100–500 total connections depending on instance class. Two databases each configured for up to 3,500 cannot both be honoured. Either the pools never fill (likely today, and §8A.3 will confirm) or connection acquisition begins failing under load — and it will fail *first* at exactly the moment festive scaling adds pods. **This is the single most important item to verify.**
2. **DM's Mongo pool is unbounded.** `mongo.minPoolSize` / `maxPoolSize` / `maxConnIdleTimeInMs` are absent from both DM properties files, and `GetConfigInt` returns 0 when a key is missing (`common_modules.go:1334-1340`), which the Go driver interprets as no limit. Actual concurrency is low today because only 2 async workers write, so this is latent rather than active — but it is an unbounded resource against a shared database. ESA sets explicit defaults (5 min / 10 max); DM should too.
3. **Asymmetric Mongo pools** (ESA 10, DM unlimited) are almost certainly unintentional rather than tuned.

### 8A.2 Demand model — so the ask updates when N changes

Give Infra a formula, not a snapshot. When `N` or `P` changes, the requirement recomputes without another analysis round.

```
λ          = decisions/sec at festive peak          (from §9-H projections)
S          = services per sequence                  (measure: query doc Q11)
N          = replica count at peak                  (from §8.2)

POSTGRES
  connections_possible   = N × 500        per service, per database
  connections_realistic  = N × observed_p95_per_pod      ← measure, don't assume
  required max_connections ≥ connections_realistic × 1.5 + admin headroom
  query rate (ESA)       ≈ λ × S × configs_per_service   ← inflated by cache.enabled=false (G7)

MONGODB
  writes/sec  = λ × (1 + S)
                 └── DM writes 1 doc per decision
                 └── ESA writes 1 doc PER SERVICE (InsertMany, esa_log_repository.go:88-115)
  storage/day = λ × 86400 × (1 + S) × avg_doc_bytes
  connections = (N_dm × unbounded) + (N_esa × 10)

REDIS  (DM only; ESA disabled)
  connections = N_dm × 50
  ops/sec     ≈ λ × cache_lookups_per_decision
```

The `λ × (1 + S)` term is the one Infra will care about most and the one most likely to be missed. **At S = 10 services per sequence, 20 decisions/sec becomes 220 Mongo document writes/sec**, and those documents embed full request and response bodies (`DecisionManagerLog` carries `esaRequestBody`, `esaResponseBody`, `sfCompositeSubRequestArray`, `sfCompositeResponse`; `EsaLog` carries `request.body` and `response.body`). Document size is therefore large and variable — measure it (query doc Q15–Q17) rather than guessing, because storage growth and write IOPS scale off it directly.

### 8A.3 What to check — before, during, after

**Before (Phase 0, read-only).** For **production**, these SQL statements and the Mongo `serverStatus()` below are run by **Infra** (E7, E8). The Mongo size and write-rate figures you get yourself from the read replica (query doc Q15–Q18). For **staging**, run them yourself; they establish the staging data-tier baseline that T-PE is measured against.

```sql
-- Per database. The headline number.
SHOW max_connections;
SHOW superuser_reserved_connections;
SELECT count(*) AS total,
       count(*) FILTER (WHERE state='active') AS active,
       count(*) FILTER (WHERE state='idle') AS idle,
       count(*) FILTER (WHERE state='idle in transaction') AS idle_in_txn
FROM pg_stat_activity;

-- Per pod, so you can derive observed_p95_per_pod for the model above
SELECT client_addr, count(*) FROM pg_stat_activity GROUP BY 1 ORDER BY 2 DESC;

-- Is anything already being refused or waiting?
SELECT datname, numbackends, xact_commit, xact_rollback,
       blks_read, blks_hit, deadlocks, conflicts, temp_files
FROM pg_stat_database WHERE datname NOT LIKE 'template%';
```

```javascript
// Mongo: current connection and write picture
db.serverStatus().connections          // current, available, totalCreated
db.serverStatus().opcounters           // insert rate baseline
db.serverStatus().wiredTiger.concurrentTransactions   // write ticket saturation
db.stats()                             // storage vs allocated
```

**During T-PE and T5 (staging data tier only).** Sample alongside pod metrics, because a data-tier limit firing first is a legitimate and important T-PE outcome:

```bash
# Postgres connections every 10s for the test window
while true; do
  printf '%s ' "$(date -Iseconds)"
  psql "$PGURI" -tAc "SELECT count(*)||' '||count(*) FILTER (WHERE state='active') FROM pg_stat_activity"
  sleep 10
done | tee pg_conns_$(date +%H%M).log
```

```javascript
// Mongo write saturation during load. Falling "available" write tickets = saturated.
while (true) {
  const s = db.serverStatus();
  print([new Date().toISOString(),
         s.connections.current, s.connections.available,
         s.opcounters.insert,
         s.wiredTiger.concurrentTransactions.write.available].join('\t'));
  sleep(10000);
}
```

**After** — recompute the §8A.2 model with measured `S`, `avg_doc_bytes`, and `observed_p95_per_pod`, and reissue the §8A.4 handover with real numbers.

### 8A.4 Handover to Infra

One table, one conversation. Fill the right column from Phase 0 and T-PE.

| # | Ask | Why it matters | Blocking? |
|---|---|---|---|
| 1 | **Postgres `max_connections`** and instance class (vCPU/RAM/IOPS) for the DM and ESA databases | Pods are configured for up to 500 connections each; `N × 500` must fit, or scaling out breaks the DB before it helps | **Yes** — bounds `N` |
| 2 | **Is PgBouncer (or RDS Proxy) in the path?** If not, should it be? | Transaction pooling decouples pod count from server connections and is the standard fix for exactly this shape | **Yes** |
| 3 | **MongoDB deployment**: Atlas tier or self-managed, connection limit, IOPS, write concern, replica set topology | Write rate is `λ × (1 + S)` — the multiplier is easy to miss | **Yes** |
| 4 | **Mongo storage growth headroom** for the festive window | `λ × 86400 × (1+S) × avg_doc_bytes`; documents embed full payloads so they are large | Yes |
| 5 | **Indexes on `decision-manager-log` and `esa-log`** — present, and covering the Phase 0 aggregations | Write-heavy collections that are about to be read heavily. Unindexed aggregations on prod-sized collections will be slow and may affect writes | Yes |
| 6 | **Redis** instance size, `maxclients`, memory, eviction policy, and whether it is shared with other services | DM holds `N × 50` connections; eviction policy affects correctness if config caching is re-enabled (§9-F) | Yes |
| 7 | **Can staging's data tier be temporarily scaled to prod-equivalent** for the T-PE window? | Removes the main caveat on the production-ceiling number (§5.5.3) | Preferred |
| 8 | **Do DM and ESA share a Postgres server** (separate databases, one instance)? Share Mongo? Share Redis? | If shared, the connection and IOPS budgets add rather than being independent, and one service can starve the other | **Yes** |
| 9 | **Scaling story for each data store**: can they be scaled ahead of festive, how long does it take, is it online or does it need downtime? | Pod scaling is seconds; a Postgres instance resize is minutes-to-hours and may need a maintenance window. This must be sequenced *before* festive, not during | **Yes** |
| 10 | **Read replicas** available or feasible for the read-mostly config/query-object lookups? | Directly relevant given ESA's Redis is disabled (G7) and every request hits Postgres | Useful |
| 11 | **Monitoring access** for the data tier — connection count, IOPS, replication lag, slow queries | Data-tier alerts (§8A.5) need somewhere to live | Yes |

**The framing for that conversation:** the services will be sized to handle `N × λ_pod` decisions/sec. That translates mechanically into `N × 500` possible Postgres connections and `λ × (1+S)` Mongo writes/sec. We need confirmation that both tiers can absorb it, and if not, either the data tier scales or `N` is capped — and a capped `N` is a capacity commitment that has to go back to business.

### 8A.5 Data-tier alerts

The service-tier alerts in §8.3 will not catch these, and they fail in ways that look like application problems.

| Alert | Threshold | Why |
|---|---|---|
| Postgres connections | > 70% of `max_connections` | The scaling ceiling; fires before acquisition errors |
| Postgres `idle in transaction` | > 10 sustained | Leaked transactions hold connections and compound under load |
| Postgres deadlocks / rollbacks | rising rate | Contention that only appears at concurrency |
| Mongo `connections.available` | < 20% of total | Pool exhaustion; DM's unbounded pool makes this reachable |
| Mongo WiredTiger write tickets available | < 20% | The real Mongo write-saturation signal, not CPU |
| Mongo replication lag | > 10s | Audit writes falling behind; corrupts the Phase 0 / T-PE data you are measuring with |
| Mongo storage used | > 75% | Festive growth is `λ × (1+S)` and accelerates |
| Redis `maxclients` usage | > 70% | `N × 50` from DM alone |
| Redis evicted_keys | > 0 | If config caching is re-enabled, eviction means stale-or-miss behaviour |
| Async audit queue depth / inline fallback | > 50% / any | Application-side symptom of a saturated data tier (G11); already exposed via telemetry (§4.4) |

The last row is the useful bridge: the ESA/DM telemetry added in §4.4 surfaces data-tier backpressure *from inside the application*, which is often the earliest visible signal and needs no data-tier access to observe.

### 8A.6 Config discrepancy — resolved

**Status: resolved, with one consequence.** The live DM ConfigMap uses `mongo.connectionString` (correct), and Mongo inserts are confirmed working in production. The `configurations` repo is out of sync with what is deployed for DM.

**Consequence:** the `configurations` repo is **not the source of truth for DM production**. Every DM production value this plan quotes was read from the repo, including `max_concurrent_trigger_decisions=200`, `server.timeout=60`, `maxOpenConnections=500`, the missing Mongo pool keys, unset goroutine caps, and the T-PE checklist in §5.5.1. **All of these need confirming against the live ConfigMap (Infra enquiry E1)** before they're used for sizing or for building the T-PE replica. ESA's repo file matched on the key that was checked, but ask for both ConfigMaps in E1 rather than assume.

Two follow-ups worth raising with whoever owns the `configurations` repo: bring the DM production file in line with the live ConfigMap, and find out how the drift happened. If ConfigMaps are edited directly in the cluster, the repo will drift again, and a festive config change made that way could be lost on the next deploy.

The original finding, kept for reference:

**DM's production Mongo keys in the repo do not match what the code reads.**

| | Production properties | Staging properties | Code reads (`db/mongo.go:25-26`) |
|---|---|---|---|
| Connection string | `mongo.connection_string` | `mongo.connectionString` | `mongo.connectionString` |
| Database name | `mongo.database_name` | `mongo.dbName` | `mongo.dbName` |

Staging matches; production uses snake_case where the code expects camelCase. Viper lowercases keys but does not translate between the two conventions, and there is no `SetEnvKeyReplacer` or `RegisterAlias` in `viper_config.go`. ESA's production file uses the camelCase form correctly — only DM differs.

Since DM production clearly does write Mongo audit logs, one of these must be true, and it matters which:

1. **The live ConfigMap differs from this repo.** Then `configurations/` is not the source of truth for production, and **every config-derived finding in this plan needs re-verification against the live ConfigMap** (§10.5 has the command). This would be the more consequential explanation.
2. **The connection string reaches the app another way** — an env var or the secret resolver populating the expected key.
3. **DM production Mongo logging is degraded** and some audit data is missing, which would undermine the Phase 0 baseline.

**Outcome:** explanation 1 applied. The live ConfigMap differs from the repo; logging is not degraded.

---

## 9. Open questions and decisions needed

Resolved from your review: ESA telemetry (**now implemented**, §4.4), the sync header (**keep and use**, §4.2), k6 (**Grafana k6, `brew install k6`**, §4.3), production testing (**out of scope**, §5), autoscaling (**works**, §5.2), and the 25-connection limit (**intentional**, §6-G4).

Commands and queries for everything below are in §10.

### Blocking

**A. Existing observability stack.** ESA telemetry is now in place, so the gap is narrower — but: **is there a Prometheus/Grafana, CloudWatch Container Insights, Datadog, or other APM already on this cluster?** If yes, that is where CPU throttling, replica history, and HPA decisions should come from, and some of §10's manual sampling becomes unnecessary. If no, we proceed with telemetry logs + `kubectl top`, and a `/metrics` endpoint becomes worth considering as follow-up rather than for this exercise.

**B. Per-provider concurrency budgets — now the top blocker.** Given `MaxConnsPerHost=25` is deliberate and enforced **per pod** (G4), scaling to 7 pods already permits 175 concurrent calls per provider, and festive scaling pushes that higher. Needed **per provider**: sustained concurrent-call tolerance, burst tolerance, any daily/hourly quota (Salesforce API limits especially), and whether festive headroom has been negotiated. Then a decision on the fleet-level mechanism in G4. **`maxReplicas` cannot responsibly be raised until this is settled.**

**C. Overload policy.** At capacity, behaviour is immediate 503 with no retry or queue, and the caller already received `200 "processing"` (G8). For festive:
   - (i) Keep reject-fast; caller retries on 503. **Does the calling system honour `Retry-After`, and does it retry at all?**
   - (ii) Bounded in-memory queue — smooths bursts, adds latency, risks stale decisions.
   - (iii) Durable queue + DLQ + replay — robust, largest change.
   **What is the acceptable loss rate at festive peak?** This sets T5's pass criteria and the §5.6 sizing margin.

**D. Provider stubbing.** To measure DMI capacity rather than the Salesforce UAT sandbox's, capacity tests need a stub mode with controllable latency (also required for T7). Does one exist, or should it be added? Without it, the sandbox will likely be the bottleneck and `λ_pod` won't reflect production. This is the main threat to T1's validity.

### Needed for sizing

**E. Node pool capacity.** Both deployments pin to `nodeSelector: node-type=preonboarding-node` with required affinity and a `workload=preonboarding` toleration. Raising `maxReplicas` past 7, or CPU limits to 2000m, needs headroom on that specific pool. **Node count, instance type, whether Cluster Autoscaler/Karpenter manages it, and the pool maximum.** There may be a ceiling above the pod-level one. Also: is the autoscaling you observed HPA (pods) or node-level, or both? §10.1 will show this.

**F. FRDP-104 — why was ESA Redis disabled?** Commit `d4795ed`. If correctness (stale config, invalidation), re-enabling needs the underlying fix first. If operational (Redis instability, TLS), more tractable. Potentially the largest single `T_hold` reduction available (G7), and `T_hold` is the dominant throughput term — worth resolving before sizing.

**G. Data-tier scaling — now has its own section.** `max_connections` for both databases, Mongo sizing and write headroom, Redis limits, and whether the tiers are shared. **This is an Infra conversation with an 11-item handover table in §8A.4**, a demand model in §8A.2 that recomputes when `N` changes, and alerts in §8A.5. Items 1, 2, 3, 8 and 9 there are blocking — in particular **item 9 (how long does scaling each data store take, and does it need downtime)**, because pod scaling is seconds while a database resize may need a maintenance window that has to be sequenced before festive rather than during it.

**G2. Live production ConfigMaps — resolved in part.** The DM ConfigMap is correct and logging works, but the `configurations` repo is out of sync for DM (§8A.6). **Still needed:** the full live DM and ESA ConfigMaps from Infra (E1), so the DM production values in this plan and the T-PE replica are built from what is actually deployed.

**H. Business numbers — shape, not just total.** The daily total is the least useful figure. Needed: **peak hour** as a fraction of day; **peak minute** within it; whether partners push **campaign blasts** (batch size and window — sizes T5); expected **payload/sequence size** changes for festive offers (drives G9); the **festive window** and any known spike dates. Note query doc **Q4–Q6** derive the current shape from history, so projections can be expressed as a multiplier on observed peak rather than absolute numbers — often an easier question for business to answer.

**I. Latency and success SLOs.** The v1 plan targeted p95/p99 overhead ≤ 1–1.5s over provider time. Still the commitment? What end-to-end p95 does the caller expect, and what decision success rate is contractual? T4/T5 need these as pass/fail.

**J. Scope.** Anything else in the festive path — API gateway/ingress rate limits, the calling system into DM, Salesforce-side automation triggered by DM writes? An ingress rate limit would bound everything upstream of these services.

**K. Staging fidelity.** Can one staging pod be temporarily raised to prod-equivalent resources (1000Mi/1000m) for T1? That removes the staging→prod extrapolation entirely and materially improves confidence in `λ_pod`. Also: does staging point at the same Salesforce UAT sandbox that other testing uses — i.e. will a load test disrupt anyone else?

---

## 9A. Proposed sequencing

| Phase | Work | Blockers |
|---|---|---|
| **0a** | ~~Verify the live ConfigMap~~ **Done.** DM ConfigMap correct, logging works; the repo is out of sync (§8A.6). | — |
| **0b** | **You:** Mongo baseline on the production read replica (query doc Q0–Q18). Staging checks (§10.2–10.5). | **None — start now** |
| **0c** | **Send Infra the read-only enquiries (§10.1, E1–E11)** together with the §8A.4 handover. E1 (live ConfigMaps) and E2 (replica history) are needed first. Long lead time, so send immediately; don't wait for 0b. | None |
| **1** | Resolve §9-A (observability), §9-D (stubbing), §9-E (node pool), §9-G (data tier). Align staging config to the **live** production ConfigMap from E1 (§5.5.1, §10.5). | A, D, E, G, **E1** |
| **2** | **T-PE on staging at prod-equivalent config** → *"what production can take today"*. Report with the §5.5.3 data-tier caveat. | Phase 1 |
| **3** | Hardening items 1–4, 6–7 (§7) on staging. Enable telemetry. **Re-run T-PE** → quantifies the gain from each fix. | Approval |
| **4** | **T0, T1, T2** single pod. Produce `λ_pod`, `C_cap`, saturation order. Local `pprof` in parallel (§10.6). | Phase 3 |
| **5** | Business projections arrive → compute **P** → derive **N** and caps (§8.2). Validate `N` against provider budgets (§9-B) **and the §8A.2 data-tier model**. | §9-H, §9-B |
| **6** | **T3–T9** at P-derived volumes, sampling the data tier throughout (§8A.3). | Phases 4–5 |
| **7** | Production service config + resources + `maxReplicas`, **and the agreed data-tier scaling, sequenced first**. Alerts per §8.3 and §8A.5. Freeze ahead of festive. | §9-B, C, F, §8A.4 |

**Phase 0 needs no decisions from anyone and can start immediately.** It will likely answer several open questions on its own: whether the concurrency caps were ever approached (§6-G5), which provider dominates `T_hold`, the real peak-to-average shape, and the current data-tier headroom. It also re-checks autoscaling against the cluster rather than the manifests.

Two sequencing points worth noting:

- **Phase 2 (T-PE) deliberately precedes the hardening in Phase 3.** Running it on the unmodified configuration is what makes it a statement about production as it stands today. Re-running after hardening then gives a measured before/after, which is the most persuasive form of the case for deploying those fixes.
- **Phase 0c starts early on purpose.** Data-tier scaling has a far longer lead time than pod scaling — an instance resize may need a maintenance window (§8A.4 item 9). If the data tier turns out to be the binding constraint, that needs to be known weeks before festive, not discovered in Phase 6.

Phase 4 does not depend on business numbers — `λ_pod` is a property of the system, so calibration can finish before projections arrive; only Phase 5 waits on them.

---

## 10. Commands — everything needed to close the open items

Grouped by who runs them. **§10.1 is the only production-facing part, and Infra runs it read-only.** Everything from §10.2 onward runs on **staging** (`NS=staging`). MongoDB queries are in **`Baseline_Extraction_Queries.md`**.

### 10.1 Production — read-only enquiries for Infra (superseded)

> **Superseded by Part A.** E1–E4 are covered by your confirmations (§A.0) and Grafana (§A.1–A.2); production node counts are out of scope. The data-tier and EKS items are handed over as requirements in **§A.7**. The table is kept only for reference if Infra wants specifics later.

| # | Ask | Read-only command / source | Why |
|---|---|---|---|
| **E1** | **Live ConfigMaps** for DM and ESA (secret values masked) | `kubectl get configmap -n preonboarding decision-manager-config -o yaml`<br>`kubectl get configmap -n preonboarding external-service-adapter-config -o yaml` | The repo is out of sync for DM (§8A.6). Every production value in this plan, and the T-PE replica, must come from these. |
| **E2** | **Replica count history**, 60 days, hourly min/max per deployment | CloudWatch Container Insights / Prometheus `kube_deployment_status_replicas`; `kubectl describe hpa -n preonboarding decision-manager-hpa external-service-adapter-hpa` | Converts Q8/Q14 fleet concurrency into **per-pod** concurrency; shows how often `maxReplicas=7` is reached and how fast scale-out happens. |
| **E3** | **Deployed spec** of both deployments | `kubectl get deploy -n preonboarding decision-manager external-service-adapter -o yaml` | Confirms the live resources, `GOMEMLIMIT`, `MEMORY_LIMIT_BYTES`, probes, absence of `GOMAXPROCS`, and image tag (so staging runs the same build). |
| **E4** | **HPA and autoscaling setup**: live HPA spec, KEDA, Cluster Autoscaler / Karpenter | `kubectl get hpa -n preonboarding -o yaml`; `kubectl get scaledobject -A`; `kubectl -n kube-system get deploy cluster-autoscaler`; `kubectl get nodepool -A` | Confirms where the scaling you observed is configured (§5.2). |
| **E5** | **Node pool** for `node-type=preonboarding-node`: instance type, current/min/max nodes, allocatable CPU and memory, what else runs on it | `kubectl get nodes -l node-type=preonboarding-node -o wide`; `kubectl describe nodes -l node-type=preonboarding-node` (Allocatable / Allocated sections); `aws eks describe-nodegroup` | Whether `maxReplicas` can rise above 7 without new nodes (§9-E). |
| **E6** | **Per-pod CPU and memory**, 60 days, p95/max, plus **CPU throttling** if available | Container Insights `pod_cpu_utilization` / `pod_memory_utilization`; Prometheus `container_cpu_cfs_throttled_periods_total` | Real production per-pod usage at observed load; validates the GOMAXPROCS finding (G2) on production rather than only staging. |
| **E7** | **Postgres**, DM and ESA databases: engine version, instance class, `max_connections`, peak `DatabaseConnections` over 60 days, CPU, IOPS, RDS Proxy / PgBouncer, shared instance? | RDS console / CloudWatch; read-only SQL from query doc §8 (`SHOW max_connections`, `pg_stat_activity`, `pg_stat_statements`) | §8A.1: pods are configured for up to `7 × 500` connections per database. |
| **E8** | **MongoDB**: version, tier, connection limit, peak connections, opcounters, write tickets, storage used/limit, **indexes and TTL** on both audit collections, oplog window | Atlas metrics or `db.serverStatus()` (needs `clusterMonitor`); `getIndexes()` | Write rate is `λ × (1 + S)` (§8A.2); indexes decide how heavy the Phase 0 queries are. |
| **E9** | **Redis**: node type, `maxclients`, peak `CurrConnections`, memory, evictions, shared with other services? | ElastiCache console / CloudWatch | DM holds `N × 50` connections (§8A.1). |
| **E10** | **Ingress / ALB / API gateway**: any rate limits, and the 60-day peak `RequestCount` per minute for DM's route | ALB CloudWatch metrics; ingress annotations (`kubectl get ingress -n preonboarding -o yaml`) | An external rate limit would cap everything upstream. Peak request rate is a second, independent measure of `λ_peak` to compare with Q4. |
| **E11** | **Data-tier scaling lead time**: for Postgres, Mongo and Redis, how long a resize takes and whether it needs downtime or a maintenance window | Infra knowledge / runbooks | §8A.4 item 9. Decides how far ahead of festive data-tier changes must happen. |

### 10.2 Per-pod resource behaviour and the GOMAXPROCS question (staging)

Staging runs the same image as production, so GOMAXPROCS and throttling behaviour can be confirmed here (G2) without touching production. For a like-for-like reading, give the staging pod production's CPU limit (1000m) first.

```bash
NS=staging
LABEL=app=decision-manager          # or app=external-service-adapter
OUT=dm_$(date +%Y%m%d_%H%M).txt

# 15s sampling for the full test duration (240 iters = 1h)
for i in $(seq 1 240); do
  echo "=== $(date -Iseconds) ===" >> "$OUT"
  kubectl top pod -n "$NS" -l "$LABEL" --no-headers >> "$OUT" 2>&1
  sleep 15
done

# Min/max memory across the window
grep -oE '[0-9]+Mi' "$OUT" | tr -d 'Mi' | sort -n \
  | awk 'NR==1{min=$1} {max=$1} END{printf "min=%sMi max=%sMi\n", min, max}'
```

```bash
# GOMAXPROCS vs cgroup CPU quota — the direct test of G2.
POD=$(kubectl get pod -n $NS -l $LABEL -o jsonpath='{.items[0].metadata.name}')

# cgroup v2: "<quota> <period>" -> cores = quota/period
kubectl exec -n $NS $POD -- cat /sys/fs/cgroup/cpu.max 2>/dev/null
# cgroup v1 fallback
kubectl exec -n $NS $POD -- cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null
kubectl exec -n $NS $POD -- cat /sys/fs/cgroup/cpu/cpu.cfs_period_us 2>/dev/null

# Throttling: nr_throttled / nr_periods. Sustained >5% confirms oversubscription.
kubectl exec -n $NS $POD -- cat /sys/fs/cgroup/cpu.stat
# sample twice 60s apart and diff, to get the rate rather than lifetime totals
```

> Once telemetry is enabled, `go_maxprocs` and `go_numcpu` appear directly in the ESA/DM snapshot lines — easier than the above. `go_maxprocs > cores_from_cpu.max` is the confirmation.

```bash
# Restarts and OOMKills
kubectl get pods -n $NS -l "$LABEL" -o custom-columns='NAME:.metadata.name,\
RESTARTS:.status.containerStatuses[0].restartCount,\
LASTSTATE:.status.containerStatuses[0].lastState.terminated.reason'

kubectl get events -n $NS --field-selector reason=OOMKilling --sort-by=.lastTimestamp | tail -20
```

### 10.3 Telemetry extraction during tests

```bash
# Follow telemetry snapshots live
kubectl logs -n $NS -l app=decision-manager --tail=0 -f | grep runtime_snapshot

# Pull a window and extract the capacity-relevant fields
kubectl logs -n $NS -l app=external-service-adapter --since=1h \
  | grep runtime_snapshot > esa_telemetry.jsonl

# Peak concurrency utilisation and goroutine count per pod (log.format=json)
jq -r 'select(.telemetry=="runtime_snapshot")
       | [.ts, .sequence_in_flight, .sequence_limiter_cap, .sequence_limiter_util_pct,
          .go_goroutines, .go_heap_inuse_mb, .async_esa_queue_len,
          .async_esa_inline_fallback_total, .go_maxprocs, .go_gc_cpu_fraction]
       | @tsv' esa_telemetry.jsonl | sort -k4 -nr | head -20

# 503 onset — the saturation marker
kubectl logs -n $NS -l app=decision-manager --since=1h \
  | grep -c "max concurrent limit reached"
kubectl logs -n $NS -l app=external-service-adapter --since=1h \
  | grep -c "rejected: max concurrent limit reached"
```

### 10.4 Postgres (§9-G) — staging; production via Infra E7

Full queries in `Baseline_Extraction_Queries.md` §8. Run them on the **staging** databases yourself; production numbers come from Infra (E7). The essential check:

```bash
kubectl run pgcheck -n $NS --rm -it --restart=Never --image=postgres:16-alpine -- \
  psql "postgresql://USER:PASS@HOST:PORT/DB" -c "SHOW max_connections;" \
       -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"
```

```sql
-- The number that matters: server ceiling vs configured pool × replicas
SHOW max_connections;                       -- compare against 500 × maxReplicas
SELECT client_addr, count(*) FROM pg_stat_activity GROUP BY 1 ORDER BY 2 DESC;
```

### 10.5 Config drift — staging vs prod (§6-G12, hardening item 4)

```bash
cd /path/to/configurations

# Which keys exist in prod but not staging, and vice versa
for svc in decision-manager external-service-adapter; do
  echo "=== $svc ==="
  comm -3 \
    <(grep -oE '^[a-zA-Z][a-zA-Z0-9._-]*' $svc/${svc}_staging.properties | sort -u) \
    <(grep -oE '^[a-zA-Z][a-zA-Z0-9._-]*' $svc/${svc}_production.properties | sort -u)
done

# Values that differ for keys present in both
for svc in decision-manager external-service-adapter; do
  echo "=== $svc ==="
  diff <(grep -E '^[a-zA-Z]' $svc/${svc}_staging.properties | sort) \
       <(grep -E '^[a-zA-Z]' $svc/${svc}_production.properties | sort)
done
```

**The repo diff above is not enough on its own.** The DM production file in the repo is out of sync with the cluster (§8A.6). For T-PE, diff **staging's live ConfigMap** (which you can read) against **production's live ConfigMap from Infra (E1)**:

```bash
# Staging — you can run this
kubectl get configmap -n staging decision-manager-config -o yaml        > dm_staging_live.yaml
kubectl get configmap -n staging external-service-adapter-config -o yaml > esa_staging_live.yaml

# Production — save Infra's E1 output as dm_prod_live.yaml / esa_prod_live.yaml, then:
diff <(grep -E '^\s+[a-zA-Z].*=' dm_staging_live.yaml  | sed 's/^ *//' | sort) \
     <(grep -E '^\s+[a-zA-Z].*=' dm_prod_live.yaml     | sed 's/^ *//' | sort)
diff <(grep -E '^\s+[a-zA-Z].*=' esa_staging_live.yaml | sed 's/^ *//' | sort) \
     <(grep -E '^\s+[a-zA-Z].*=' esa_prod_live.yaml    | sed 's/^ *//' | sort)
```

Every difference is either a T-PE checklist item (§5.5.1) or a deliberate exception (telemetry, endpoints, DB hosts).

### 10.6 Local profiling (§5.1)

```bash
# Approximate the production cgroup so GOMAXPROCS/throttling behaviour is representative
docker run --cpus=1 --memory=1000m -e GOMEMLIMIT=900MiB \
  -e MEMORY_LIMIT_BYTES=1048576000 <esa-image>

# CPU profile under load — identifies the hot paths behind T_hold
go tool pprof -http=:8081 'http://localhost:8080/debug/pprof/profile?seconds=30'
go tool pprof -http=:8082 'http://localhost:8080/debug/pprof/heap'
go tool pprof -http=:8083 'http://localhost:8080/debug/pprof/goroutine'
```

> `net/http/pprof` is not currently registered in either router. Adding it behind an env flag for local/staging only (never production) would make the CPU question much easier to answer. Small change — say if you want it.

### 10.7 Cleaning up test data

```javascript
// STAGING ONLY. The k6 script puts LOADTEST_ in Application_Id__c.
// DM stores it LOWERCASED under esaRequestBody (default Go driver naming); ESA stores it as applicationId.
db.getCollection("decision-manager-log").countDocuments({ "esaRequestBody.applicationid": { $regex: "^LOADTEST_" } });
db.getCollection("esa-log").countDocuments({ applicationId: { $regex: "^LOADTEST_" } });

// Soft delete (preferred; the schema has isDeleted)
db.getCollection("decision-manager-log").updateMany({ "esaRequestBody.applicationid": { $regex: "^LOADTEST_" } },
                                                    { $set: { isDeleted: true } });
db.getCollection("esa-log").updateMany({ applicationId: { $regex: "^LOADTEST_" } }, { $set: { isDeleted: true } });
```

Agree the marker prefix before T1 so cleanup is unambiguous. Also confirm whether load-test decisions will create Salesforce records in the UAT sandbox — if so, that needs its own cleanup plan and is part of §9-D.

---

## 11. Change log

**v1.5.** Fast Mongo queries, production snapshot, production-shaped mocks.

| Item | Change |
|---|---|
| Query doc §F | **New fast queries M0–M5.** They window on the always-indexed `_id` (ObjectId time) and carry `maxTimeMS: 60000`. Q1–Q18 are marked "do not run on production". Includes how to kill your own runaway query with `read`-only access. |
| §A.0, §A.1b | Live partner × stage list; findings from the production config snapshot (sequence shapes; Quickwork at up to 8 calls per request across all 22 live sequences; Actico in every path; two raw-IP endpoints; 149 SF sub-request templates). |
| §A.4 step 5 | Mocks are now **clones of real production sequences** (Tecno kyc, GooglePay loan), generated from the snapshot with one mock host per real host and M3 p50 delays. |
| §A.5 | Tests use the clones and a production kyc/loan `MIX`. **New T1-DM-R** corrects DM capacity for Salesforce templating, which the mocks skip. |
| §A.7 | New rows: internal services in every decision path; third-party gateway concurrency. |
| Snapshot hygiene | Two literal API keys and one password the export had missed were redacted in place. Moved to `local/prod_db_snapshot/`, which the repo `.gitignore` already excludes. |

**v1.3.** Production boundary, verified queries, ConfigMap resolved.

| Item | Change |
|---|---|
| §1 | **New "Production boundary".** Production is touched only by your read-only Mongo queries on the read replica and by Infra's read-only enquiries; everything else is staging. |
| §10.1 | **Replaced.** Production `kubectl` commands are now **Infra enquiries E1–E11**, all read-only. §10.2 onward is staging-only (`NS=staging`). |
| §8A.6, §9-G2, §9A | **ConfigMap resolved:** the live DM ConfigMap is correct and logging works; the repo is out of sync. All DM production values from the repo, and the T-PE checklist, are now marked *confirm against E1*. Phase 0a done. |
| §5.5.1 | Values flagged as repo-sourced; staging node-capacity check added for 7 × 1000Mi/1000m. |
| §4.3 | **k6 payload corrected** to the real `TriggerDecisionRequest` JSON keys (`Application_Id__c`, `RecordId__c`, `LeadSource__c`, `StageEvent__c`, `workflowId__c`). The previous `leadId`/`stage`/`partnerName` keys would have failed DM's validation with a 400. |
| §10.7 | Cleanup filter corrected to `esaRequestBody.applicationid` (lowercased) and marked staging-only. |
| Query doc | **Rewritten and verified:** 18 queries plus 2 checks plus 2 fallbacks, run on MongoDB 7.0.14 and parsed with Compass's parser. **Three errors fixed:** lowercased DM keys, zero-date end times, ESA request dedup and fan-out double-counting. |

**v1.2 — 2026-09-18.** Data tier and current-production-ceiling added.

| Item | Change |
|---|---|
| §8A | **New section.** Data-tier scaling: configured demand per pod and at `maxReplicas`, a demand model that recomputes when `N` changes, what to check before/during/after, an 11-item **handover table for Infra**, and data-tier alerts. Addresses the failure mode where correctly scaled services still fail on under-scaled databases. |
| §8A.1 | **New findings.** Postgres configured for `7 × 500 = 3,500` connections per database against a typical server limit of 100–500. DM's Mongo pool is **unbounded** (`mongo.maxPoolSize` unset → `GetConfigInt` returns 0 → driver treats as no limit) while ESA's is 10. |
| §8A.2 | **New.** Mongo write rate is `λ × (1 + S)` because **ESA writes one document per service**, not per request — the multiplier most likely to be missed when sizing. |
| §8A.6 | **New finding.** DM production Mongo config keys (`mongo.connection_string`/`database_name`) don't match what the code reads (`mongo.connectionString`/`dbName`); staging matches. Either the live ConfigMap differs from the repo — which would require re-verifying every config finding here — or prod Mongo logging is degraded. **Check first.** |
| §5.5 | **New.** T-PE, the prod-equivalent capacity test: answers "what can production take today" on staging, with a full replication checklist (every setting that differs), what load to apply, and the data-tier caveat. Also notes staging has **no HPA**. |
| §5 ladder | T-PE added. |
| §9-G | Rewritten to point at §8A; split out §9-G2 for the ConfigMap discrepancy. |
| §9A | Resequenced: ConfigMap check first, Infra handover early (long lead time), T-PE before *and* after hardening. |
| §1 | Data tier added as constraint 7; restructured around the two questions being answered. |
| Query doc §2.7 | **New.** Document sizes via `$bsonSize`, empirical `(1+S)` write multiplier, collection/index sizes, `serverStatus()` connection and write-ticket sampling, TTL-index check. |

**v1.1 — 2026-09-18.** Revised after review.

| Item | Change |
|---|---|
| §5.2, §6-G1 | **Corrected.** Autoscaling works; the "pinned at `maxReplicas`" claim was wrong. Real question is whether `maxReplicas=7` suffices. HPA retuning moved to not-recommended. |
| §6-G4 | **Reframed.** `MaxConnsPerHost=25` is deliberate provider protection, not a defect. New finding: it is enforced per pod, so fleet pressure is `25 × replicas` — now the top blocker for raising `maxReplicas`. |
| §6-G5 | **Reframed.** Caps that are never reached cost nothing. Reframed around safety-net value and measurement-first. |
| §1 | Rewritten around the stated objective: per-pod capacity → fleet sizing. |
| §4.2 | **New.** Assessment of `X-Process-Mode: sync` — keep it; verified the limiter holds a slot identically in both modes, so sync-mode calibration transfers to async production. |
| §4.3 | **New.** Grafana k6 explained, install command, starter script, both workload models. |
| §4.4 | **ESA telemetry implemented.** Also DM/ESA log-extraction commands. |
| §5, §5.1 | Production testing removed; T9 replaced with an autoscaler-reaction test. Local-vs-staging validity assessed. |
| §7 | Reordered by what to do before T1 vs what is blocked; HPA and cap retuning deprioritised. |
| §9 | Resolved items marked; §9-B (provider budgets) promoted to top blocker. |
| §10 | **New.** Consolidated commands for every open item. |
| — | **New companion:** `Baseline_Extraction_Queries.md` — Mongo aggregations bifurcated by stage/partner (DM) and serviceName/stage (ESA), plus Postgres queries. |

**v1.0 — 2026-09-18.** Initial plan.

---

*Findings in §2 and §6 are code-verified against `main` in both repos, with file/line references. Values in §5 and §8 require measurement (§3, T1) before use. Production is out of scope for testing.*
