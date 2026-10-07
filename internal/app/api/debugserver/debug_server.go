// Package debugserver exposes Go's net/http/pprof profiling endpoints on a separate,
// gated HTTP listener for short-lived production diagnostics (e.g. confirming a CPU-bound
// hang or capturing goroutine/heap profiles).
//
// Safety:
//   - Disabled by default. Enable only via config "pprof.enabled=true" or env ENABLE_PPROF=true.
//   - Binds to 127.0.0.1 by default, so it is reachable only via `kubectl port-forward` / `kubectl exec`,
//     never from other pods, Services, or Ingress. Do not bind to 0.0.0.0 in shared environments.
//   - Intended to be turned off again once debugging is complete.
package debugserver

import (
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"strings"
	"time"

	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"
)

// Start launches the pprof debug server in a background goroutine when enabled.
// It is a no-op (and logs nothing) when disabled, so it is safe to call unconditionally
// from application startup. The server lifetime is tied to the process; it is intentionally
// not registered with the graceful-shutdown manager since it holds no critical state.
func Start() {
	log := commoninit.GetLogger()

	enabled := commoninit.GetConfigBool(constants.PprofEnabledKey, false)
	// Env override (ENABLE_PPROF=true|1) so it can be toggled from the Deployment without a config push.
	if v := strings.TrimSpace(os.Getenv("ENABLE_PPROF")); v != "" {
		enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if !enabled {
		return
	}

	addr := commoninit.GetConfigString(constants.PprofAddrKey, constants.DefaultPprofAddr)
	if a := strings.TrimSpace(os.Getenv("PPROF_ADDR")); a != "" {
		addr = a
	}

	// Explicit mux so only the pprof endpoints are exposed (avoids leaking anything else
	// that may be registered on http.DefaultServeMux). Index also serves the named profiles
	// (goroutine, heap, allocs, block, mutex, threadcreate) via /debug/pprof/<name>.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if log != nil {
		log.Warnw("pprof debug server ENABLED (diagnostics only) — reachable via port-forward only; disable after debugging",
			"addr", addr)
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if log != nil {
				log.Errorw("pprof debug server failed to start", "error", err, "addr", addr)
			}
		}
	}()
}
