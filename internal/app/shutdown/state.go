package shutdown

import "sync/atomic"

// shuttingDown is 1 when the process is shutting down (SIGTERM received), 0 otherwise.
var shuttingDown atomic.Uint32

// SetShuttingDown marks the application as shutting down (e.g. on SIGTERM).
// Readiness checks should then return unhealthy so traffic is moved to other pods.
func SetShuttingDown() {
	shuttingDown.Store(1)
}

// IsShuttingDown returns true if the application has been marked as shutting down.
func IsShuttingDown() bool {
	return shuttingDown.Load() == 1
}
