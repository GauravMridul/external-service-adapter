# Staging Load Testing Plan – ESA & Decision Manager

This document describes the end-to-end plan for load testing the External Service Adapter (ESA) and Decision Manager (DM) in staging, tuning their limits for best utilization without OOM, and using the results to define alert thresholds and scaling behavior.

---

## 1. Goals

- **Max throughput without OOM:** Utilize allocated memory and CPU to the best possible degree while keeping the system safe.
- **Latency:** p95/p99 overhead should not exceed 1–1.5 s on top of time spent in external/sequence service calls.
- **Run near limit:** Operate close to capacity by design; use alerts and scaling (spin up more pods) when approaching the limit.
- **Informed tuning:** Use test outcomes plus MongoDB and DB data to fine-tune resources and to set correct alert limits.

---

## 2. Context

| Item | Detail |
|------|--------|
| **ESA** | Sync API only. Request → full sequence processing → response. |
| **DM** | **Production uses only the async** trigger-decision API (request returns quickly; processing in background). **For load testing we use the sync** trigger-decision API so each request holds for the full processing time (DM → ESA → DM work), giving real in-flight concurrency and measurable load. |
| **Access** | Load can be generated from a machine that connects to the cluster via AWS VPN (e.g. Postman for manual checks; k6/Artillery on same machine for sustained load). |
| **Constraints** | DB and Redis pool sizes are not changed. Service replicas and memory limits can be changed as needed. |

---

## 3. Observability and Instrumentation

### 3.1 Current state

- Establish what is available today: Prometheus/Grafana, CloudWatch, or only `kubectl top` and application logs.
- Aim to obtain (or add) the following for visibility and decision-making:
  - **Request rate** (e.g. from access logs, API gateway, or load tool).
  - **Latency percentiles** (p50, p95, p99) from load tool and/or APM.
  - **5xx and 503 count** from load tool and/or logs.
  - **Pod memory and CPU over time** (e.g. `kubectl top pod` sampled every 30–60 s to a file, or from metrics).

### 3.2 Temporary logging (for test period only)

- **ESA:** When a sequence request is rejected at capacity (503), log once per rejection (e.g. `"sequence request rejected at capacity"` with correlation_id). Remove after testing.
- **DM:** When a trigger-decision request is rejected or blocked at capacity, log once (e.g. `"trigger decision at capacity"`). Remove after testing.
- Use these logs to correlate 503s with capacity and to validate limiter behavior.

### 3.3 MongoDB and DB data (post–test)

- **DM:** `triggerDecisionTime` in decision-manager Mongo log gives full DM processing time per request.
- **ESA:** Per-service `timeTaken` and total execution time in ESA Mongo log give time in sequence/external calls.
- **Post-test:** Share DB configuration and MongoDB data (for the test time window) so that:
  - Overhead can be computed (total latency vs time in sequence / vs `triggerDecisionTime`).
  - Limits and alert thresholds can be chosen in an informed way.

---

## 4. Load Tooling

- **Preferred:** Install **k6** (or Artillery) on the AWS machine that has VPN access to the cluster. Use a script that:
  - Calls ESA process-sequence or DM sync trigger-decision with the same URL/headers/body as production-like requests.
  - Runs a ramp (e.g. 5 → 10 → 20 → 40 RPS or stepped concurrent users) then holds at a target for 15–30 min.
  - Reports RPS, p50/p95/p99, and 5xx/503 count.
- **Alternative:** Postman Collection Runner with many iterations and concurrent runs (document the setup and its limits).
- **During every test:** Run a pod memory/CPU sampling loop (e.g. `kubectl top pod` every 60 s to a file) for the full duration.

---

## 5. Stage 1 – ESA Load Testing

### 5.1 Baseline

- Record current ESA staging config:
  - Memory-based concurrency: `server.use_memory_based_concurrency_limit`, `server.concurrency_base_reserve_mb`, `server.concurrency_memory_per_request_mb`, `server.max_concurrent_sequence_requests_min/max`.
  - Replicas and pod memory limit (e.g. 512 Mi).
- With no load, sample `kubectl top pod` for ESA; record min memory and CPU per pod.

### 5.2 Test execution

- **Ramp:** Increase load (e.g. 5 → 10 → 20 → 40 RPS or by concurrent VUs) with 2–5 min per step.
- **Steady state:** Hold at a target load (e.g. 2× expected peak or until 503s appear) for 15–30 min.
- **Capture:**
  - Request rate, p50/p95/p99 latency, 5xx/503 count (from load tool and/or logs).
  - Pod memory and CPU (from sampling loop).
  - When 503s and “rejected at capacity” logs first appear.

### 5.3 Find the limit

- Ramp until 503 (and/or “rejected at capacity” logs) or until pod memory approaches the limit (or OOM).
- Record: RPS and approximate concurrency at that point, max memory per pod, any pod restarts (OOM).

### 5.4 Tune ESA config

- **memory_per_request_mb:**  
  `memory_per_request_mb ≈ (max_observed_memory_Mi − baseline_idle_Mi) / concurrent_requests_at_that_time`.  
  Set `server.concurrency_memory_per_request_mb` with small headroom. If concurrency is unknown, use (RPS × average_latency_sec) as a proxy.
- **Reserve and clamps:** Adjust `server.concurrency_base_reserve_mb` and min/max clamps so the effective cap is slightly above the RPS you want to support (run near limit).
- If OOM occurred: increase reserve or memory_per_request_mb (or lower max clamp); re-test.
- **Latency:** If p95/p99 add more than 1–1.5 s over external call time, investigate CPU, connection pool, or cap; tune only within constraints (no DB/Redis pool size changes unless agreed).

### 5.5 Lock and document

- Update ESA staging config (and ConfigMap); restart pods.
- Document: test scenario (tool, RPS, duration, replicas), final config, max RPS, p95/p99, 503 rate, max memory, and one-line rationale for each limit.

---

## 6. Stage 2 – DM Load Testing

### 6.1 API choice

- Use the **sync** trigger-decision API so that each request represents one full in-flight trigger-decision (DM → ESA → DM). This properly loads both DM and ESA and allows tuning of `server.max_concurrent_trigger_decisions`.

### 6.2 Baseline

- Record DM staging config: `server.max_concurrent_trigger_decisions`, replicas, pod memory limit.
- Record baseline pod memory/CPU with no load.

### 6.3 Test execution

- Same approach as ESA: ramp then steady state; capture RPS, p50/p95/p99, 5xx/503, pod memory/CPU, and “at capacity” logs.
- Ramp until latency degrades or limit is hit (or 503 / at-capacity logs).

### 6.4 Tune DM config

- Set `server.max_concurrent_trigger_decisions` so the system runs near limit without OOM. Adjust replicas or memory limits only if needed and within constraints.

### 6.5 Lock and document

- Update DM staging config (and ConfigMap); restart pods.
- Document test scenario, final config, and results.

---

## 7. Post–Test: Mongo and DB for Informed Decisions

- **Share after test runs:**
  - DB configuration (as used in staging).
  - MongoDB data for the test window:
    - Decision-manager logs: e.g. `triggerDecisionTime` (and any identifiers needed to match requests).
    - ESA logs: per-service `timeTaken` and total execution time.
- **Use this to:**
  - **Overhead:** Compare total request latency vs “time in sequence” (ESA) and vs `triggerDecisionTime` (DM) to validate p95/p99 overhead (≤ 1–1.5 s).
  - **Resources:** Correlate with pod memory/CPU and 503 rate to justify final concurrency and memory settings.
  - **Alerts:** Propose concrete alert thresholds (see below).

---

## 8. Post–Test: Using Results to Optimize Resource Allocation on Microservices

After each test run, use the collected data to optimize **application config**, **pod resources** (CPU/memory limits), and **replicas** so that staging (and later production) achieves best utilization without OOM.

### 8.1 Inputs from the test

Gather and keep in one place:

| Input | Source | Use |
|-------|--------|-----|
| Max memory per pod (Mi) | `kubectl top` sampling or metrics | Right-size memory limit; compute memory_per_request_mb |
| Baseline (idle) memory per pod (Mi) | Same, no-load sample | Denominator for per-request MB |
| Max RPS at limit (or just before 503) | Load tool | Target throughput; set cap slightly above this |
| Approximate concurrency at limit | RPS × avg latency (s), or “at capacity” log count | For memory_per_request_mb formula |
| 503 rate / OOM (yes/no) | Load tool, logs, pod restarts | Safety: avoid OOM; tune cap and reserve |
| p95/p99 latency | Load tool | Overhead vs Mongo (sequence time, triggerDecisionTime) |
| Pod CPU at peak | `kubectl top` or metrics | Decide if CPU limit is a bottleneck |

### 8.2 What to optimize (decision flow)

**Step 1 – Application config (concurrency and memory model)**

- **ESA**
  - **memory_per_request_mb:**  
    `(max_memory_Mi − baseline_idle_Mi) / concurrent_requests` with small headroom (e.g. round up).  
    Update `server.concurrency_memory_per_request_mb` in config.
  - **Reserve:** If you hit OOM, increase `server.concurrency_base_reserve_mb`; if memory was well under limit, you can decrease reserve to allow more concurrent requests.
  - **Clamps:** Set `server.max_concurrent_sequence_requests_min/max` so the effective cap (from memory formula) is slightly above the RPS you want to support (run near limit). Ensure max is not so high that (max × memory_per_request_mb + reserve) could exceed pod memory.
- **DM**
  - Set `server.max_concurrent_trigger_decisions` to a value at which you observed stable behavior (no OOM, acceptable latency). Typically “max concurrency seen at limit minus small buffer,” or the value at which 503s started so that scaling (more pods) kicks in before that under real traffic.

**Step 2 – Pod resource allocation (deployment YAML)**

- **Memory limit**
  - If **max observed memory** was well below the current limit (e.g. 300 Mi vs 512 Mi): you can **lower** the pod memory limit to improve node packing and cost, but leave ~15–20% headroom above max observed (e.g. set 384 Mi). Then set `MEMORY_LIMIT_BYTES` (ESA) to match so the memory-based cap stays correct.
  - If you saw **OOM or very close to limit:** keep or **increase** the pod memory limit (and `MEMORY_LIMIT_BYTES` for ESA) so that (reserve + cap × memory_per_request_mb) stays safely below the limit.
- **CPU limit**
  - If CPU was saturated (e.g. 100% or at limit) while latency was high, consider **increasing** CPU limit or adding replicas. If CPU was low and memory was the bottleneck, CPU allocation is likely fine.

**Step 3 – Replicas (and scaling)**

- **Staging:** If you need more throughput than one pod can safely give, **increase replicas** (e.g. 2 → 3) and re-run a short test to confirm aggregate throughput and that no single pod OOMs.
- **Production:** Use the same per-pod limits and concurrency; set **HPA** (or scaling policy) so that when 503 or memory/CPU thresholds are breached, more pods are added. The test-derived “max RPS per pod” and “memory at limit” directly inform **alert thresholds** and **target utilization** (e.g. scale when memory > 80% or 503 rate > 0).

### 8.3 Summary table: test outcome → allocation change

| Observation | Action |
|-------------|--------|
| OOM or restarts | Increase `server.concurrency_base_reserve_mb` or `server.concurrency_memory_per_request_mb`; or lower max clamp; or increase pod memory limit. |
| Max memory well below limit | Optionally lower pod memory limit (with headroom) and update `MEMORY_LIMIT_BYTES`; or increase max clamp to use more of the existing limit. |
| 503 at lower RPS than desired | Increase effective cap (higher max clamp or lower memory_per_request_mb within safety) or add replicas. |
| High latency (overhead > 1–1.5 s) | Check CPU saturation; if CPU-bound, increase CPU limit or replicas. If cap-bound, relax cap or add replicas. |
| Stable at high RPS, no OOM | Lock current app config and pod limits; use observed “max memory” and “RPS at limit” to set alert thresholds (e.g. alert when memory > 80% of limit, or 503 > 0). |

### 8.4 Production extrapolation

- Use the **same** `memory_per_request_mb` and reserve logic in production (per-pod memory limit may be higher, e.g. 1000 Mi). Effective cap scales with (limit − reserve) / memory_per_request_mb.
- Scale **replicas** and **HPA** so that at expected peak traffic you stay just under the aggregate capacity (sum of per-pod caps), with alerts in place to add pods when approaching limit.

---

## 9. Alert and Scaling Recommendations

Testing should yield results that support both resource tuning and **setting correct limits for alerts**. Recommended alerts (to be refined with actual test data):

- **503 rate:** Alert when 503 rate > 0 (or above a small threshold) for ESA and/or DM so that scaling can be triggered before sustained overload.
- **Pod memory:** Alert when pod memory utilization exceeds a threshold (e.g. 80% of limit) for ESA/DM pods.
- **Optional:** CPU above a threshold.
- **Scaling:** Ensure HPA (or scaling policy) can add pods when these fire; document that “run near limit” implies scaling out when alerts trigger.

Document the chosen alert thresholds and the test data (max RPS, memory at limit, 503 rate) that justified them.

---

## 10. Deliverables

| Deliverable | Description |
|-------------|-------------|
| **ESA staging config** | Final concurrency and memory-related settings, with short rationale. |
| **DM staging config** | Final `server.max_concurrent_trigger_decisions` (and any replica/memory changes), with short rationale. |
| **Resource allocation recommendations** | Per-pod memory/CPU limits, replica count (or HPA min/max), and rationale from post-test data (see §8). |
| **Alert recommendations** | Suggested alert thresholds (503, memory, optionally CPU) and how they were derived from test results. |
| **Test report** | Scenario (tool, RPS, duration, replicas), metrics (max RPS, p95/p99, 503 rate, max memory), and use of Mongo/DB data for overhead and limits. |

---

## 11. Optional: Production

- Apply the same `memory_per_request_mb` and reserve logic to production; scale per-pod concurrency (and DM cap) with production memory limits and replica count.
- Re-validate with a short load test in production if possible.

---

## 12. EKS / kubectl Reference

**Pod memory sampling (run during test):**

```bash
NAMESPACE=staging
LABEL=app=external-service-adapter-uat   # or app=decision-manager-uat for DM
OUTPUT_FILE=esa_staging_memory_$(date +%Y%m%d_%H%M).txt

for i in $(seq 1 60); do
  echo "=== $(date -Iseconds) ===" >> "$OUTPUT_FILE"
  kubectl top pod -n "$NAMESPACE" -l "$LABEL" >> "$OUTPUT_FILE" 2>&1
  echo "" >> "$OUTPUT_FILE"
  sleep 60
done
```

**Min/max memory from file:**

```bash
grep -oE '[0-9]+Mi' "$OUTPUT_FILE" | sed 's/Mi//' | sort -n | awk 'NR==1{min=$1} {max=$1} END{printf "min_Mi=%s max_Mi=%s\n", min, max}'
```

**Container memory limit:**

```bash
kubectl get pods -n staging -l app=external-service-adapter-uat -o jsonpath='{.items[0].spec.containers[0].resources.limits.memory}'
```

---

*Document version: 1.0. Last updated: 2026-02.*
