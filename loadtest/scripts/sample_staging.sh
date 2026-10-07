#!/usr/bin/env bash
# Samples DM and ESA pods on STAGING during a load test and writes CSVs:
#   pods.csv      ts, deployment, pod, cpu_millicores, memory_mib
#   throttle.csv  ts, pod, nr_periods, nr_throttled, throttled_usec   (cumulative cgroup counters)
#   replicas.csv  ts, deployment, ready_replicas
# Usage: ./sample_staging.sh <out_dir> [interval_seconds=15]
# Stop with Ctrl-C. Refuses to run against any namespace other than "staging".
set -euo pipefail

NS="${NS:-staging}"
OUT="${1:?usage: $0 <out_dir> [interval_seconds]}"
INTERVAL="${2:-15}"
DEPLOYS=("decision-manager-uat" "external-service-adapter-uat")

if [[ "$NS" != "staging" ]]; then
  echo "Refusing: NS=$NS. This sampler is for staging only." >&2
  exit 1
fi
mkdir -p "$OUT"
[[ -f "$OUT/pods.csv" ]]     || echo "ts,deployment,pod,cpu_m,mem_mi" > "$OUT/pods.csv"
[[ -f "$OUT/throttle.csv" ]] || echo "ts,pod,nr_periods,nr_throttled,throttled_usec" > "$OUT/throttle.csv"
[[ -f "$OUT/replicas.csv" ]] || echo "ts,deployment,ready" > "$OUT/replicas.csv"

# One-off context: node CPU count (what GOMAXPROCS defaults to on Go 1.23) vs the pod's CPU quota.
{
  echo "# captured $(date -Iseconds)"
  echo "# node cpu capacity:"
  kubectl get nodes -o custom-columns='NODE:.metadata.name,CPU:.status.capacity.cpu,ALLOCATABLE:.status.allocatable.cpu' 2>&1
  for d in "${DEPLOYS[@]}"; do
    pod=$(kubectl -n "$NS" get pods -l "app=$d" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    [[ -z "$pod" ]] && continue
    echo "# $d ($pod) cpu.max (quota period):"
    kubectl -n "$NS" exec "$pod" -- cat /sys/fs/cgroup/cpu.max 2>/dev/null \
      || kubectl -n "$NS" exec "$pod" -- sh -c 'cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us /sys/fs/cgroup/cpu/cpu.cfs_period_us' 2>&1
  done
} > "$OUT/context.txt"

echo "Sampling every ${INTERVAL}s into $OUT (Ctrl-C to stop)"
while true; do
  ts=$(date -Iseconds)
  for d in "${DEPLOYS[@]}"; do
    kubectl -n "$NS" top pod -l "app=$d" --no-headers 2>/dev/null \
      | awk -v ts="$ts" -v d="$d" '{c=$2; m=$3; sub(/m$/,"",c); if (m ~ /Gi$/) {sub(/Gi$/,"",m); m=m*1024} else sub(/Mi$/,"",m); print ts","d","$1","c","m}' \
      >> "$OUT/pods.csv" || true
    ready=$(kubectl -n "$NS" get deploy "$d" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
    echo "$ts,$d,${ready:-0}" >> "$OUT/replicas.csv"
    for pod in $(kubectl -n "$NS" get pods -l "app=$d" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
      stat=$(kubectl -n "$NS" exec "$pod" -- cat /sys/fs/cgroup/cpu.stat 2>/dev/null || true)
      p=$(awk '/^nr_periods/{print $2}' <<<"$stat"); t=$(awk '/^nr_throttled/{print $2}' <<<"$stat"); u=$(awk '/^throttled_usec/{print $2}' <<<"$stat")
      [[ -n "$p" ]] && echo "$ts,$pod,$p,$t,${u:-0}" >> "$OUT/throttle.csv"
    done
  done
  sleep "$INTERVAL"
done
