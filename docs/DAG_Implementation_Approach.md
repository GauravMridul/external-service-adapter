# DAG Implementation in ESA: Approaches, Pros and Cons

This document outlines approaches and trade-offs for implementing a Directed Acyclic Graph (DAG) execution model in the External Service Adapter (ESA), and clarifies why it does not apply to the Decision Manager (DM) to any significant extent.

---

## 1. Applicability: ESA vs Decision Manager

### 1.1 Decision Manager (DM)

**DM has a single linear pipeline.** One request goes through a fixed sequence of steps in order:

- metadata_fetched → esa_invocation_done → esa_response_transformed → sfdc_field_mapping_fetched → sf_composite_request_built → sf_composite_executed → callback_sent → exception_log_initiated → mongo_log_initiated

There are no parallel branches, no “sequence of groups,” and no alternative orderings. Each step depends on the previous one. **A DAG does not add value here**—the flow is already a chain, and there is nothing to reorder or parallelize at the graph level. DAG is **not applicable to DM** to any meaningful extent.

### 1.2 External Service Adapter (ESA)

**ESA executes a configurable sequence of service groups.** Today:

- Groups are run **strictly in order** (G0 → G1 → G2 → …).
- Within each group, services run in parallel (with caps).
- Dependencies are implicit: “group i depends on all previous groups.”

A **DAG model is relevant only for ESA**, where:

- Different groups might depend on different subsets of earlier groups.
- Independent branches could run in parallel (e.g. G2 and G3 both waiting only on G0 and G1), reducing end-to-end latency.

The rest of this document focuses on **ESA only**.

---

## 2. Current ESA Model (Baseline)

- **Structure:** `SequenceString` → list of groups, e.g. `"1,2;3;4,5"` → `[[1,2], [3], [4,5]]`.
- **Execution:** One loop over groups; within each group, services run in parallel (semaphore-limited).
- **Dependency:** Implicit: group order = full dependency (each group waits for all previous groups).
- **Control flow:** Terminate sequence (EXIT) and skip-service conditions are supported; remaining groups or services are marked SKIPPED.

**Limitation:** If group 2 only needs group 0 (not group 1), we still run G0 → G1 → G2, so G2 waits unnecessarily. A DAG would allow G2 to run as soon as G0 is done (e.g. in parallel with G1).

---

## 3. Approaches to Implementing a DAG in ESA

### 3.1 Approach A: Explicit Dependencies in Config

**Idea:** Configuration explicitly declares which nodes (services or groups) each node depends on.

**Mechanics:**

- Extend sequence or service config with a dependency model, e.g.:
  - Per service: `depends_on: [serviceId1, serviceId2]`, or
  - Per group: `group_depends_on: [groupIndex0, groupIndex1]`.
- At load time (or once per sequence): build a graph (nodes = services or groups, edges = dependencies). Validate DAG (no cycles).
- At runtime: topological sort (or maintain a “ready” set). Run a node when all its predecessors have completed. Apply existing caps (e.g. max concurrent per group, fan-out limits) and EXIT/skip semantics.

**Pros:**

- Clear, explicit semantics; no guesswork.
- Full control over parallelism and ordering.
- Easy to reason about and debug.
- Graph build + topo sort is O(V+E); negligible CPU overhead.

**Cons:**

- Config authors must keep dependencies correct and in sync with expressions.
- More config surface and possible mistakes (e.g. missing or wrong dependency).
- Migration: existing sequences have no explicit deps; need a strategy (e.g. default to “depend on all previous groups” to preserve current behavior).

---

### 3.2 Approach B: Inferred Dependencies from Expressions

**Idea:** Parse request templates (URL, headers, body) for placeholders that reference other services’ outputs (e.g. `${service_3.response.body.score}`). Infer: “this service depends on service 3.”

**Mechanics:**

- When loading sequence/service config: for each service, scan its templates and collect referenced service IDs (static analysis; no request data needed).
- Build dependency graph: edge from each referenced service to this service.
- Add “data fetch” (e.g. Salesforce) as a root node if needed.
- Cache the graph per sequence/config. At runtime, use it like Approach A (topo sort, run when predecessors done).

**Pros:**

- No extra fields in config for deps; inferred from existing templates.
- Stays in sync with actual data usage (if templates are the only source of references).
- One-time inference at config load; minimal runtime overhead.

**Cons:**

- Dynamic or indirect references (e.g. variable service ID) may be missed or over-approximated.
- Requires a clear placeholder convention and robust parsing.
- If inference is wrong, execution order can be wrong (e.g. missing dep → race; extra dep → unnecessary wait). Harder to debug than explicit deps.
- Edge cases: optional placeholders, conditional blocks, nested structures.

---

### 3.3 Approach C: Hybrid (Explicit Override + Inference Fallback)

**Idea:** Infer dependencies from expressions by default; allow optional explicit `depends_on` to override or extend.

**Mechanics:**

- Run inference (Approach B) to get a default graph.
- If config specifies `depends_on` for a service, use it (replace or merge with inferred deps, per policy).
- Validate final graph is a DAG; then execute as in A.

**Pros:**

- Good default for simple cases (inference); explicit override for complex or subtle cases.
- Backward compatibility: existing sequences get inferred deps; can migrate to explicit where needed.

**Cons:**

- Two mechanisms to maintain and document; possible confusion when explicit and inferred disagree.
- Need a clear policy (e.g. “explicit overrides inferred” or “union of both”) and validation.

---

### 3.4 Approach D: Keep Groups, Add Inter-Group Edges (Minimal Change)

**Idea:** Keep the current “list of groups” and within-group parallelism. Add optional edges only **between groups** (e.g. “group 2 depends only on group 0”), not per-service.

**Mechanics:**

- Sequence format gains optional dependency info, e.g. `group_deps: [{group: 2, depends_on: [0]}, ...]`. Default: group i depends on [0..i-1] (current behavior).
- Build a group-level DAG. Execution: when all of a group’s predecessor groups are done, run that group (services inside the group still in parallel).
- EXIT/skip: when a group triggers EXIT, do not start any downstream groups (same idea as today).

**Pros:**

- Smaller change to existing model; groups remain the unit of parallelism and config.
- Fewer nodes and edges than full per-service DAG; simpler to configure and debug.
- Still allows latency gain when “group 3 only needs group 1” etc.

**Cons:**

- Less granular than per-service DAG; if one service in group 2 only needs one service in group 0, the whole group 2 still waits for the whole group 1 if group 2 declares dependency on group 1.
- Dependency is at group level only; cannot express “service A in group 2 needs only service X in group 0.”

---

## 4. Sequence Terminating Conditions (EXIT) and Pre/Post Execution in a DAG

This section describes how to preserve current EXIT and pre/post execution semantics in a DAG execution model, and how to make behavior **more optimal** than the existing linear-group implementation.

### 4.1 Current behavior (baseline)

Config keys **PreExecution** and **PostExecution** are canonical; **pre_execution** and **post_execution** are also accepted.

- **PreExecution:** Before invoking a service, expressions are evaluated. Decision can be:
  - **CONTINUE** – Skip this service (no HTTP call); optionally set response from `pick_response_from` (e.g. Salesforce data); node is considered “done” for the sequence.
  - **EXIT** – Terminate the sequence; set `exitTriggered`; all remaining groups and any PENDING services in the same group are marked SKIPPED.
  - **EXECUTE** – Run the service (HTTP call), then evaluate PostExecution.
- **PostExecution:** After the service completes, expressions are evaluated. Same three decisions. **EXIT** sets status `COMPLETED_WITH_EXIT` and triggers sequence termination (same “skip all remaining” behavior).
- **EXIT scope today:** “All remaining groups” and “any not-yet-started services in the current group.” So EXIT is **global**: everything after the EXIT-ing node (in group order) is skipped.

### 4.2 Handling EXIT (sequence termination) in a DAG

**Idea:** Treat EXIT as “do not start any node that is **downstream** of the EXIT-ing node in the graph.”

**Mechanics:**

- **Downstream set:** At graph build time (or load time), for each node N compute the set of nodes reachable from N along edges (descendants). Store as `downstream(N)` or as a reverse reachability structure.
- **When a node produces EXIT (pre or post):**
  1. Set a shared “sequence terminating” flag (or record the EXIT-ing node).
  2. **Cancel/skip** every node that is in `downstream(EXIT-ing node)` and has **not yet started**. Mark them SKIPPED (same status as today).
  3. Nodes that are **already in-flight** can either (a) be allowed to finish (recommended for simplicity and consistent response shape), or (b) be cancelled if the implementation supports it. Allowing in-flight to finish matches current “we don’t abort mid-request” behavior.
- **No new nodes:** After EXIT, the scheduler simply never marks any node in the downstream set as “ready” to run (or checks “if EXIT and node in downstream set → skip”). So EXIT is **scoped to the graph**: only descendants are skipped.

**Two EXIT semantics (product choice):**

| Option | Scope of EXIT | Behavior | Performance |
|--------|----------------|----------|-------------|
| **A. Descendants only** | Only nodes that are **downstream** of the EXIT-ing node in the DAG | Independent branches (not descendants) continue to run. | **More optimal:** Other branches can complete; total time = max of (EXIT branch, other branches). |
| **B. Global (current-like)** | All nodes that have **not yet started** when EXIT fires | Same as today: “everything after” in a global sense. | Conservative; no behavior change from current; may skip work that could have run. |

**Recommendation:** Default to **Option A (descendants only)** for the DAG design so that EXIT gives **better performance** when there are independent branches: only the branch that triggered EXIT and its descendants stop; other branches finish. If product requires “EXIT = stop everything,” implement Option B (e.g. “EXIT cancels all not-yet-started nodes”) as a configurable or legacy mode.

### 4.3 Pre-execution and post-execution in a DAG (unchanged semantics, same or better performance)

**PreExecution** and **PostExecution** are **per-node** today; they remain per-node in a DAG. No change to config or expression semantics.

**PreExecution in DAG:**

- A node becomes **runnable** when all its predecessor nodes have completed (same as today: “all deps satisfied”).
- **Before** starting the node’s HTTP call (or fan-out), run **PreExecution** for that node (same `processPreExecution` / expression evaluation as today).
  - **CONTINUE:** Mark node as SKIPPED; do not run the service; optionally apply `pick_response_from`; **mark node as completed** for dependency purposes so that any successor nodes can become runnable. No HTTP call → **same or better performance** (we may have run this node earlier in the DAG than in “group order,” so we skip work earlier).
  - **EXIT:** Mark node as TERMINATED (or COMPLETED_WITH_EXIT for post); set EXIT flag; skip all downstream nodes as in §4.2. No further nodes started on that branch.
  - **EXECUTE:** Run the service (HTTP or fan-out); then run PostExecution.

**PostExecution in DAG:**

- **After** the node’s HTTP call (or fan-out) completes, run **PostExecution** for that node (same `processPostExecution` as today).
  - **CONTINUE / EXECUTE:** Mark node COMPLETED; successors can run.
  - **EXIT:** Set node status to COMPLETED_WITH_EXIT; set EXIT flag; skip all downstream nodes as in §4.2.

**Placeholder and data visibility:** PreExecution and PostExecution expressions today can reference `masterDTO` (e.g. other services’ responses, Salesforce data). In a DAG, a node’s predecessors are exactly the nodes whose outputs are already available. So **data visibility is correct**: when a node runs (or its pre-execution runs), all dependency nodes have completed and their outputs are in `masterDTO`. No change needed to expression evaluation order.

**Performance:**

- **Same semantics:** Pre/post decisions (CONTINUE, EXIT, EXECUTE) behave the same at each node.
- **More optimal where:**
  1. **EXIT scoped to descendants (Option A):** Independent branches complete instead of being skipped → less wasted work and often lower end-to-end latency.
  2. **Earlier skip:** In a DAG we may run nodes in a different order (e.g. run a node as soon as its deps are done). If that node’s PreExecution returns CONTINUE, we skip it without ever running its HTTP call, and we may have done that earlier than in the linear group order → same or better CPU/IO.
  3. **No “group barrier”:** Today we wait for the whole group before starting the next group. In a DAG we start a node as soon as its predecessors are done, so we don’t add artificial delay at group boundaries.

### 4.4 Summary: conditions and performance

| Feature | Current (linear groups) | DAG (proposed) | Performance / behavior |
|---------|--------------------------|----------------|-------------------------|
| **EXIT** | Skip all remaining groups + PENDING in current group | Skip only **downstream** nodes (Option A) or all not-started (Option B) | Option A: **more optimal** (other branches finish). Option B: same as today. |
| **PreExecution** | Before each service; CONTINUE / EXIT / EXECUTE | Same: before each node; same three decisions | **Maintained;** can be more optimal (earlier skip, no group barrier). |
| **PostExecution** | After each service; same decisions | Same: after each node | **Maintained;** EXIT applies to downstream only (Option A) or global (Option B). |
| **Skip (CONTINUE)** | Node skipped; successors still run (next group) | Node skipped; node marked done; **successors run when their deps are done** (may be earlier than in linear order) | **More optimal:** independent successors can run without waiting for rest of “group.” |
| **Data for pre/post** | masterDTO has all prior groups’ outputs | masterDTO has all **predecessor** nodes’ outputs when node runs | **Correct:** DAG dependencies match data visibility. |

### 4.5 Single root / gate service: one service first, then run or terminate based on response

**Scenario:** One service runs at the start; based on its response, other services may run or the sequence may terminate (no further work).

**How the DAG handles it (no special case needed):**

- **Graph shape:** Model the gate as a **root node** (no dependencies, or only “data fetch” as predecessor). All other services that “depend on the gate’s response” have a **dependency on that root node**.
- **Execution:** The root runs first (it’s the only runnable node initially). When it completes, its response is in `masterDTO`. All nodes that depend only on the root become runnable.
- **Run or terminate:** For each of those successor nodes, **PreExecution** runs before the HTTP call. PreExecution expressions can reference the root service’s response (e.g. `((GateService.response.body.eligible))`). So you can:
  - Return **EXIT** if the gate says “stop” → downstream nodes are skipped (per §4.2).
  - Return **CONTINUE** to skip a specific successor (e.g. “don’t run this service for this request”).
  - Return **EXECUTE** to run the service.
- **PostExecution on the root:** Alternatively, the **root service** can have **PostExecution** that returns EXIT when its response indicates “terminate.” Then all nodes that depend on the root are downstream of the root → they are all skipped when EXIT fires. So “gate says stop” = root’s PostExecution returns EXIT; no other services run.

**Summary:** One service at start = one root node in the DAG. “Based on response, others may run or terminate” = PreExecution (or root’s PostExecution) on the successors (or root) that read that response and return EXECUTE / CONTINUE / EXIT. No new primitives; existing dependency + pre/post is enough. The configuration only needs to declare that the gate is the sole dependency for the “others” (via explicit `depends_on` or inferred from placeholders).

---

## 5. Configuration Changes: Dependency Column and Sequence String

Two aspects: (1) where to store dependencies (e.g. a new column in service_configuration), and (2) how the existing sequence string (`,` and `;`) behaves and whether it changes.

### 5.1 Storing dependencies (service_configuration)

**Straightforward option: new column (or JSON field) in service_configuration.**

- **Column name (example):** `depends_on` or `dependency_service_ids`.
- **Format:** List of service IDs that this service depends on. Examples:
  - `depends_on: [1, 2]` (service depends on services 1 and 2)
  - `depends_on: []` or null/absent = “depend on all previous groups” (current default) or “infer from placeholders”
- **Scope:** Per-service. So each row in service_configuration can optionally specify which other services must complete before this one runs.
- **Validation:** At load time, ensure every ID in `depends_on` is a valid service in the sequence and that the resulting graph has no cycles (DAG).

**Alternative:** Store at group level (e.g. “group 2 depends on groups [0, 1]”) if using group-level DAG (Approach D). Then the column might live on a separate “sequence config” or “group config” rather than per-service.

### 5.2 Sequence string: current logic and how it can change

**Current logic:**

- **`;` (semicolon)** = group separator. Order of groups is the execution order. So `"1,2;3;4,5"` → group 0 = [1,2], group 1 = [3], group 2 = [4,5].
- **`,` (comma)** = within a group = services run in parallel (same group). So `1,2` means “services 1 and 2 in parallel in this group.”

**Default dependency today:** Group *i* depends on “all previous groups” (0..*i*−1). So the sequence string **implies** a linear chain of groups; no explicit dependency config.

**Ways to handle this with a DAG:**

| Approach | Sequence string | Dependency | How it looks / behaves |
|----------|------------------|------------|-------------------------|
| **A. Keep string; add column only** | **Unchanged.** Still `"1,2;3;4,5"`. `;` = group, `,` = parallel within group. | **New column** `depends_on` per service (or per group). If absent → **default**: “this service (or group) depends on all previous groups” so current behavior is preserved. If present → override: e.g. service 4 has `depends_on: [1]` so group 2 runs as soon as group 0 is done (no wait for group 1). | **Minimal change.** Parsing of `,` and `;` stays the same. Graph is built from string (groups + order) plus overrides from the new column. Backward compatible. |
| **B. Optional new format for explicit DAG** | **New optional format** when you want to express dependencies in the string. Example: `1 \| 2,3 \| 1 \| 4,5 \| 2,3` meaning “layer 0: service 1; layer 1: services 2,3 depend on 1; layer 2: services 4,5 depend on 2,3.” Or: `1; 2,3 <- 1; 4,5 <- 2,3` (“2,3 depend on 1; 4,5 depend on 2,3”). | Dependencies come **from the string** (e.g. `<- 1` or “layer” ordering). Column can still override or add. | **Larger change.** New parser; `,` still “parallel,” but `;` plus `<-` or “layer” defines edges. Use when you want sequence string to be self-contained for simple DAGs. |
| **C. Hybrid (recommended)** | **Keep current string** as default. Same `,` and `;` semantics. | **New column** for overrides. Default when column absent = “depend on all previous groups” (current behavior). Optional: in future, allow a **second format** of sequence string (e.g. with `<-` or JSON) for flows that need explicit DAG in the string. | **Backward compatible;** no change to existing sequences. New flows can add `depends_on` in service_configuration. String logic of `,` and `;` **does not change** unless you introduce an optional new format. |

**Recommendation:** Use **Approach A or C.** Keep the current sequence string and the meaning of `,` and `;`. Add a **dependency column** (or JSON field) in service_configuration. Default = “depend on all previous groups” so that existing sequences behave as today. When `depends_on` is set, the graph is built from the string (groups + order) plus these overrides; the scheduler runs nodes when their dependencies (explicit or default) are satisfied. No change to the sequence string logic for existing or most new flows.

**Summary:**

- **New column:** e.g. `depends_on` (array of service IDs) per service. Straightforward.
- **Sequence string:** Keep `,` (parallel within group) and `;` (next group) as they are. Dependencies default from “group order”; column overrides when present. Optional later: a second string format for explicit DAG in the string if needed.

---

## 6. Pros and Cons of Implementing a DAG in ESA (Summary)

| Aspect | Pros | Cons |
|--------|------|------|
| **Latency** | Can run independent branches in parallel; total time can be “longest path” instead of “sum of all groups.” | Gain only when sequences have real independent branches; linear sequences see no benefit. |
| **Correctness** | Same outcomes if dependency model matches actual data flow; EXIT/skip can be preserved. | Wrong deps (explicit or inferred) can cause wrong order, races, or unnecessary waits. |
| **Complexity** | More expressive execution model. | Heavier execution engine (topo sort, ready set, per-node completion), more code paths and tests. |
| **Config / API** | Explicit or hybrid gives clear control. | New or extended config; migration path for existing sequences. |
| **Operations** | Better observability possible (e.g. “which nodes ran in parallel”). | New failure modes (e.g. deadlock if cycle slips through), need validation and monitoring. |
| **Overhead** | Graph build + topo at load time is cheap; inference once at config load is acceptable. | Per-request inference would add cost; avoid by caching. |

---

## 7. When a DAG Is Worth It (ESA)

**Worth considering if:**

- Many sequences have **independent branches** (e.g. group 2 only needs group 0; group 3 only needs group 1), and latency is a concern.
- You are willing to invest in **dependency representation** (explicit or inferred), **validation** (cycle detection, tests), and **operational clarity** (docs, runbooks).

**Lower priority if:**

- Most sequences are effectively **linear** (each group depends on all previous).
- Current latency is acceptable and the main pain is not “waiting for unnecessary groups.”
- Team size or roadmap favors **stability over new execution model**.

---

## 8. Recommendation Summary

- **Decision Manager:** DAG is **not applicable** to any significant extent; DM’s pipeline is a single linear chain. No need to consider DAG for DM.
- **ESA:** DAG is **applicable** where sequences have independent branches and you want lower end-to-end latency. Among the approaches:
  - **Explicit dependencies (A)** or **group-level DAG (D)** give the clearest control and smallest risk of wrong inference.
  - **Inferred (B)** or **hybrid (C)** reduce config burden but need robust parsing and clear semantics.
- **Overhead:** Keep dependency computation (explicit read or inference) **at config/load time** and reuse the graph at runtime so that processing overhead remains small.
- **EXIT and pre/post execution:** Sequence terminating conditions (EXIT) and pre/post execution (CONTINUE, EXECUTE, EXIT) are preserved per node. For **more optimal performance**, scope EXIT to **descendants only** (Option A) so independent branches can complete; optionally support global EXIT (Option B) for backward compatibility.
- **Gate / single-root scenario:** One service at start with “run or terminate based on response” is modeled as a root node; others depend on it and use PreExecution (or root’s PostExecution) to EXIT or run. No new primitives.
- **Configuration:** Add a `depends_on` (or equivalent) column in service_configuration for explicit dependencies. **Keep the current sequence string** and the meaning of `,` (parallel within group) and `;` (next group); default dependency = “all previous groups.” Override via column for DAG; optional later: a second string format for explicit DAG in the string.

This document is intended to support architecture and product decisions; it does not prescribe a specific implementation.
