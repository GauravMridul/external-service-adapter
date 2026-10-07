#!/usr/bin/env bash
# Summarises a sample_staging.sh output dir for a time window.
# Usage: ./summarize_samples.sh <out_dir> [from_iso] [to_iso]
#   e.g. ./summarize_samples.sh run1 2026-10-06T11:00 2026-10-06T11:20
# Prints per deployment: peak/avg CPU (m) and memory (Mi) per pod, peak ready replicas,
# and CPU throttling ratio per pod (throttled periods / periods) over the window.
set -euo pipefail
OUT="${1:?usage: $0 <out_dir> [from_iso] [to_iso]}"
FROM="${2:-0000}"; TO="${3:-9999}"

echo "== per-pod resource usage (window $FROM .. $TO) =="
awk -F, -v f="$FROM" -v t="$TO" 'NR>1 && $1>=f && $1<=t {
  k=$2; n[k]++; c[k]+=$4; m[k]+=$5; if ($4>cm[k]) cm[k]=$4; if ($5>mm[k]) mm[k]=$5 }
  END { printf "%-32s %8s %8s %8s %8s %8s\n","deployment","samples","cpu_avg","cpu_max","mem_avg","mem_max";
        for (k in n) printf "%-32s %8d %7.0fm %7.0fm %6.0fMi %6.0fMi\n", k, n[k], c[k]/n[k], cm[k], m[k]/n[k], mm[k] }' "$OUT/pods.csv"

echo; echo "== peak ready replicas =="
awk -F, -v f="$FROM" -v t="$TO" 'NR>1 && $1>=f && $1<=t { if ($3>r[$2]) r[$2]=$3 } END { for (k in r) printf "%-32s %s\n", k, r[k] }' "$OUT/replicas.csv"

echo; echo "== CPU throttling over window (ratio > 0.05 sustained => CPU-starved; see GOMAXPROCS in plan G2) =="
awk -F, -v f="$FROM" -v t="$TO" 'NR>1 && $1>=f && $1<=t {
  if (!($2 in p0)) { p0[$2]=$3; t0[$2]=$4; u0[$2]=$5 } p1[$2]=$3; t1[$2]=$4; u1[$2]=$5 }
  END { printf "%-48s %10s %10s %9s %12s\n","pod","periods","throttled","ratio","throttled_s";
        for (k in p0) { dp=p1[k]-p0[k]; dt=t1[k]-t0[k]; du=(u1[k]-u0[k])/1e6;
          printf "%-48s %10d %10d %9.3f %12.1f\n", k, dp, dt, (dp>0?dt/dp:0), du } }' "$OUT/throttle.csv"
