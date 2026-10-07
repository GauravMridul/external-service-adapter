#!/usr/bin/env python3
"""Turns measured staging capacity into pod counts and data-tier demand for N x current load.

All inputs come from the tests and baseline queries (see the plan, section A):
  --peak            current production peak, decisions/sec            (Mongo Q4, top row)
  --dm-pod          decisions/sec one DM pod sustains at <=70% CPU, no 503s, p95 within SLO (T1-DM)
  --esa-pod         ESA requests/sec one ESA pod sustains, same criteria (T1-ESA)
  --docs            Mongo documents written per decision                (Mongo Q17)
  --doc-kb          average Mongo document size in KB, weighted          (Mongo Q15/Q16)
  --dm-pg / --esa-pg  peak Postgres connections observed per pod        (pg_stat_activity during T1)
  --dm-cpu / --esa-cpu  per-pod CPU (millicores) observed at --dm-pod / --esa-pod load (T1)
  --dm-mem / --esa-mem  per-pod memory (Mi) observed at that load       (T1)
Optional:
  --multiples       load multiples to tabulate (default 1,2,3,5,10)
  --margin          headroom on top of the target load (default 1.3 = 30%)
  --min-pods        HPA floor (default 2), --max-pods current HPA ceiling (default 7)
Example:
  ./size_fleet.py --peak 0.8 --dm-pod 3.0 --esa-pod 3.5 --docs 7.2 --doc-kb 9 \
      --dm-pg 6 --esa-pg 12 --dm-cpu 650 --esa-cpu 680 --dm-mem 260 --esa-mem 300
"""
import argparse
import math


def pods(load, per_pod, margin, floor):
    return max(floor, math.ceil(load * margin / per_pod))


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for name in ("peak", "dm-pod", "esa-pod", "docs", "doc-kb", "dm-pg", "esa-pg",
                 "dm-cpu", "esa-cpu", "dm-mem", "esa-mem"):
        p.add_argument(f"--{name}", type=float, required=True)
    p.add_argument("--multiples", default="1,2,3,5,10")
    p.add_argument("--margin", type=float, default=1.3)
    p.add_argument("--min-pods", type=int, default=2)
    p.add_argument("--max-pods", type=int, default=7)
    a = p.parse_args()

    if min(a.dm_pod, a.esa_pod, a.peak) <= 0:
        raise SystemExit("peak, dm-pod and esa-pod must be > 0")

    mults = [float(x) for x in a.multiples.split(",") if x.strip()]
    ceiling = min(a.dm_pod * a.max_pods, a.esa_pod * a.max_pods)
    print(f"Current HPA ceiling ({a.max_pods} pods each): {ceiling:.2f} decisions/sec "
          f"= {ceiling / a.peak:.1f}x today's peak of {a.peak:.2f}/sec "
          f"(bound by {'DM' if a.dm_pod <= a.esa_pod else 'ESA'})")
    print(f"Sizing margin: {a.margin:.2f}x on top of each target load; HPA floor {a.min_pods}\n")

    hdr = (f"{'load':>6} {'dec/s':>7} {'dec/min':>8} {'DM pods':>8} {'ESA pods':>9} "
           f"{'fits 7?':>8} {'CPU req*':>9} {'Mem req*':>9} {'PG conns':>9} "
           f"{'Mongo w/s':>10} {'Mongo GB/day':>13}")
    print(hdr)
    print("-" * len(hdr))
    for m in mults:
        load = a.peak * m
        n_dm = pods(load, a.dm_pod, a.margin, a.min_pods)
        n_esa = pods(load, a.esa_pod, a.margin, a.min_pods)
        cpu = (n_dm * a.dm_cpu + n_esa * a.esa_cpu) / 1000.0
        mem = (n_dm * a.dm_mem + n_esa * a.esa_mem) / 1024.0
        pg = n_dm * a.dm_pg + n_esa * a.esa_pg
        writes = load * a.docs
        gb_day = writes * 86400 * a.doc_kb / (1024 * 1024)
        fits = "yes" if max(n_dm, n_esa) <= a.max_pods else "NO"
        print(f"{m:>5.1f}x {load:>7.2f} {load * 60:>8.0f} {n_dm:>8d} {n_esa:>9d} {fits:>8} "
              f"{cpu:>7.1f}c {mem:>7.1f}Gi {pg:>9.0f} {writes:>10.1f} {gb_day:>13.2f}")
    print("\n* CPU/Mem req = what the scheduler must actually reserve for the fleet if requests are set to "
          "the per-pod usage measured at capacity (today's requests reserve only 200m per pod).")
    print("  PG conns = observed per-pod peak x pods (compare with Aurora max_connections; "
          "pool ceilings are 500/pod by config).")


if __name__ == "__main__":
    main()
