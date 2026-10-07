// Self-contained fast baseline (no set-up block). Verified on MongoDB 8.0.15. Statements separated by blank lines.
// Edit the ISODate strings to move the window. M3 uses 30 minutes because esa-log is dense.

db.getCollection("esa-log").countDocuments(
  { _id: { $gte: ObjectId("6ac29b280000000000000000"), $lt: ObjectId("6ac3eca80000000000000000") } },
  { maxTimeMS: 60000, hint: { _id: 1 } })

db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:00:00+05:30").getTime() / 1000)),
                     $lt:  ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T12:00:00+05:30").getTime() / 1000)) },
              triggerDecisionTime: { $gt: 0 } } },
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
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:00:00+05:30").getTime() / 1000)),
                     $lt:  ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:30:00+05:30").getTime() / 1000)) },
              fanOutRole: { $ne: "per_call" } } },
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
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:00:00+05:30").getTime() / 1000)),
                     $lt:  ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T12:00:00+05:30").getTime() / 1000)) } } },
  { $limit: 1000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:00:00+05:30").getTime() / 1000)),
                     $lt:  ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T12:00:00+05:30").getTime() / 1000)) } } },
  { $limit: 1000 },
  { $project: { sz: { $bsonSize: "$$ROOT" } } },
  { $group: { _id: null, n: { $sum: 1 }, avg: { $avg: "$sz" }, mx: { $max: "$sz" }, p: { $percentile: { input: "$sz", p: [0.95], method: "approximate" } } } },
  { $project: { _id: 0, sampled: "$n", avg_kb: { $round: [{ $divide: ["$avg", 1024] }, 1] }, p95_kb: { $round: [{ $divide: [{ $arrayElemAt: ["$p", 0] }, 1024] }, 1] }, max_kb: { $round: [{ $divide: ["$mx", 1024] }, 1] } } }
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T11:00:00+05:30").getTime() / 1000)),
                     $lt:  ObjectId.createFromTime(Math.floor(ISODate("2026-10-05T12:00:00+05:30").getTime() / 1000)) },
              triggerDecisionTime: { $gt: 0 } } },
  { $project: { _id: 0, ev: [ { t: "$triggerDecisionStartTime", d: 1 }, { t: "$triggerDecisionEndTime", d: -1 } ] } },
  { $unwind: "$ev" }, { $replaceWith: "$ev" },
  { $match: { t: { $gt: ISODate("2000-01-01T00:00:00Z") } } },
  { $setWindowFields: { sortBy: { t: 1, d: 1 }, output: { inflight: { $sum: "$d", window: { documents: ["unbounded", "current"] } } } } },
  { $group: { _id: null, peak_inflight: { $max: "$inflight" } } },
  { $project: { _id: 0, peak_inflight: 1 } }
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

// ---- SAMPLED versions: use when a window is too dense to read in full. Verified on MongoDB 8.0.15. ----
// The window scan reads only the _id index; only the sampled documents are fetched, via $lookup on _id.
// Window: 5 Oct 2026 19:00-20:00 IST (peak hour).

// S2 — DM decision time by stage x partner, 800 random decisions from the window
db.getCollection("decision-manager-log").aggregate([
  { $match: { _id: { $gte: ObjectId("6ac3a6580000000000000000"), $lt: ObjectId("6ac3b4680000000000000000") } } },
  { $project: { _id: 1 } },
  { $sample: { size: 800 } },
  { $lookup: { from: "decision-manager-log", localField: "_id", foreignField: "_id", as: "d", pipeline: [
      { $project: { _id: 0, stage: "$esaRequestBody.stage", partner: "$esaRequestBody.partnername",
          total: "$triggerDecisionTime", esa: { $ifNull: ["$esaExecutionTime", 0] }, sf: { $ifNull: ["$sfCompositeExecutionTime", 0] } } } ] } },
  { $unwind: "$d" }, { $replaceWith: "$d" },
  { $match: { total: { $gt: 0 } } },
  { $group: {
      _id: { stage: "$stage", partner: "$partner" }, sampled: { $sum: 1 }, avg: { $avg: "$total" },
      t: { $percentile: { input: "$total", p: [0.5, 0.95, 0.99], method: "approximate" } }, t_max: { $max: "$total" },
      e: { $percentile: { input: "$esa", p: [0.5, 0.95], method: "approximate" } },
      s: { $percentile: { input: "$sf", p: [0.5, 0.95], method: "approximate" } } } },
  { $project: { _id: 0, stage: "$_id.stage", partner: "$_id.partner", sampled: 1, avg_ms: { $round: ["$avg", 0] },
      p50_ms: { $arrayElemAt: ["$t", 0] }, p95_ms: { $arrayElemAt: ["$t", 1] }, p99_ms: { $arrayElemAt: ["$t", 2] }, max_ms: "$t_max",
      esa_p50: { $arrayElemAt: ["$e", 0] }, esa_p95: { $arrayElemAt: ["$e", 1] },
      sf_p50: { $arrayElemAt: ["$s", 0] }, sf_p95: { $arrayElemAt: ["$s", 1] } } },
  { $sort: { stage: 1, sampled: -1 } }
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()

// S3 — ESA per-service latency, 6,000 random documents from the window
db.getCollection("esa-log").aggregate([
  { $match: { _id: { $gte: ObjectId("6ac3a6580000000000000000"), $lt: ObjectId("6ac3b4680000000000000000") } } },
  { $project: { _id: 1 } },
  { $sample: { size: 6000 } },
  { $lookup: { from: "esa-log", localField: "_id", foreignField: "_id", as: "d", pipeline: [
      { $project: { _id: 0, serviceName: 1, stage: 1, timeTaken: 1, status: 1, fanOutRole: 1 } } ] } },
  { $unwind: "$d" }, { $replaceWith: "$d" },
  { $match: { fanOutRole: { $ne: "per_call" } } },
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
], { maxTimeMS: 60000, hint: { _id: 1 } }).toArray()
