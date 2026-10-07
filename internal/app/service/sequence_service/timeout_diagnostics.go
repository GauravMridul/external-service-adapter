package sequence_service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
	"esa/internal/app/utility"
)

const (
	maxTimeoutDiagFailedServices   = 10
	maxTimeoutDiagProcessing       = 10
	maxTimeoutDiagLongestCompleted = 5
	maxErrorSnippetRunes           = 240
)

// BuildProcessSequenceTimeoutDiagnostics returns a JSON-serializable snapshot when the
// process-sequence API deadline is exceeded. It is a best-effort hint only (parallel groups
// share wall time).
func BuildProcessSequenceTimeoutDiagnostics(
	masterDTO *common_dto.MasterDTO,
	ctx context.Context,
	elapsedMs int64,
	timeoutSeconds int,
) map[string]interface{} {
	out := map[string]interface{}{
		"attribution_hint":    "partial snapshot when API deadline exceeded; parallel groups share wall time",
		"timeout_seconds":     timeoutSeconds,
		"elapsed_ms":          elapsedMs,
		"context_group_index": utility.GetGroupIndex(ctx),
	}

	if masterDTO == nil {
		out["total_sequence_groups"] = 0
		out["service_status_counts"] = map[string]int{}
		return out
	}

	totalGroups := len(masterDTO.SequenceArray)
	out["total_sequence_groups"] = totalGroups

	counts := make(map[string]int)
	var failed []map[string]interface{}
	var processing []map[string]interface{}
	type completedRow struct {
		serviceID   int
		serviceName string
		timeMs      float64
	}
	var completed []completedRow

	visit := func(svc *models.EsaLog) {
		if svc == nil {
			return
		}
		st := svc.Status
		if st == "" {
			st = "UNKNOWN"
		}
		counts[st]++

		summary := map[string]interface{}{
			"service_id":   svc.ServiceId,
			"service_name": svc.ServiceName,
			"status":       svc.Status,
			"time_taken_ms": func() float64 {
				if svc.TimeTaken < 0 {
					return 0
				}
				return svc.TimeTaken
			}(),
		}

		switch svc.Status {
		case "FAILED":
			if msg := errorSnippetFromEsaLog(svc); msg != "" {
				summary["error_snippet"] = msg
			}
			if len(failed) < maxTimeoutDiagFailedServices {
				failed = append(failed, summary)
			}
		case "PROCESSING":
			if len(processing) < maxTimeoutDiagProcessing {
				processing = append(processing, summary)
			}
		case "COMPLETED":
			completed = append(completed, completedRow{
				serviceID:   svc.ServiceId,
				serviceName: svc.ServiceName,
				timeMs:      svc.TimeTaken,
			})
		}
	}

	for _, svc := range masterDTO.EsaServices {
		visit(svc)
	}

	out["service_status_counts"] = counts
	if len(processing) > 0 {
		out["services_processing"] = processing
	}
	if len(failed) > 0 {
		out["services_failed"] = failed
	}

	sort.Slice(completed, func(i, j int) bool {
		return completed[i].timeMs > completed[j].timeMs
	})
	if n := len(completed); n > maxTimeoutDiagLongestCompleted {
		completed = completed[:maxTimeoutDiagLongestCompleted]
	}
	if len(completed) > 0 {
		longest := make([]map[string]interface{}, 0, len(completed))
		for _, r := range completed {
			longest = append(longest, map[string]interface{}{
				"service_id":   r.serviceID,
				"service_name": r.serviceName,
				"time_taken_ms": func() float64 {
					if r.timeMs < 0 {
						return 0
					}
					return r.timeMs
				}(),
			})
		}
		out["longest_completed_services_ms"] = longest
	}

	return out
}

func errorSnippetFromEsaLog(svc *models.EsaLog) string {
	if svc == nil || svc.Response.Body == nil {
		return ""
	}
	parts := []string{}
	for _, k := range []string{"error", "error_details"} {
		if v, ok := svc.Response.Body[k]; ok && v != nil {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	s := strings.TrimSpace(strings.Join(parts, " | "))
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > maxErrorSnippetRunes {
		s = string(r[:maxErrorSnippetRunes]) + "…"
	}
	return s
}
