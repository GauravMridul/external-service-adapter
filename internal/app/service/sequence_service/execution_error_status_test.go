package sequence_service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"esa/internal/app/models"
)

// fakeNetTimeoutErr implements net.Error reporting a timeout, to verify typed detection in
// isTimeoutError independent of the error's message text.
type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "i/o operation stalled" } // deliberately no "timeout" text
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return true }

// The exact shape of the error the Go HTTP client returns on a client timeout — this is the string
// that the previous case-sensitive `strings.Contains(err, "timeout")` check failed to classify.
const prodClientTimeoutMsg = `Post "https://hub.example.com/Inquiry": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`

// TestIsTimeoutError covers BUG-3: genuine Go timeouts must be recognized by type and by the real
// message text (which contains "Timeout"/"deadline exceeded" but not the lowercase word "timeout").
func TestIsTimeoutError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context deadline", context.DeadlineExceeded, true},
		{"os deadline", os.ErrDeadlineExceeded, true},
		{"net.Error timeout (typed, no keyword)", fakeNetTimeoutErr{}, true},
		{"production client timeout string", errors.New(prodClientTimeoutMsg), true},
		{"wrapped production timeout", fmt.Errorf("service call failed: %w", errors.New(prodClientTimeoutMsg)), true},
		{"lowercase timeout word", errors.New("dial tcp: i/o timeout"), true},
		{"connection refused is not a timeout", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), false},
		{"generic error", errors.New("some other failure"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTimeoutError(tc.err); got != tc.want {
				t.Fatalf("isTimeoutError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsConnectionRefusedError covers the connection-refused classification (typed + string).
func TestIsConnectionRefusedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"syscall ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"wrapped ECONNREFUSED", fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED), true},
		{"string connection refused", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"timeout is not connection refused", errors.New(prodClientTimeoutMsg), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnectionRefusedError(tc.err); got != tc.want {
				t.Fatalf("isConnectionRefusedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// logWithPreset returns an EsaLog carrying a stale pre-set success response, simulating a service
// that was stamped StatusCode=200 (+ mock body) at load time before its live call ran.
func logWithPreset() *models.EsaLog {
	return &models.EsaLog{
		ServiceName: "CRIF",
		Response: models.ResponseDetails{
			StatusCode: 200,
			Body:       map[string]interface{}{"mock": true},
		},
	}
}

// TestApplyExecutionError_TimeoutForces408 covers BUG-3 + BUG-4: a real client timeout must yield
// a 408 (not the generic 500), and the stale pre-set 200 + mock body must be replaced.
func TestApplyExecutionError_TimeoutForces408(t *testing.T) {
	si := &ServiceInvoker{}
	esaLog := logWithPreset()

	si.applyExecutionError(esaLog, errors.New(prodClientTimeoutMsg))

	if esaLog.Response.StatusCode != 408 {
		t.Fatalf("StatusCode = %d, want 408", esaLog.Response.StatusCode)
	}
	if esaLog.Response.Body["error"] != "Request timeout" {
		t.Fatalf("body.error = %v, want \"Request timeout\"", esaLog.Response.Body["error"])
	}
	if _, ok := esaLog.Response.Body["mock"]; ok {
		t.Fatalf("stale pre-set body was not reset: %v", esaLog.Response.Body)
	}
}

// TestApplyExecutionError_ConnectionRefusedForces503 covers connection-refused classification.
func TestApplyExecutionError_ConnectionRefusedForces503(t *testing.T) {
	si := &ServiceInvoker{}
	esaLog := logWithPreset()

	si.applyExecutionError(esaLog, fmt.Errorf("dial: %w", syscall.ECONNREFUSED))

	if esaLog.Response.StatusCode != 503 {
		t.Fatalf("StatusCode = %d, want 503", esaLog.Response.StatusCode)
	}
	if esaLog.Response.Body["error"] != "Connection refused" {
		t.Fatalf("body.error = %v, want \"Connection refused\"", esaLog.Response.Body["error"])
	}
}

// TestApplyExecutionError_GenericFailureForcesNon2xxAndResetsPreset200 is the core BUG-4 case: a
// generic failure must never leave a success status code behind.
func TestApplyExecutionError_GenericFailureForcesNon2xxAndResetsPreset200(t *testing.T) {
	si := &ServiceInvoker{}
	esaLog := logWithPreset() // StatusCode preset to 200

	si.applyExecutionError(esaLog, errors.New("unexpected EOF"))

	if esaLog.Response.StatusCode != 500 {
		t.Fatalf("StatusCode = %d, want 500 (stale 200 must be forced to non-2xx)", esaLog.Response.StatusCode)
	}
	if esaLog.Response.Body["error"] != "Request failed" {
		t.Fatalf("body.error = %v, want \"Request failed\"", esaLog.Response.Body["error"])
	}
	if _, ok := esaLog.Response.Body["mock"]; ok {
		t.Fatalf("stale pre-set body was not reset: %v", esaLog.Response.Body)
	}
}

// TestApplyExecutionError_PreservesExistingErrorStatus ensures a real server error status (>=400)
// captured before classification is not downgraded to 500.
func TestApplyExecutionError_PreservesExistingErrorStatus(t *testing.T) {
	si := &ServiceInvoker{}
	esaLog := &models.EsaLog{
		ServiceName: "CRIF",
		Response:    models.ResponseDetails{StatusCode: 502, Body: map[string]interface{}{}},
	}

	si.applyExecutionError(esaLog, errors.New("bad gateway body read error"))

	if esaLog.Response.StatusCode != 502 {
		t.Fatalf("StatusCode = %d, want 502 preserved", esaLog.Response.StatusCode)
	}
	if esaLog.Response.Body["error"] != "Request failed" {
		t.Fatalf("body.error = %v, want \"Request failed\"", esaLog.Response.Body["error"])
	}
}

// TestApplyExecutionError_TimestampsUnaffected is a light guard that applyExecutionError does not
// depend on timing fields (documents intended pure behavior).
func TestApplyExecutionError_TimestampsUnaffected(t *testing.T) {
	si := &ServiceInvoker{}
	esaLog := logWithPreset()
	before := time.Now()
	esaLog.StartTime = before
	si.applyExecutionError(esaLog, context.DeadlineExceeded)
	if !esaLog.StartTime.Equal(before) {
		t.Fatalf("StartTime should be untouched by applyExecutionError")
	}
	if esaLog.Response.StatusCode != 408 {
		t.Fatalf("StatusCode = %d, want 408", esaLog.Response.StatusCode)
	}
}
