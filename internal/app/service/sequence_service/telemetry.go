package sequence_service

import (
	"os"
	"runtime"
	"time"

	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"
)

// initRuntimeTelemetry starts a periodic, low-overhead runtime snapshot logger.
//
// Disabled by default (telemetry.enabled=false) so enabling it is an explicit, reversible
// config change with no code deployment. Mirrors the decision-manager telemetry contract
// (same "telemetry" log key and comparable field names) so DM and ESA snapshots can be
// correlated on a single timeline during load testing.
//
// Emitted fields answer the questions the capacity model needs and that `kubectl top`
// cannot: how close each pod is to its concurrency cap, whether goroutine count is
// tracking sequence size, whether the audit queue is backing up onto the request path,
// and whether GOMAXPROCS matches the container CPU limit.
func (s *SequenceService) initRuntimeTelemetry() {
	s.telemetryInitOnce.Do(func() {
		if !commoninit.GetConfigBool(constants.TelemetryEnabledKey, false) {
			return
		}

		intervalSeconds := commoninit.GetConfigInt(constants.TelemetryIntervalSecondsKey, constants.DefaultTelemetryIntervalSeconds)
		if intervalSeconds < constants.MinTelemetryIntervalSeconds {
			intervalSeconds = constants.MinTelemetryIntervalSeconds
		}
		memstatsEnabled := commoninit.GetConfigBool(constants.TelemetryMemstatsEnabledKey, true)
		interval := time.Duration(intervalSeconds) * time.Second

		commoninit.StartTrackedGoroutine(func() {
			shutdownSignal := commoninit.GetAppShutdownSignal()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-shutdownSignal:
					if s.logger != nil {
						s.logger.Info("Stopping runtime telemetry loop due to shutdown signal")
					}
					return
				case <-ticker.C:
					s.logRuntimeTelemetrySnapshot(memstatsEnabled)
				}
			}
		})

		if s.logger != nil {
			s.logger.Infow("Runtime telemetry enabled",
				"interval_seconds", intervalSeconds,
				"memstats_enabled", memstatsEnabled)
		}
	})
}

// logRuntimeTelemetrySnapshot emits one snapshot line. Cheap by design: channel len/cap,
// atomic loads, and optionally runtime.ReadMemStats (which briefly stops the world, hence
// the 10s minimum interval).
func (s *SequenceService) logRuntimeTelemetrySnapshot(memstatsEnabled bool) {
	if s.logger == nil {
		return
	}

	inFlight, limiterCap := commoninit.SequenceLimiterStats()
	var limiterUtilPct float64
	if limiterCap > 0 {
		limiterUtilPct = (float64(inFlight) / float64(limiterCap)) * 100
	}

	fields := []interface{}{
		"telemetry", "runtime_snapshot",
		// Concurrency headroom: the primary per-pod capacity signal.
		"sequence_in_flight", inFlight,
		"sequence_limiter_cap", limiterCap,
		"sequence_limiter_util_pct", limiterUtilPct,
		// Audit write path: queue_len approaching cap means inline Mongo writes
		// start landing on the request path and show up as latency.
		"async_esa_queue_len", len(s.asyncEsaLogQueue),
		"async_esa_queue_cap", cap(s.asyncEsaLogQueue),
		"async_esa_inline_fallback_total", s.asyncEsaLogInlineFallback.Load(),
		// Goroutine count tracks total services per sequence, not the group semaphore,
		// because a goroutine is started per service before it acquires groupSem.
		"go_goroutines", runtime.NumGoroutine(),
		// GOMAXPROCS is NOT derived from the cgroup CPU limit. If this exceeds the
		// container's CPU allowance, the runtime oversubscribes threads and the
		// scheduler gets throttled by CFS, inflating tail latency.
		"go_maxprocs", runtime.GOMAXPROCS(0),
		"go_numcpu", runtime.NumCPU(),
		"env_gomemlimit", os.Getenv("GOMEMLIMIT"),
		"env_memory_limit_bytes", os.Getenv("MEMORY_LIMIT_BYTES"),
	}

	if memstatsEnabled {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		fields = append(fields,
			"go_heap_alloc_mb", bytesToMB(mem.HeapAlloc),
			"go_heap_inuse_mb", bytesToMB(mem.HeapInuse),
			"go_heap_idle_mb", bytesToMB(mem.HeapIdle),
			"go_heap_released_mb", bytesToMB(mem.HeapReleased),
			"go_stack_inuse_mb", bytesToMB(mem.StackInuse),
			"go_sys_mb", bytesToMB(mem.Sys),
			"go_num_gc", mem.NumGC,
			"go_gc_cpu_fraction", mem.GCCPUFraction)
	}

	s.logger.Infow("Runtime telemetry snapshot", fields...)
}

func bytesToMB(v uint64) float64 {
	return float64(v) / (1024 * 1024)
}
