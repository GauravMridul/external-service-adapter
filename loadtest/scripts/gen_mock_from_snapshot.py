#!/usr/bin/env python3
"""Builds production-shaped mock sequences for STAGING load tests from the production DB snapshot.

Reads the snapshot CSVs (partner_service_mapping, service_configuration) and, optionally, the
per-service latencies from Mongo query M3. For each production sequence you choose to clone it
emits:

  mock-hosts.yaml   one k8s Service per REAL provider host, all pointing at the mock pods. ESA caps
                    concurrent connections per host (MaxConnsPerHost=25), so keeping production's
                    host grouping matters (e.g. 8 calls/request to one API gateway).
  esa_mock.sql      ESA staging DB: one LOADTEST_ service_configuration row per service in each
                    cloned sequence (same group/parallel structure), calling the mock with the
                    service's production p50 delay. Prints the mapped sequence strings.
  dm_mock.sql       DM staging DB: partner_service_mapping rows (LOADTEST_KYC / LOADTEST_LOAN ...)
                    and empty ('[]') Salesforce field mappings, so DM performs no SF object writes.
  set_delays.sql    ESA staging DB: re-points the LOADTEST_ services at another latency profile.
  analysis.txt      per live sequence: services, groups, expected wall time, and the per-pod
                    ceiling implied by MaxConnsPerHost=25 (the busiest host decides).

Output goes to --out (default ~/loadtest-generated), deliberately OUTSIDE the repo, because it
contains production service and host names.

Usage:
  ./gen_mock_from_snapshot.py --snapshot ../../local/prod_db_snapshot --latencies m3.txt
  ./gen_mock_from_snapshot.py --snapshot ... --latencies m3.txt --profile fast    # 0.2 s everywhere
Options:
  --clone 14:kyc,1:loan    partner_service_mapping ids to clone and the LOADTEST_ suffix to use
                           (default: the largest live kyc-decision and loan-decision sequences)
  --latencies FILE         M3 output pasted from the Compass shell, or JSON. Missing services
                           fall back to --default-delay and are listed in analysis.txt
  --percentile p50|p95     which latency to use for mock delays (default p50)
"""
import argparse
import csv
import glob
import json
import os
import re
import sys
from collections import Counter, defaultdict
from urllib.parse import urlparse

csv.field_size_limit(sys.maxsize)

LIVE = {
    "kyc-decision": "ZestMoney Wishfin SuperMoney Oppo Billcut Vivo Flipkart Ola DigitMoney Realme Turno Tecno".split(),
    "loan-decision": "GooglePay Finnable AirtelFinance Oppo Vivo Realme Tecno".split(),
}
MAX_CONNS_PER_HOST = 25
NS = "staging"


def load_csv(snapshot, prefix):
    files = sorted(glob.glob(os.path.join(snapshot, prefix + "_*.csv")))
    if not files:
        raise SystemExit(f"no {prefix}_*.csv in {snapshot}")
    with open(files[-1], newline="", encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def groups_of(seq):
    return [[int(x) for x in g.split(",") if x.strip()] for g in seq.strip("{}").split(";") if g.strip()]


def host_of(url):
    try:
        h = urlparse(url).netloc
    except ValueError:
        h = ""
    return h if h and not any(t in h for t in ("{{", "((", "<")) else "(templated)"


def parse_latencies(path):
    """Accepts mongosh shell output ({ service: 'X', stage: 'y', p50_ms: 1, ... }) or JSON."""
    out = {}
    if not path:
        return out
    text = open(path, encoding="utf-8").read()
    try:
        rows = json.loads(text)
    except json.JSONDecodeError:
        rows = []
        for block in re.findall(r"\{[^{}]*\}", text, flags=re.S):
            row = {}
            for k, v in re.findall(r"(\w+)\s*:\s*('(?:[^'\\]|\\.)*'|\"[^\"]*\"|[-\d.]+|null)", block):
                v = v.strip("'\"")
                row[k] = None if v == "null" else v
            if "service" in row:
                rows.append(row)
    for r in rows:
        def num(k):
            v = r.get(k)
            try:
                return float(v) if v not in (None, "") else None
            except ValueError:
                return None
        out[(r.get("service"), r.get("stage"))] = {"p50": num("p50_ms"), "p95": num("p95_ms"), "calls": num("calls"),
                                                    "live_calls": num("live_calls")}
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--snapshot", required=True)
    ap.add_argument("--latencies")
    ap.add_argument("--clone", default="")
    ap.add_argument("--profile", choices=["real", "fast"], default="real")
    ap.add_argument("--percentile", choices=["p50", "p95"], default="p50")
    ap.add_argument("--default-delay", type=float, default=0.5, help="seconds, when a service has no latency data")
    ap.add_argument("--fast-delay", type=float, default=0.2)
    ap.add_argument("--no-skip-weighting", action="store_true",
                    help="use the full live-call latency even for services production often skips (pre_execution). "
                         "Default: delay = latency x (live_calls / calls), i.e. the expected provider time per request")
    ap.add_argument("--out", default=os.path.expanduser("~/loadtest-generated"))
    a = ap.parse_args()

    psm = [r for r in load_csv(a.snapshot, "partner_service_mapping") if r["is_deleted"] == "false"]
    sc = {int(r["id"]): r for r in load_csv(a.snapshot, "service_configuration")}
    lat = parse_latencies(a.latencies)

    live = [r for r in psm if r["partner_name"] in LIVE.get(r["stage"], [])]
    if not a.clone:
        pick = []
        for stage, suffix in (("kyc-decision", "kyc"), ("loan-decision", "loan")):
            cands = [r for r in live if r["stage"] == stage]
            best = max(cands, key=lambda r: sum(map(len, groups_of(r["service_sequence_string"]))))
            pick.append((int(best["id"]), suffix))
    else:
        pick = [(int(x.split(":")[0]), x.split(":")[1]) for x in a.clone.split(",")]
    by_id = {int(r["id"]): r for r in psm}

    hosts = sorted({host_of(sc[i]["api_url"]) for r in live for g in groups_of(r["service_sequence_string"]) for i in g if i in sc})
    mock_of = {h: f"mock-h{n:02d}" for n, h in enumerate(hosts, 1)}

    def delay_s(svc_id, stage):
        if a.profile == "fast":
            return a.fast_delay, "fast"
        name = sc[svc_id]["service_name"]
        d = lat.get((name, stage)) or {}
        v = d.get(a.percentile)
        if v is None:
            return a.default_delay, "default"
        frac = 1.0
        if not a.no_skip_weighting and d.get("calls") and d.get("live_calls") is not None:
            frac = max(0.0, min(1.0, d["live_calls"] / d["calls"]))
        return max(0.01, round(v * frac / 1000.0, 3)), "measured"

    os.makedirs(a.out, exist_ok=True)

    # ---- mock-hosts.yaml
    with open(os.path.join(a.out, "mock-hosts.yaml"), "w") as f:
        f.write("# GENERATED - STAGING ONLY. One Service per production provider host -> loadtest-mock-provider pods.\n")
        f.write("# Requires k8s/mock-provider.yaml (the Deployment) to be applied first.\n")
        for h, m in mock_of.items():
            f.write(f"---\napiVersion: v1\nkind: Service\nmetadata:\n  name: {m}\n  namespace: {NS}\n"
                    f"  labels: {{ purpose: loadtest }}\n  annotations: {{ loadtest/models-host: \"{h}\" }}\n"
                    f"spec:\n  selector: {{ app: loadtest-mock-provider }}\n  ports: [{{ port: 80, targetPort: 8080 }}]\n")

    # ---- esa_mock.sql
    missing = set()
    esa = ["-- GENERATED - STAGING ONLY - ESA staging DB.  psql \"$ESA_STAGING_URL\" -v ON_ERROR_STOP=1 -f esa_mock.sql",
           f"-- latency profile: {a.profile} ({a.percentile})", "BEGIN;",
           "CREATE TEMP TABLE _lt_map(clone text, orig int, new_id bigint) ON COMMIT DROP;"]
    set_delays = ["-- GENERATED - STAGING ONLY - ESA staging DB: re-point LOADTEST_ services at this latency profile.",
                  f"-- profile: {a.profile} ({a.percentile})", "BEGIN;"]
    dm_names = []
    for psm_id, suffix in pick:
        r = by_id[psm_id]
        stage = r["stage"]
        ids = []
        for g in groups_of(r["service_sequence_string"]):
            for i in g:
                if i not in ids:
                    ids.append(i)
        for i in ids:
            src = sc[i]
            name = f"LOADTEST_{suffix.upper()}_{src['service_name']}"[:255]
            d, how = delay_s(i, stage)
            if how != "measured" and a.profile == "real":
                missing.add((src["service_name"], stage))
            url = f"http://{mock_of[host_of(src['api_url'])]}.{NS}.svc.cluster.local/delay/{d}"
            esa.append(
                "WITH ins AS (INSERT INTO service_configuration (service_name, api_url, headers, request_body, request_method, "
                "response_body, send_response, timeout, additional_config, created_date, created_by, is_deleted) VALUES "
                f"('{name}', '{url}', '{{}}', '{{}}', 'POST', '{{}}', false, 0, '{{}}', now(), 'loadtest', false) RETURNING id) "
                f"INSERT INTO _lt_map SELECT '{suffix}', {i}, id FROM ins;")
            set_delays.append(f"UPDATE service_configuration SET api_url = '{url}', modified_date = now(), modified_by = 'loadtest' "
                              f"WHERE service_name = '{name}' AND created_by = 'loadtest' AND is_deleted = false;")
            dm_names.append(name)
        seq = r["service_sequence_string"].replace("'", "")
        esa.append(
            f"SELECT '{suffix}' AS clone, '{r['partner_name']} / {stage}' AS cloned_from, string_agg(grp, ';' ORDER BY gi) AS sequence_string FROM ("
            f" SELECT g.gi, string_agg(m.new_id::text, ',' ORDER BY s.si) AS grp"
            f" FROM unnest(string_to_array('{seq}', ';')) WITH ORDINALITY AS g(txt, gi)"
            f" CROSS JOIN LATERAL unnest(string_to_array(g.txt, ',')) WITH ORDINALITY AS s(id, si)"
            f" JOIN _lt_map m ON m.clone = '{suffix}' AND m.orig = trim(s.id)::int GROUP BY g.gi) x;")
    esa.append("COMMIT;")
    set_delays.append("COMMIT;")
    open(os.path.join(a.out, "esa_mock.sql"), "w").write("\n".join(esa) + "\n")
    open(os.path.join(a.out, "set_delays.sql"), "w").write("\n".join(set_delays) + "\n")

    # ---- dm_mock.sql
    dm = ["-- GENERATED - STAGING ONLY - DM staging DB. Pass each sequence_string printed by esa_mock.sql:",
          "--   psql \"$DM_STAGING_URL\" -v ON_ERROR_STOP=1 " + " ".join(f"-v seq_{s}='<{s} sequence_string>'" for _, s in pick) + " -f dm_mock.sql"]
    for _, s in pick:
        dm += [f"\\if :{{?seq_{s}}}", "\\else", f"  \\echo 'ERROR: pass -v seq_{s}=<sequence_string>'", "  \\quit", "\\endif"]
    dm.append("BEGIN;")
    for psm_id, s in pick:
        r = by_id[psm_id]
        dm.append("INSERT INTO partner_service_mapping (name, partner_name, program_type, business_type, sourcing_program, loan_category, "
                  "customer_type, product_line, stage, service_sequence_string, created_date, created_by, is_deleted) VALUES "
                  f"('LOADTEST_{s.upper()}_SEQUENCE', 'LOADTEST_{s.upper()}', '', '', '', '', '', '', 'loadtest-{s}', :'seq_{s}', now(), 'loadtest', false) "
                  "RETURNING id AS sequence_id, partner_name, stage;")
    dm.append("INSERT INTO service_sfdc_field_mapping (service_name, request_body, created_date, created_by, is_deleted) VALUES")
    dm.append(",\n".join(f"  ('{n}', '[]', now(), 'loadtest', false)" for n in dm_names) + ";")
    dm.append("COMMIT;")
    open(os.path.join(a.out, "dm_mock.sql"), "w").write("\n".join(dm) + "\n")

    # ---- analysis.txt: shape + MaxConnsPerHost ceiling for every live sequence
    lines = [f"Latency source: {a.latencies or 'none'} ({a.percentile}); missing services use {a.default_delay}s.",
             f"Per-pod ceiling from MaxConnsPerHost={MAX_CONNS_PER_HOST}: requests/s = 25 / (sum of call-seconds to the busiest host per request).",
             "", f"{'id':>3} {'stage':<13} {'partner':<13} {'svc':>4} {'grp':>4} {'wall_s':>7}  {'binding host':<46} {'conn-s/req':>10} {'pod req/s cap':>13}"]
    for r in sorted(live, key=lambda x: (x["stage"], x["partner_name"], x["id"])):
        gs = groups_of(r["service_sequence_string"])
        wall = sum(max(delay_s(i, r["stage"])[0] for i in g) for g in gs)
        per_host = defaultdict(float)
        for g in gs:
            for i in g:
                per_host[host_of(sc[i]["api_url"])] += delay_s(i, r["stage"])[0]
        h, cs = max(per_host.items(), key=lambda kv: kv[1])
        lines.append(f"{r['id']:>3} {r['stage']:<13} {r['partner_name']:<13} {sum(map(len, gs)):>4} {len(gs):>4} {wall:>7.2f}  "
                     f"{h[:46]:<46} {cs:>10.2f} {MAX_CONNS_PER_HOST / cs if cs else float('inf'):>13.2f}")
    if missing:
        lines += ["", "Services with no latency data (default delay used): " + ", ".join(sorted(f"{n} [{s}]" for n, s in missing))]
    lines += ["", "Cloned: " + ", ".join(f"psm id {i} ({by_id[i]['partner_name']} {by_id[i]['stage']}) -> LOADTEST_{s.upper()} / loadtest-{s}" for i, s in pick),
              f"Mock hosts: {len(mock_of)} Services (mock-h01..mock-h{len(mock_of):02d})"]
    open(os.path.join(a.out, "analysis.txt"), "w").write("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote: {a.out}/mock-hosts.yaml, esa_mock.sql, dm_mock.sql, set_delays.sql, analysis.txt")


if __name__ == "__main__":
    main()
