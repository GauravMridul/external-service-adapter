// ---- FAST BASELINE (v3) — paste blocks into the Compass MongoSH panel, in order. ----
// Window set-up: run once per session. var (not const) so you can re-run it with new dates.
var FROM14 = ObjectId.createFromTime(Math.floor(ISODate("2026-09-22T00:00:00+05:30").getTime() / 1000));
var TO     = ObjectId.createFromTime(Math.floor(ISODate("2026-10-06T00:00:00+05:30").getTime() / 1000));
var WIN_START = "2026-10-05T11:00:00+05:30";   // start of a busy period: set from M1's busiest hours
var HOURS     = 2;                              // M2-M5 window length. If a query hits MaxTimeMSExpired, halve this.
var W_FROM = ObjectId.createFromTime(Math.floor(ISODate(WIN_START).getTime() / 1000));
var W_TO   = ObjectId.createFromTime(Math.floor(ISODate(WIN_START).getTime() / 1000) + HOURS * 3600);
var OPTS   = { maxTimeMS: 60000, hint: { _id: 1 } };   // server stops the query after 60 s

// M0 — documents per IST day, both collections (reads only the _id index)
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateToString: { date: { $toDate: "$_id" }, format: "%Y-%m-%d", timezone: "Asia/Kolkata" } }, dm_docs: { $sum: 1 } } },
  { $project: { _id: 0, day_ist: "$_id", dm_docs: 1, weekday: { $arrayElemAt: [["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"], { $dayOfWeek: { date: { $dateFromString: { dateString: "$_id" } } } }] } } },
  { $sort: { day_ist: 1 } }
], OPTS)
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateToString: { date: { $toDate: "$_id" }, format: "%Y-%m-%d", timezone: "Asia/Kolkata" } }, esa_docs: { $sum: 1 } } },
  { $project: { _id: 0, day_ist: "$_id", esa_docs: 1, weekday: { $arrayElemAt: [["", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"], { $dayOfWeek: { date: { $dateFromString: { dateString: "$_id" } } } }] } } },
  { $sort: { day_ist: 1 } }
], OPTS)

// M1 — 20 busiest minutes and 10 busiest hours of DM decisions, last 14 days (reads only the _id index)
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateTrunc: { date: { $toDate: "$_id" }, unit: "minute", timezone: "Asia/Kolkata" } }, n: { $sum: 1 } } },
  { $sort: { n: -1 } }, { $limit: 20 },
  { $project: { _id: 0, minute_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:%M", timezone: "Asia/Kolkata" } }, decisions: "$n", per_sec: { $round: [{ $divide: ["$n", 60] }, 2] } } }
], OPTS)
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: FROM14, $lt: TO } } },
  { $group: { _id: { $dateTrunc: { date: { $toDate: "$_id" }, unit: "hour", timezone: "Asia/Kolkata" } }, n: { $sum: 1 } } },
  { $sort: { n: -1 } }, { $limit: 10 },
  { $project: { _id: 0, hour_ist: { $dateToString: { date: "$_id", format: "%Y-%m-%d %H:00", timezone: "Asia/Kolkata" } }, decisions: "$n", per_sec: { $round: [{ $divide: ["$n", 3600] }, 3] } } }
], OPTS)

// M2 — DM decision time (T_hold) by stage x partner, for the window
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

// M3 — ESA per-service latency by service x stage, for the window (fan-out per-call docs excluded)
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

// M4 — document size: first 2,000 documents of the window per collection (reads only those 2,000)
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO } } }, { $limit: 2000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], OPTS)
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: W_FROM, $lt: W_TO } } }, { $limit: 2000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], OPTS)

// M5 (optional) — peak decisions in flight at once, per IST hour, for the window (fleet-wide)
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
