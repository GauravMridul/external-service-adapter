# Baseline Extraction — exact queries for the production Mongo read replica

Companion to `Festive_Load_Test_And_Scaling_Plan.md`. **Everything here is read-only.**

> ## ⚠ Use §F (fast queries) only. Do not run Q1–Q18 on production.
> Q1–Q18 further down filter on `createdAt` over 60 days, with no time limit. Q1's run time suggests `createdAt` isn't indexed on production; in that case each query reads the **whole collection**, embedded payloads included. The §F queries don't depend on that index either way. Q1–Q18 are kept below only as a reference for field names and logic.

---

## F. Fast baseline — verified on MongoDB 8.0.15 as a read-only user

### F.0 If a query is still running: stop it (works with read-only access)

You can always kill **your own** operations. No admin role is needed. Paste this into the Compass MongoSH panel, connected with the same user:

```javascript
var mine = db.currentOp({ $ownOps: true, active: true, ns: /^<your-db-name>\./ }).inprog;
mine.forEach(function (op) { print("opid=" + op.opid + "  running " + op.secs_running + "s  on " + op.ns); });
mine.forEach(function (op) { printjson(db.killOp(op.opid)); });
```

Replace `<your-db-name>` with the database you ran `use` on. The `ns` filter matters: without it, the list includes the `currentOp` command itself, which a read-only user isn't allowed to kill.

Verified on 8.0.15 as a user with only the `read` role: a query that had been running 16 seconds was killed, and its client got `Interrupted … operation was interrupted`.

If `currentOp` returns nothing, the operation has already finished or timed out. Closing Compass does **not** stop a server-side aggregation; only `killOp` or the query's own time limit does.

### F.1 Why these are fast

| Technique | Effect |
|---|---|
| **Time window on `_id`, not `createdAt`** | Every document's `_id` is an ObjectId the Go driver generates at insert, so its first 4 bytes are the insert time. `_id` **always** has an index. A range on `_id` reads only the documents in the window, and needs no new index or admin action. |
| **M0 and M1 read the index only** | They only need `_id`, so MongoDB answers them from the index without fetching any document. In testing: `PROJECTION_COVERED > IXSCAN`, **`docsExamined = 0`**. These are cheap even across 14 days. |
| **M2–M5 use a short window** (default 2 hours) | They read only documents from those hours. In testing, keys examined equalled documents in the window exactly, never the collection. |
| **`maxTimeMS: 60000` on every query** | The server stops the query itself after 60 seconds. Tested: a deliberately heavy query stopped at 5,013 ms with a 5,000 ms limit. Nothing can run away again. |
| **`hint: { _id: 1 }`** | Forces the `_id` index so the planner can't pick a scan. |

`_id` is the **insert** time. For DM that's when the async audit write runs, just after the decision ends; for ESA, just after the sequence ends. Seconds of difference don't matter for hourly or daily buckets.

### F.2 How to run

1. Compass → connect to the read replica → **MongoSH** panel → `use <your-db-name>`.
2. If the collection names differ from `decision-manager-log` / `esa-log`, replace them in the statements.
3. Paste the **set-up block** first, then one statement at a time, in order.
4. After M1, set `WIN_START` to the start of one of the busiest hours M1 shows, re-paste the set-up block, then run M2–M5.
5. If any query says **`MaxTimeMSExpired`**, halve `HOURS` in the set-up block and re-run. No other change is needed.

The same text is saved as `loadtest/mongo/fast_baseline.js` in this repo; it's byte-identical to what was tested.

> **Run notes from production (6 Oct):**
> - The **DM** M0 ran fine: 49–74k decisions a day.
> - The **ESA** M0 hits the 60s limit: ESA writes about 2M documents a day. Use an index-only one-day count instead; 5 Oct gave 1,972,155: `db.getCollection("esa-log").countDocuments({ _id: { $gte: ObjectId("6ac29b280000000000000000"), $lt: ObjectId("6ac3eca80000000000000000") } }, { maxTimeMS: 60000, hint: { _id: 1 } })`
> - **DM documents average ~1 MB at peak**, so any query that reads every document in a window is slow. At the peak hour, M2 over 30 minutes, M3 over 15 minutes and M5 over 30 minutes all timed out, while M7 over 15 minutes finished. The **sampled** versions S2/S3 also timed out on production: random fetches across an hour are the worst access pattern for documents that aren't in the replica's cache. What works is short, contiguous windows, ideally ones a previous query has already read (5 Oct 11:00–11:10 and 19:00–19:15 both completed).
> - Shell variables are lost if the set-up block isn't run in the same shell session, which includes the Aggregations tab. Use the self-contained versions with the dates written in: `loadtest/mongo/fast_baseline_selfcontained.js`.

### F.3 The queries

```javascript
// ---- set-up block: paste first. var (not const) so it can be re-pasted with new values ----
var FROM14 = ObjectId.createFromTime(Math.floor(ISODate("2026-09-22T00:00:00+05:30").getTime() / 1000));
var TO     = ObjectId.createFromTime(Math.floor(ISODate("2026-10-06T00:00:00+05:30").getTime() / 1000));
var WIN_START = "2026-10-05T11:00:00+05:30";   // start of a busy period: set from M1's busiest hours
var HOURS     = 2;                              // M2-M5 window length. If a query hits MaxTimeMSExpired, halve this.
var W_FROM = ObjectId.createFromTime(Math.floor(ISODate(WIN_START).getTime() / 1000));
var W_TO   = ObjectId.createFromTime(Math.floor(ISODate(WIN_START).getTime() / 1000) + HOURS * 3600);
var OPTS   = { maxTimeMS: 60000, hint: { _id: 1 } };   // server stops the query after 60 s
```

**M0 — documents per day, both collections** (index only). Gives the daily volume trend and, from the ESA/DM ratio, documents written per decision.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateToString: { date: { $toDate: "$_id" }, format: "%Y-%m-%d", timezone: "Asia/Kolkata" } }, dm_docs: { $sum: 1 } } },
  { $project: { _id: 0, day_ist: "$_id", dm_docs: 1, weekday: { $arrayElemAt: [["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"], { $dayOfWeek: { date: { $dateFromString: { dateString: "$_id" } } } }] } } },
  { $sort: { day_ist: 1 } }
], OPTS)
```

```javascript
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateToString: { date: { $toDate: "$_id" }, format: "%Y-%m-%d", timezone: "Asia/Kolkata" } }, esa_docs: { $sum: 1 } } },
  { $project: { _id: 0, day_ist: "$_id", esa_docs: 1, weekday: { $arrayElemAt: [["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"], { $dayOfWeek: { date: { $dateFromString: { dateString: "$_id" } } } }] } } },
  { $sort: { day_ist: 1 } }
], OPTS)
```

**M1 — busiest minutes and hours** (index only). The top minute is **today's peak arrival rate**, the number festive projections multiply. The busiest hours tell you where to point `WIN_START`.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateTrunc: { date: { $toDate: "$_id" }, unit: "minute", timezone: "Asia/Kolkata" } }, n: { $sum: 1 } } },
  { $sort: { n: -1 } }, { $limit: 20 },
  { $project: { _id: 0, minute_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:%M", timezone: "Asia/Kolkata" } }, decisions: "$n", per_sec: { $round: [{ $divide: ["$n", 60] }, 2] } } }
], OPTS)
```

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateTrunc: { date: { $toDate: "$_id" }, unit: "hour", timezone: "Asia/Kolkata" } }, n: { $sum: 1 } } },
  { $sort: { n: -1 } }, { $limit: 10 },
  { $project: { _id: 0, hour_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:00", timezone: "Asia/Kolkata" } }, decisions: "$n", per_sec: { $round: [{ $divide: ["$n", 3600] }, 3] } } }
], OPTS)
```

**M2 — DM decision time (`T_hold`) by stage × partner**, for the window. `p95_ms` is `T_hold` for the capacity model; `esa_*` and `sf_*` show where the time goes. The `decisions` counts also give the kyc/loan traffic split for the k6 `MIX`.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO }, triggerDecisionTime: { $gt: 0 } } },
  { $project: {
      stage: "$esaRequestBody.stage", partner: "$esaRequestBody.partnername",
      total: "$triggerDecisionTime", esa: { $ifNull: ["$esaExecutionTime", 0] }, sf: { $ifNull: ["$sfCompositeExecutionTime", 0] } } },
  { $group: {
      _id: { stage: "$stage", partner: "$partner" }, decisions: { $sum: 1 },
      t: { $percentile: { input: "$total", p: [0.5, 0.95, 0.99], method: "approximate" } }, t_max: { $max: "$total" },
      e: { $percentile: { input: "$esa", p: [0.5, 0.95], method: "approximate" } },
      s: { $percentile: { input: "$sf", p: [0.5, 0.95], method: "approximate" } } } },
  { $project: { _id: 0, stage: "$_id.stage", partner: "$_id.partner", decisions: 1,
      p50_ms: { $arrayElemAt: ["$t", 0] }, p95_ms: { $arrayElemAt: ["$t", 1] }, p99_ms: { $arrayElemAt: ["$t", 2] }, max_ms: "$t_max",
      esa_p50: { $arrayElemAt: ["$e", 0] }, esa_p95: { $arrayElemAt: ["$e", 1] },
      sf_p50: { $arrayElemAt: ["$s", 0] }, sf_p95: { $arrayElemAt: ["$s", 1] } } },
  { $sort: { stage: 1, decisions: -1 } }
], OPTS)
```

**M3 — ESA per-service latency by service × stage**, for the window. **These p50s become the mock-provider delays.** Save the raw shell output to a text file; `gen_mock_from_snapshot.py --latencies` reads it directly. `live_calls` is lower than `calls` where a service sometimes uses its static response (e.g. `Dedupe`); percentiles are over live calls only.

```javascript
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO }, fanOutRole: { $ne: "per_call" } } },
  { $project: { serviceName: 1, stage: 1, timeTaken: 1, status: 1 } },
  { $group: {
      _id: { svc: "$serviceName", stage: "$stage" }, calls: { $sum: 1 },
      live_calls: { $sum: { $cond: [{ $gt: ["$timeTaken", 0] }, 1, 0] } },
      failed: { $sum: { $cond: [{ $eq: ["$status", "FAILED"] }, 1, 0] } },
      t: { $percentile: { input: { $cond: [{ $gt: ["$timeTaken", 0] }, "$timeTaken", null] }, p: [0.5, 0.95, 0.99], method: "approximate" } },
      t_max: { $max: "$timeTaken" } } },
  { $project: { _id: 0, service: "$_id.svc", stage: "$_id.stage", calls: 1, live_calls: 1,
      failed_pct: { $round: [{ $multiply: [{ $divide: ["$failed", "$calls"] }, 100] }, 1] },
      p50_ms: { $arrayElemAt: ["$t", 0] }, p95_ms: { $arrayElemAt: ["$t", 1] }, p99_ms: { $arrayElemAt: ["$t", 2] }, max_ms: "$t_max" } },
  { $sort: { stage: 1, p95_ms: -1 } }
], OPTS)
```

**M4 — document size**, from the first 2,000 documents of the window per collection. Feeds the Mongo storage figure for Infra.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO } } }, { $limit: 2000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], OPTS)
```

```javascript
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO } } }, { $limit: 2000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], OPTS)
```

**M5 (optional) — peak decisions in flight at once**, per hour, fleet-wide. Divide by the replica count Grafana shows for that hour to get per-pod concurrency, and compare it with the DM cap of 200.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO }, triggerDecisionTime: { $gt: 0 } } },
  { $project: { _id: 0, ev: [ { t: "$triggerDecisionStartTime", d: 1 }, { t: "$triggerDecisionEndTime", d: -1 } ] } },
  { $unwind: "$ev" }, { $replaceWith: "$ev" },
  { $match: { t: { $gt: ISODate("2000-01-01T00:00:00Z") } } },
  { $setWindowFields: { sortBy: { t: 1, d: 1 }, output: { inflight: { $sum: "$d", window: { documents: ["unbounded", "current"] } } } } },
  { $group: { _id: { $dateTrunc: { date: "$t", unit: "hour", timezone: "Asia/Kolkata" } }, peak_inflight: { $max: "$inflight" } } },
  { $sort: { peak_inflight: -1 } }, { $limit: 5 },
  { $project: { _id: 0, hour_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:00", timezone: "Asia/Kolkata" } }, peak_inflight: 1 } }
], OPTS)
```

### F.4 Verification

- **Engine and access:** run on **MongoDB 8.0.15** as a user with only the **`read`** role, against data built with the production field names and types.
- **Validity:** every statement parses as JavaScript, and every pipeline passes Compass's parser (`ejson-shell-parser`, strict mode).
- **Plans:** M0/M1 ran as `PROJECTION_COVERED > IXSCAN` with `docsExamined = 0`. M2–M5 ran as `IXSCAN > FETCH`, with documents examined equal to the documents in the window.
- **Results:** 9 of 9 statements returned rows.
- **Your data already confirmed** the field names (Q0b/Q0c). `workFlowId` is empty on production, which is why none of these queries key on it.

### F.5 What you no longer need to run

The production DB snapshot answers these directly, so the matching Mongo queries are dropped: sequence shapes and services per sequence (old Q11), provider hosts and calls per request (old Q12/Q13), and the partner/stage mapping. The plan's §A.1b summarises them.

---

## Reference only — the original queries (do not run on production)

**How these were verified (v2):** every pipeline below was:

1. run on **MongoDB 7.0.14** against synthetic data built with the exact BSON field names and types the production Go structs write. The names and types were confirmed by marshalling the real `DecisionManagerLog` and `EsaLog` structs with the same `mongo-driver` the services use;
2. parsed with **`ejson-shell-parser`**, the parser behind Compass's pipeline text editor, in strict mode, and then executed from the parsed output;
3. cross-checked where the logic is non-trivial. The concurrency queries Q8 and Q14 matched a brute-force event sweep exactly.

All 18 returned rows with 0 failures. What could **not** be verified is behaviour on your production data volume (run time, memory) and any data quirks that synthetic data cannot have. §3 covers both.

---

> **Production MongoDB is v8 (confirmed).** Everything below runs as written; ignore the §7 fallbacks. The run order and minimum set for the two-day plan are in the plan's §A.4 step 1.

## 0. Three errors fixed from the previous version

These would have returned wrong numbers silently. They are fixed below.

| Was | Problem | Fix |
|---|---|---|
| `esaRequestBody.partnerName` | The Go driver stores struct fields without `bson` tags under their **lowercased Go name**, and `ExternalServiceAdapterRequest` has only `json` tags. The real key is **`esaRequestBody.partnername`**. The old key matches nothing, so every partner would have shown as `UNKNOWN`. | All DM queries now use the lowercased keys (`partnername`, `applicationid`, `workflowid` …). Q0b confirms this on your data. |
| Concurrency sweep filtered `triggerDecisionEndTime: { $ne: null }` | An unset Go `time.Time` is stored as **0001-01-01**, not null. Incomplete decisions passed the filter with an end date before their start date, which corrupts the in-flight count. | Q8 now requires start and end after 2000-01-01, and `triggerDecisionTime > 0`. |
| ESA dedup on `workFlowId` + `stage` | A workflow can be re-triggered, which would merge separate requests into one. Fan-out also writes extra `per_call` and `aggregate` documents, so services were counted twice. | Every document of one ESA request shares an **identical `createdAt`** (stamped once at sequence end, `sequence_service.go:520-525`), so requests are keyed on `workFlowId` + `createdAt`. `fanOutRole` is filtered per query, as noted on each. |

---

## 1. How to run these in Compass

**Recommended: the embedded shell.** Connect Compass to the read replica, open the **MongoSH** panel at the bottom of the window, run `use <your-db-name>`, then paste a whole statement and press Enter. This is the form that was tested. It also passes `{ allowDiskUse: true }`, which large windows need.

**Alternative: the Aggregations tab**, which is useful if you want to export results to CSV. Open the collection, go to **Aggregations**, switch the editor to **text** mode, and paste **only the array**, from the opening `[` to the matching `]`. Leave out `db.getCollection(...).aggregate(` and `, { allowDiskUse: true })`. If a large window fails with a memory-limit error there, run it from the shell instead.

Run the queries **one at a time and off-peak**. They run on the replica only, but the 60-day ones scan a lot of data. Every result is flat (no nested fields), so it reads cleanly as a table and exports cleanly to CSV.

**Collection names.** The queries use `decision-manager-log` and `esa-log`, which are the staging names. Production names come from secrets (`MONGODB_COLLECTION_NAME_DM`, `MONGODB_COLLECTION_NAME_ESA`). If `db.getCollectionNames()` shows different names, replace them in `db.getCollection("…")` **and inside Q17's `$unionWith`**.

---

## 2. Time windows

The queries contain literal values for three windows. Literals are used because the Compass text editor doesn't evaluate expressions like `Date.now()`. DM stores `createdAt` as **epoch seconds (Int64)**; ESA stores it as a **Date**. That's why the same window appears in two forms.

| Window | IST range | DM `createdAt` (epoch seconds) | ESA `createdAt` (UTC Date) | Used by |
|---|---|---|---|---|
| **W60** | 2026-08-06 00:00 → 2026-10-05 00:00 | `1785954600` → `1791138600` | `2026-08-05T18:30:00Z` → `2026-10-04T18:30:00Z` | Q1–Q7, Q9–Q13 |
| **W7** | 2026-09-28 00:00 → 2026-10-05 00:00 | `1790533800` → `1791138600` | `2026-09-27T18:30:00Z` → `2026-10-04T18:30:00Z` | Q8, Q14, Q15, Q16 |
| **W1** | 2026-10-01 00:00 → 2026-10-02 00:00 | `1790793000` → `1790879400` | `2026-09-30T18:30:00Z` → `2026-10-01T18:30:00Z` | Q17 |

To shift a window, compute new values in the Compass shell:

```javascript
Math.floor(ISODate("2026-10-05T00:00:00+05:30").getTime() / 1000)   // -> 1791138600  (DM form)
ISODate("2026-10-05T00:00:00+05:30").toISOString()                   // -> 2026-10-04T18:30:00.000Z  (ESA form)
```

Two queries contain a constant tied to their window: **Q6 uses `86400` = minutes in 60 days**, and **Q17 uses `86400` = seconds in 1 day**. Update them if you change those windows.

---

## 3. Run these first (2 minutes)

```javascript
db.version()                 // 7.0+ -> use §4–6 as-is.  5.2–6.x -> §7 fallbacks.  Below 5.2 -> tell me.
db.getCollectionNames()      // confirm the two production collection names (§1)
db.getCollection("decision-manager-log").getIndexes()
db.getCollection("esa-log").getIndexes()
```

**Indexes:** every query filters on `createdAt`. If neither collection has an index starting with `createdAt`, each query is a full collection scan on the replica. That still works, but it's slow on large collections. **Do not create indexes on production yourself.** If they're missing, raise it with Infra (plan §10.1, E8) and use shorter windows until then.

**Q0b — confirm DM field names and types on real data.** Expected: `createdAt_type: "long"`, `start_type: "date"`, `partnername_lowercase` populated, `partnerName_camelcase: "ABSENT"`.

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $sort: { _id: -1 } },
  { $limit: 3 },
  { $project: {
      _id: 0,
      createdAt_type: { $type: "$createdAt" },
      triggerDecisionTime_type: { $type: "$triggerDecisionTime" },
      start_type: { $type: "$triggerDecisionStartTime" },
      stage: "$esaRequestBody.stage",
      partnername_lowercase: "$esaRequestBody.partnername",
      partnerName_camelcase: { $ifNull: ["$esaRequestBody.partnerName", "ABSENT"] }
  } }
])
```

**Q0c — confirm ESA field names and types.** Expected: `createdAt_type: "date"`, `timeTaken_type: "double"`, `total_type: "long"`, plus `serviceName`, `stage` and `workFlowId` populated.

```javascript
db.getCollection("esa-log").aggregate([
  { $sort: { _id: -1 } },
  { $limit: 3 },
  { $project: {
      _id: 0,
      createdAt_type: { $type: "$createdAt" },
      timeTaken_type: { $type: "$timeTaken" },
      total_type: { $type: "$totalExecutionTimeMs" },
      serviceName: 1, stage: 1, partnerName: 1, workFlowId: 1,
      fanOutRole: { $ifNull: ["$fanOutRole", "none"] },
      url_prefix: { $substrCP: [{ $ifNull: ["$request.url", ""] }, 0, 30] }
  } }
])
```

`workFlowId` is optional on the inbound request. If Q0c shows it **empty** on production documents, the request key falls back, in effect, to `createdAt` alone. That is still millisecond-precise, but two requests finishing in the same millisecond would merge, which slightly undercounts requests at very high throughput. Tell me if it's empty and I'll add a second key.

**If Q0b or Q0c differs from what's expected, stop and send me the output.** The queries below depend on these exact names and types.

**Then run Q17 as a timing probe.** It covers one day and is cheap. How long it takes tells you roughly how long the 60-day queries will take: about 60x for Q1–Q7 and Q9–Q13 without an index, much less with one.

---

## 4. Decision Manager queries

### Q1 — T_hold percentiles by stage × partner, with time attribution

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true }, triggerDecisionTime: { $gt: 0 } } },
  { $project: {
      stage: { $ifNull: ["$esaRequestBody.stage", "UNKNOWN"] },
      partner: { $ifNull: ["$esaRequestBody.partnername", "UNKNOWN"] },
      total: "$triggerDecisionTime",
      esa: { $ifNull: ["$esaExecutionTime", 0] },
      sf: { $ifNull: ["$sfCompositeExecutionTime", 0] }
  } },
  { $set: { dmOwn: { $max: [0, { $subtract: ["$total", { $add: ["$esa", "$sf"] }] }] } } },
  { $group: {
      _id: { stage: "$stage", partner: "$partner" },
      decisions: { $sum: 1 },
      pct: { $percentile: { input: "$total", p: [0.5, 0.9, 0.95, 0.99], method: "approximate" } },
      max_ms: { $max: "$total" },
      avg_total: { $avg: "$total" },
      p95_esa: { $percentile: { input: "$esa", p: [0.95], method: "approximate" } },
      p95_sf: { $percentile: { input: "$sf", p: [0.95], method: "approximate" } },
      p95_dmOwn: { $percentile: { input: "$dmOwn", p: [0.95], method: "approximate" } },
      avg_esa: { $avg: "$esa" },
      avg_sf: { $avg: "$sf" },
      avg_dmOwn: { $avg: "$dmOwn" }
  } },
  { $project: {
      _id: 0,
      stage: "$_id.stage",
      partner: "$_id.partner",
      decisions: 1,
      p50_ms: { $arrayElemAt: ["$pct", 0] },
      p90_ms: { $arrayElemAt: ["$pct", 1] },
      p95_ms: { $arrayElemAt: ["$pct", 2] },
      p99_ms: { $arrayElemAt: ["$pct", 3] },
      max_ms: 1,
      avg_ms: { $round: ["$avg_total", 0] },
      p95_esa_ms: { $arrayElemAt: ["$p95_esa", 0] },
      p95_sf_ms: { $arrayElemAt: ["$p95_sf", 0] },
      p95_dm_own_ms: { $arrayElemAt: ["$p95_dmOwn", 0] },
      esa_share_pct: { $round: [{ $multiply: [{ $divide: ["$avg_esa", "$avg_total"] }, 100] }, 1] },
      sf_share_pct: { $round: [{ $multiply: [{ $divide: ["$avg_sf", "$avg_total"] }, 100] }, 1] },
      dm_own_share_pct: { $round: [{ $multiply: [{ $divide: ["$avg_dmOwn", "$avg_total"] }, 100] }, 1] }
  } },
  { $sort: { decisions: -1 } }
], { allowDiskUse: true })
```

**`p95_ms / 1000` is `T_hold`** for `λ_pod = C_cap / T_hold`. Use p95, not the average: slot occupancy is driven by the slow tail. The `*_share_pct` columns show where the time goes. A high `esa_share_pct` means DM is mostly waiting on ESA, so tuning DM's own code buys little.

### Q2 — T_hold percentiles by stage only

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true }, triggerDecisionTime: { $gt: 0 } } },
  { $group: {
      _id: { $ifNull: ["$esaRequestBody.stage", "UNKNOWN"] },
      decisions: { $sum: 1 },
      pct: { $percentile: { input: "$triggerDecisionTime", p: [0.5, 0.9, 0.95, 0.99], method: "approximate" } },
      max_ms: { $max: "$triggerDecisionTime" }
  } },
  { $project: {
      _id: 0, stage: "$_id", decisions: 1,
      p50_ms: { $arrayElemAt: ["$pct", 0] }, p90_ms: { $arrayElemAt: ["$pct", 1] },
      p95_ms: { $arrayElemAt: ["$pct", 2] }, p99_ms: { $arrayElemAt: ["$pct", 3] }, max_ms: 1
  } },
  { $sort: { decisions: -1 } }
], { allowDiskUse: true })
```

Same as Q1, by stage only.

### Q3 — T_hold percentiles by partner only

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true }, triggerDecisionTime: { $gt: 0 } } },
  { $group: {
      _id: { $ifNull: ["$esaRequestBody.partnername", "UNKNOWN"] },
      decisions: { $sum: 1 },
      pct: { $percentile: { input: "$triggerDecisionTime", p: [0.5, 0.9, 0.95, 0.99], method: "approximate" } },
      max_ms: { $max: "$triggerDecisionTime" }
  } },
  { $project: {
      _id: 0, partner: "$_id", decisions: 1,
      p50_ms: { $arrayElemAt: ["$pct", 0] }, p90_ms: { $arrayElemAt: ["$pct", 1] },
      p95_ms: { $arrayElemAt: ["$pct", 2] }, p99_ms: { $arrayElemAt: ["$pct", 3] }, max_ms: 1
  } },
  { $sort: { decisions: -1 } }
], { allowDiskUse: true })
```

Same as Q1, by partner only.

### Q4 — 50 busiest minutes (peak arrival rate)

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true } } },
  { $group: { _id: { $subtract: ["$createdAt", { $mod: ["$createdAt", 60] }] }, decisions: { $sum: 1 } } },
  { $sort: { decisions: -1 } },
  { $limit: 50 },
  { $project: {
      _id: 0,
      minute_ist: { $dateToString: { date: { $toDate: { $multiply: ["$_id", 1000] } }, format: "%Y-%m-%d %H:%M", timezone: "Asia/Kolkata" } },
      decisions_per_min: "$decisions",
      decisions_per_sec: { $round: [{ $divide: ["$decisions", 60] }, 2] }
  } }
], { allowDiskUse: true })
```

The top row is the **busiest minute in the window: current `λ_peak`**. Festive projections are best expressed as a multiple of this.

### Q5 — weekly shape: average decisions per hour, by weekday and IST hour

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true } } },
  { $project: { d: { $toDate: { $multiply: ["$createdAt", 1000] } } } },
  { $group: {
      _id: {
        dow: { $dayOfWeek: { date: "$d", timezone: "Asia/Kolkata" } },
        hour: { $hour: { date: "$d", timezone: "Asia/Kolkata" } }
      },
      total: { $sum: 1 },
      days: { $addToSet: { $dateToString: { date: "$d", format: "%Y-%m-%d", timezone: "Asia/Kolkata" } } }
  } },
  { $project: {
      _id: 0,
      weekday: { $arrayElemAt: [["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"], "$_id.dow"] },
      hour_ist: "$_id.hour",
      total: 1,
      days_seen: { $size: "$days" },
      avg_per_hour: { $round: [{ $divide: ["$total", { $size: "$days" }] }, 1] },
      avg_per_sec: { $round: [{ $divide: ["$total", { $multiply: [{ $size: "$days" }, 3600] }] }, 3] },
      dow: "$_id.dow"
  } },
  { $sort: { dow: 1, hour_ist: 1 } },
  { $project: { dow: 0 } }
], { allowDiskUse: true })
```

Daily and weekly shape. Shows whether load is spiky or flat, which drives the burst margin and how far ahead of a peak the HPA must react.

### Q6 — peak-to-average ratio (sets burst margin)

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true } } },
  { $group: { _id: { $subtract: ["$createdAt", { $mod: ["$createdAt", 60] }] }, n: { $sum: 1 } } },
  { $group: {
      _id: null,
      active_minutes: { $sum: 1 },
      total_decisions: { $sum: "$n" },
      avg_per_active_min: { $avg: "$n" },
      pct: { $percentile: { input: "$n", p: [0.5, 0.95, 0.99], method: "approximate" } },
      max_per_min: { $max: "$n" }
  } },
  { $project: {
      _id: 0, active_minutes: 1, total_decisions: 1,
      avg_per_window_min: { $round: [{ $divide: ["$total_decisions", 86400] }, 2] },
      avg_per_active_min: { $round: ["$avg_per_active_min", 2] },
      p50_per_min: { $arrayElemAt: ["$pct", 0] },
      p95_per_min: { $arrayElemAt: ["$pct", 1] },
      p99_per_min: { $arrayElemAt: ["$pct", 2] },
      max_per_min: 1,
      peak_to_avg_ratio: { $round: [{ $divide: ["$max_per_min", { $divide: ["$total_decisions", 86400] }] }, 1] }
  } }
], { allowDiskUse: true })
```

`peak_to_avg_ratio` = busiest minute ÷ average minute across the whole window. Above about 5 means burst capacity, not average capacity, is what you size for, and T5 is the decisive test. **`86400` is the number of minutes in the 60-day window.** Change it if you change the window.

### Q7 — failure, incomplete and retry profile by stage × partner

Collection: `decision-manager-log` · Window: **W60**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true } } },
  { $group: {
      _id: { stage: "$esaRequestBody.stage", partner: "$esaRequestBody.partnername" },
      decisions: { $sum: 1 },
      incomplete: { $sum: { $cond: [{ $or: [
          { $lte: [{ $ifNull: ["$triggerDecisionTime", 0] }, 0] },
          { $lt: ["$triggerDecisionEndTime", ISODate("2000-01-01T00:00:00Z")] }
      ] }, 1, 0] } },
      esa_near_timeout: { $sum: { $cond: [{ $gte: [{ $ifNull: ["$esaExecutionTime", 0] }, 119000] }, 1, 0] } },
      with_sf_retry: { $sum: { $cond: [{ $gt: [{ $ifNull: ["$sfRetryCount", 0] }, 0] }, 1, 0] } },
      sf_retries: { $sum: { $ifNull: ["$sfRetryCount", 0] } }
  } },
  { $project: {
      _id: 0, stage: "$_id.stage", partner: "$_id.partner", decisions: 1,
      incomplete: 1,
      incomplete_pct: { $round: [{ $multiply: [{ $divide: ["$incomplete", "$decisions"] }, 100] }, 2] },
      esa_near_timeout: 1,
      sf_retry_pct: { $round: [{ $multiply: [{ $divide: ["$with_sf_retry", "$decisions"] }, 100] }, 2] },
      sf_retries: 1
  } },
  { $sort: { decisions: -1 } }
], { allowDiskUse: true })
```

`incomplete` counts decisions with no end time or zero duration (failed or aborted). `esa_near_timeout` counts ESA calls of 119s or more, i.e. close to the 120s ESA budget.

### Q8 — peak concurrent in-flight decisions per IST hour (fleet-wide)

Collection: `decision-manager-log` · Window: **W7**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: {
      createdAt: { $gte: 1790533800, $lt: 1791138600 },
      isDeleted: { $ne: true },
      triggerDecisionTime: { $gt: 0 },
      triggerDecisionStartTime: { $gt: ISODate("2000-01-01T00:00:00Z") },
      triggerDecisionEndTime: { $gt: ISODate("2000-01-01T00:00:00Z") }
  } },
  { $project: { _id: 0, ev: [ { t: "$triggerDecisionStartTime", d: 1 }, { t: "$triggerDecisionEndTime", d: -1 } ] } },
  { $unwind: "$ev" },
  { $replaceWith: "$ev" },
  { $setWindowFields: {
      sortBy: { t: 1, d: 1 },
      output: { inflight: { $sum: "$d", window: { documents: ["unbounded", "current"] } } }
  } },
  { $group: {
      _id: { $dateTrunc: { date: "$t", unit: "hour", timezone: "Asia/Kolkata" } },
      peak_inflight: { $max: "$inflight" },
      starts: { $sum: { $cond: [{ $eq: ["$d", 1] }, 1, 0] } }
  } },
  { $project: {
      _id: 0,
      hour_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:00", timezone: "Asia/Kolkata" } },
      peak_inflight: 1,
      decisions_started: "$starts"
  } },
  { $sort: { peak_inflight: -1 } },
  { $limit: 48 }
], { allowDiskUse: true })
```

**Peak number of decisions in flight at the same moment**, per hour, across the whole fleet. Divide by the replica count during that hour (Infra enquiry E2) to get **per-pod concurrency**, then compare with `max_concurrent_trigger_decisions`. This tells you whether the cap has ever been approached. Use 7-day windows: the query sorts every start and end event.

## 5. ESA queries

### Q9 — per-service latency by serviceName × stage

Collection: `esa-log` · Window: **W60**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true },
      fanOutRole: { $ne: "per_call" },
      timeTaken: { $gt: 0 }
  } },
  { $group: {
      _id: { serviceName: { $ifNull: ["$serviceName", "UNKNOWN"] }, stage: { $ifNull: ["$stage", "UNKNOWN"] } },
      calls: { $sum: 1 },
      pct: { $percentile: { input: "$timeTaken", p: [0.5, 0.9, 0.95, 0.99], method: "approximate" } },
      max_ms: { $max: "$timeTaken" },
      avg_ms: { $avg: "$timeTaken" },
      failed: { $sum: { $cond: [{ $eq: ["$status", "FAILED"] }, 1, 0] } },
      non_2xx: { $sum: { $cond: [{ $or: [
          { $lt: ["$response.statusCode", 200] }, { $gte: ["$response.statusCode", 300] }
      ] }, 1, 0] } }
  } },
  { $project: {
      _id: 0, serviceName: "$_id.serviceName", stage: "$_id.stage", calls: 1,
      p50_ms: { $arrayElemAt: ["$pct", 0] }, p90_ms: { $arrayElemAt: ["$pct", 1] },
      p95_ms: { $arrayElemAt: ["$pct", 2] }, p99_ms: { $arrayElemAt: ["$pct", 3] },
      max_ms: 1, avg_ms: { $round: ["$avg_ms", 0] },
      failed_pct: { $round: [{ $multiply: [{ $divide: ["$failed", "$calls"] }, 100] }, 2] },
      non_2xx_pct: { $round: [{ $multiply: [{ $divide: ["$non_2xx", "$calls"] }, 100] }, 2] }
  } },
  { $sort: { calls: -1 } }
], { allowDiskUse: true })
```

Per-service latency. Fan-out per-call documents are excluded so each service step is counted once.

### Q10 — sequence-level latency by stage × partner (one row per request)

Collection: `esa-log` · Window: **W60**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true }
  } },
  { $group: {
      _id: { wf: "$workFlowId", at: "$createdAt" },
      stage: { $first: "$stage" },
      partner: { $first: "$partnerName" },
      total_ms: { $first: "$totalExecutionTimeMs" },
      overall: { $first: "$overallStatus" },
      services: { $sum: { $cond: [{ $eq: ["$fanOutRole", "per_call"] }, 0, 1] } },
      sum_service_ms: { $sum: { $cond: [{ $eq: ["$fanOutRole", "per_call"] }, 0, "$timeTaken"] } }
  } },
  { $match: { total_ms: { $gt: 0 } } },
  { $group: {
      _id: { stage: "$stage", partner: "$partner" },
      requests: { $sum: 1 },
      pct: { $percentile: { input: "$total_ms", p: [0.5, 0.9, 0.95, 0.99], method: "approximate" } },
      max_ms: { $max: "$total_ms" },
      avg_total: { $avg: "$total_ms" },
      avg_sum_service: { $avg: "$sum_service_ms" },
      avg_services: { $avg: "$services" },
      max_services: { $max: "$services" },
      near_504: { $sum: { $cond: [{ $gte: ["$total_ms", 119000] }, 1, 0] } },
      failed: { $sum: { $cond: [{ $eq: ["$overall", "FAILED"] }, 1, 0] } }
  } },
  { $project: {
      _id: 0, stage: "$_id.stage", partner: "$_id.partner", requests: 1,
      p50_ms: { $arrayElemAt: ["$pct", 0] }, p90_ms: { $arrayElemAt: ["$pct", 1] },
      p95_ms: { $arrayElemAt: ["$pct", 2] }, p99_ms: { $arrayElemAt: ["$pct", 3] }, max_ms: 1,
      avg_services: { $round: ["$avg_services", 1] }, max_services: 1,
      parallelism_factor: { $round: [{ $divide: ["$avg_sum_service", "$avg_total"] }, 2] },
      near_504: 1,
      failed_pct: { $round: [{ $multiply: [{ $divide: ["$failed", "$requests"] }, 100] }, 2] }
  } },
  { $sort: { requests: -1 } }
], { allowDiskUse: true })
```

One row per ESA request (deduplicated on `workFlowId` + `createdAt`, which every document of a request shares). `parallelism_factor` = summed service time ÷ wall-clock time. Close to 1 means the sequence is effectively serial.

### Q11 — services per sequence, by stage (feeds memory and write model)

Collection: `esa-log` · Window: **W60**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true }
  } },
  { $group: {
      _id: { wf: "$workFlowId", at: "$createdAt" },
      stage: { $first: "$stage" },
      services: { $sum: { $cond: [{ $eq: ["$fanOutRole", "per_call"] }, 0, 1] } },
      docs: { $sum: 1 }
  } },
  { $group: {
      _id: "$stage",
      requests: { $sum: 1 },
      avg_services: { $avg: "$services" },
      pct: { $percentile: { input: "$services", p: [0.5, 0.95], method: "approximate" } },
      max_services: { $max: "$services" },
      avg_docs: { $avg: "$docs" },
      max_docs: { $max: "$docs" }
  } },
  { $project: {
      _id: 0, stage: "$_id", requests: 1,
      avg_services: { $round: ["$avg_services", 1] },
      p50_services: { $arrayElemAt: ["$pct", 0] },
      p95_services: { $arrayElemAt: ["$pct", 1] },
      max_services: 1,
      avg_docs_per_request: { $round: ["$avg_docs", 1] },
      max_docs_per_request: "$max_docs"
  } },
  { $sort: { requests: -1 } }
], { allowDiskUse: true })
```

`avg_services` is `S` in the capacity and memory model. `avg_docs_per_request` is the Mongo write multiplier on the ESA side; it includes fan-out per-call documents.

### Q12 — provider host latency and the per-pod ceiling from MaxConnsPerHost=25

Collection: `esa-log` · Window: **W60**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true },
      fanOutRole: { $ne: "aggregate" },
      timeTaken: { $gt: 0 },
      "request.url": { $regex: "^https?://" }
  } },
  { $project: {
      timeTaken: 1, serviceName: 1, status: 1,
      host: { $arrayElemAt: [{ $split: [{ $arrayElemAt: [{ $split: ["$request.url", "://"] }, 1] }, "/"] }, 0] }
  } },
  { $group: {
      _id: "$host",
      calls: { $sum: 1 },
      services: { $addToSet: "$serviceName" },
      pct: { $percentile: { input: "$timeTaken", p: [0.5, 0.95, 0.99], method: "approximate" } },
      avg_ms: { $avg: "$timeTaken" },
      failed: { $sum: { $cond: [{ $eq: ["$status", "FAILED"] }, 1, 0] } }
  } },
  { $project: {
      _id: 0, host: "$_id", calls: 1,
      services: { $reduce: { input: "$services", initialValue: "", in: { $concat: ["$$value", { $cond: [{ $eq: ["$$value", ""] }, "", ", "] }, "$$this"] } } },
      p50_ms: { $arrayElemAt: ["$pct", 0] },
      p95_ms: { $arrayElemAt: ["$pct", 1] },
      p99_ms: { $arrayElemAt: ["$pct", 2] },
      avg_ms: { $round: ["$avg_ms", 0] },
      failed_pct: { $round: [{ $multiply: [{ $divide: ["$failed", "$calls"] }, 100] }, 2] },
      pod_ceiling_calls_per_sec: { $round: [{ $divide: [25, { $divide: [{ $arrayElemAt: ["$pct", 1] }, 1000] }] }, 2] }
  } },
  { $sort: { calls: -1 } }
], { allowDiskUse: true })
```

Per provider host. **`pod_ceiling_calls_per_sec` = 25 ÷ p95 seconds**: the most calls per second one pod can make to that provider under `MaxConnsPerHost=25`. This is `λ_conn`. Fleet-wide, multiply by replicas, which is the figure to discuss with providers.

### Q13 — calls per request to each provider host

Collection: `esa-log` · Window: **W60**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true },
      fanOutRole: { $ne: "aggregate" },
      "request.url": { $regex: "^https?://" }
  } },
  { $project: {
      wf: "$workFlowId", at: "$createdAt",
      host: { $arrayElemAt: [{ $split: [{ $arrayElemAt: [{ $split: ["$request.url", "://"] }, 1] }, "/"] }, 0] }
  } },
  { $group: { _id: { wf: "$wf", at: "$at", host: "$host" }, calls: { $sum: 1 } } },
  { $group: {
      _id: "$_id.host",
      requests_using_host: { $sum: 1 },
      avg_calls_per_request: { $avg: "$calls" },
      max_calls_per_request: { $max: "$calls" }
  } },
  { $project: {
      _id: 0, host: "$_id", requests_using_host: 1,
      avg_calls_per_request: { $round: ["$avg_calls_per_request", 2] },
      max_calls_per_request: 1
  } },
  { $sort: { requests_using_host: -1 } }
], { allowDiskUse: true })
```

Multiply `avg_calls_per_request` by target decisions per second to get calls per second per provider at festive peak.

### Q14 — peak concurrent in-flight sequences per IST hour (fleet-wide)

Collection: `esa-log` · Window: **W7**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-09-27T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true },
      totalExecutionTimeMs: { $gt: 0 }
  } },
  { $group: { _id: { wf: "$workFlowId", at: "$createdAt" }, ms: { $first: "$totalExecutionTimeMs" } } },
  { $project: { _id: 0, ev: [
      { t: { $subtract: ["$_id.at", "$ms"] }, d: 1 },
      { t: "$_id.at", d: -1 }
  ] } },
  { $unwind: "$ev" },
  { $replaceWith: "$ev" },
  { $setWindowFields: {
      sortBy: { t: 1, d: 1 },
      output: { inflight: { $sum: "$d", window: { documents: ["unbounded", "current"] } } }
  } },
  { $group: {
      _id: { $dateTrunc: { date: "$t", unit: "hour", timezone: "Asia/Kolkata" } },
      peak_inflight: { $max: "$inflight" },
      starts: { $sum: { $cond: [{ $eq: ["$d", 1] }, 1, 0] } }
  } },
  { $project: {
      _id: 0,
      hour_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:00", timezone: "Asia/Kolkata" } },
      peak_inflight: 1,
      sequences_started: "$starts"
  } },
  { $sort: { peak_inflight: -1 } },
  { $limit: 48 }
], { allowDiskUse: true })
```

Same as Q8 for ESA. A sequence's interval is reconstructed as `[createdAt − totalExecutionTimeMs, createdAt]`. Divide by the ESA replica count for per-pod concurrency, and compare with the effective cap of 266.

## 6. Size, write rate and growth (for the Infra handover)

### Q15 — document size (sample of 20,000 from last 7 days)

Collection: `decision-manager-log` · Window: **W7**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1790533800, $lt: 1791138600 } } },
  { $sample: { size: 20000 } },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: {
      _id: null, sampled: { $sum: 1 }, avg: { $avg: "$sz" }, max: { $max: "$sz" },
      pct: { $percentile: { input: "$sz", p: [0.5, 0.95, 0.99], method: "approximate" } }
  } },
  { $project: {
      _id: 0, sampled: 1,
      avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] },
      p50_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 0] }, 1024] }, 1] },
      p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 1] }, 1024] }, 1] },
      p99_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 2] }, 1024] }, 1] },
      max_kb: { $round: [{ $divide: ["$max", 1024] }, 1] }
  } }
], { allowDiskUse: true })
```

`avg_kb` feeds Mongo storage growth: `λ × 86400 × docs_per_decision × avg_doc_bytes`.

### Q16 — document size (sample of 20,000 from last 7 days)

Collection: `esa-log` · Window: **W7**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: { createdAt: { $gte: ISODate("2026-09-27T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") } } },
  { $sample: { size: 20000 } },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: {
      _id: null, sampled: { $sum: 1 }, avg: { $avg: "$sz" }, max: { $max: "$sz" },
      pct: { $percentile: { input: "$sz", p: [0.5, 0.95, 0.99], method: "approximate" } }
  } },
  { $project: {
      _id: 0, sampled: 1,
      avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] },
      p50_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 0] }, 1024] }, 1] },
      p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 1] }, 1024] }, 1] },
      p99_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$pct", 2] }, 1024] }, 1] },
      max_kb: { $round: [{ $divide: ["$max", 1024] }, 1] }
  } }
], { allowDiskUse: true })
```

As Q15, for ESA documents.

### Q17 — Write multiplier — documents written per decision, and per-second rates (one day)

Collection: `esa-log` · Window: **W1**

```javascript
db.getCollection("esa-log").aggregate([
  { $match: { createdAt: { $gte: ISODate("2026-09-30T18:30:00Z"), $lt: ISODate("2026-10-01T18:30:00Z") }, isDeleted: { $ne: true } } },
  { $group: { _id: { wf: "$workFlowId", at: "$createdAt" }, docs: { $sum: 1 } } },
  { $group: { _id: null, esa_requests: { $sum: 1 }, esa_docs: { $sum: "$docs" } } },
  { $unionWith: {
      coll: "decision-manager-log",
      pipeline: [
        { $match: { createdAt: { $gte: 1790793000, $lt: 1790879400 }, isDeleted: { $ne: true } } },
        { $group: { _id: null, dm_docs: { $sum: 1 } } }
      ]
  } },
  { $group: {
      _id: null,
      esa_requests: { $max: "$esa_requests" },
      esa_docs: { $max: "$esa_docs" },
      dm_docs: { $max: "$dm_docs" }
  } },
  { $project: {
      _id: 0, dm_docs: 1, esa_requests: 1, esa_docs: 1,
      dm_docs_per_sec: { $round: [{ $divide: ["$dm_docs", 86400] }, 3] },
      esa_docs_per_sec: { $round: [{ $divide: ["$esa_docs", 86400] }, 3] },
      esa_docs_per_request: { $round: [{ $divide: ["$esa_docs", "$esa_requests"] }, 2] },
      total_docs_per_decision: { $round: [{ $divide: [{ $add: ["$dm_docs", "$esa_docs"] }, "$dm_docs"] }, 2] }
  } }
], { allowDiskUse: true })
```

`total_docs_per_decision` is the measured **`(1 + S)` write multiplier** to hand to Infra. **`86400` is the number of seconds in the one-day window.** Pick a busy day from Q4.

### Q18 — Collection size and growth footing (run once per collection)

Collection: `decision-manager-log` · Window: **none**

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $collStats: { storageStats: { scale: 1048576 } } },
  { $project: {
      _id: 0, ns: 1,
      docs: "$storageStats.count",
      data_mb: { $round: ["$storageStats.size", 0] },
      storage_mb: { $round: ["$storageStats.storageSize", 0] },
      index_mb: { $round: ["$storageStats.totalIndexSize", 0] },
      avg_doc_bytes: "$storageStats.avgObjSize",
      indexes: "$storageStats.nindexes"
  } }
], { allowDiskUse: true })
```

Collection size today. Run it twice: once on `decision-manager-log` and once with the collection name changed to `esa-log`.

## 7. Fallbacks if `db.version()` is below 7.0

`$percentile` needs MongoDB 7.0. On 5.2–6.x, these two replace Q2 and Q9 and give the same results (checked against `$percentile` on the test data; the p95 and p99 values matched). The pattern is the same for every other percentile query; tell me the version and I'll convert the rest. Below 5.2, tell me, because `$sortArray` isn't available either.

### Q2 fallback (5.2–6.x)

```javascript
db.getCollection("decision-manager-log").aggregate([
  { $match: { createdAt: { $gte: 1785954600, $lt: 1791138600 }, isDeleted: { $ne: true }, triggerDecisionTime: { $gt: 0 } } },
  { $group: { _id: { $ifNull: ["$esaRequestBody.stage", "UNKNOWN"] }, v: { $push: "$triggerDecisionTime" } } },
  { $set: { v: { $sortArray: { input: "$v", sortBy: 1 } }, n: { $size: "$v" } } },
  { $project: {
      _id: 0, stage: "$_id", decisions: "$n",
      p50_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.50] } }] }] },
      p90_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.90] } }] }] },
      p95_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.95] } }] }] },
      p99_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.99] } }] }] },
      max_ms: { $arrayElemAt: ["$v", -1] }
  } },
  { $sort: { decisions: -1 } }
], { allowDiskUse: true })
```

### Q9 fallback (5.2–6.x)

```javascript
db.getCollection("esa-log").aggregate([
  { $match: {
      createdAt: { $gte: ISODate("2026-08-05T18:30:00Z"), $lt: ISODate("2026-10-04T18:30:00Z") },
      isDeleted: { $ne: true }, fanOutRole: { $ne: "per_call" }, timeTaken: { $gt: 0 }
  } },
  { $group: {
      _id: { serviceName: { $ifNull: ["$serviceName", "UNKNOWN"] }, stage: { $ifNull: ["$stage", "UNKNOWN"] } },
      v: { $push: "$timeTaken" },
      failed: { $sum: { $cond: [{ $eq: ["$status", "FAILED"] }, 1, 0] } }
  } },
  { $set: { v: { $sortArray: { input: "$v", sortBy: 1 } }, n: { $size: "$v" } } },
  { $project: {
      _id: 0, serviceName: "$_id.serviceName", stage: "$_id.stage", calls: "$n",
      p50_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.50] } }] }] },
      p90_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.90] } }] }] },
      p95_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.95] } }] }] },
      p99_ms: { $arrayElemAt: ["$v", { $min: [{ $subtract: ["$n", 1] }, { $floor: { $multiply: ["$n", 0.99] } }] }] },
      max_ms: { $arrayElemAt: ["$v", -1] },
      failed_pct: { $round: [{ $multiply: [{ $divide: ["$failed", "$n"] }, 100] }, 2] }
  } },
  { $sort: { calls: -1 } }
], { allowDiskUse: true })
```

---

## 8. PostgreSQL

Production Postgres is reached through Infra (plan §10.1, enquiry E7), so it has no queries here. Run the following yourself **on staging** to establish the staging baseline. Infra can run the same statements, read-only, on production.

```sql
SHOW max_connections;
SHOW superuser_reserved_connections;

SELECT count(*)                                              AS total,
       count(*) FILTER (WHERE state = 'active')              AS active,
       count(*) FILTER (WHERE state = 'idle')                AS idle,
       count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_txn
FROM pg_stat_activity;

-- per client address, i.e. per pod
SELECT client_addr, usename, datname, count(*) AS conns,
       count(*) FILTER (WHERE state = 'active') AS active
FROM pg_stat_activity GROUP BY 1,2,3 ORDER BY conns DESC;

-- hottest statements (needs pg_stat_statements)
SELECT calls, round(total_exec_time::numeric,1) AS total_ms, round(mean_exec_time::numeric,2) AS mean_ms,
       left(regexp_replace(query,'\s+',' ','g'),130) AS query
FROM pg_stat_statements ORDER BY calls DESC LIMIT 25;

SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS total, n_live_tup, n_dead_tup, last_autovacuum
FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 20;
```

These Postgres statements were **not** executed during verification; only the Mongo pipelines were. They use standard catalog views, so they should run as written. `pg_stat_statements` may not be installed; if that query fails, skip it.

---

## 9. Output template

Fill this in from the results. It is the direct input to plan §8.2 (fleet sizing) and §8A (Infra handover).

| Quantity | From | Value |
|---|---|---|
| `T_hold` p95, worst stage/partner | Q1 | |
| `T_hold` p95, by stage | Q2 | |
| ESA / SF / DM-own share of decision time | Q1 | |
| Busiest minute, decisions/sec (**current `λ_peak`**) | Q4 | |
| Peak-to-average ratio | Q6 | |
| Incomplete %, ESA near-timeout count | Q7 | |
| Peak in-flight decisions, fleet-wide (÷ replicas = per pod) | Q8 + Infra E2 | |
| Slowest service p95 | Q9 | |
| Avg / p95 services per sequence (`S`) | Q11 | |
| Parallelism factor | Q10 | |
| Tightest `pod_ceiling_calls_per_sec` and its host | Q12 | |
| Calls per request, top provider | Q13 | |
| Peak in-flight ESA sequences (÷ replicas = per pod) | Q14 + Infra E2 | |
| Avg document size DM / ESA | Q15, Q16 | |
| Documents written per decision (`1 + S` measured) | Q17 | |
| Collection size DM / ESA | Q18 | |
