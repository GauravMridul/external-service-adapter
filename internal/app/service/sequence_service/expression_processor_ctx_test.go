package sequence_service

import (
	"context"
	"strings"
	"testing"
	"time"

	"esa/internal/app/dto/common_dto"

	"github.com/stretchr/testify/assert"
)

// newCtxTestMasterDTO returns a minimal MasterDTO sufficient for literal-only expressions.
func newCtxTestMasterDTO() *common_dto.MasterDTO {
	return &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
}

// TestProcessPlaceholders_CtxBackground_EqualsLegacy proves the new ctx-aware path is behavior-neutral:
// with context.Background() (no deadline) the output is byte-identical to the legacy no-ctx methods.
// This is the happy-path equivalence guard requested for the deadline change.
func TestProcessPlaceholders_CtxBackground_EqualsLegacy(t *testing.T) {
	ep := NewExpressionProcessor()
	dto := newCtxTestMasterDTO()

	exprs := []string{
		"plain literal, no placeholders",
		"{{NUMERIC:5}}",
		"{{CONCAT:a:-:b}}",
		"{{FORMAT:text:HeLLo:lowercase}}",
		"{{CUSTOM:sha256Hash:hello}}",
		"{{CUSTOM:takeLast:abcdef:3}}",
		"{{CONCAT:{{NUMERIC:1}}:-:{{NUMERIC:2}}}}",
		"prefix {{NUMERIC:7}} suffix",
	}

	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			// String mode: ProcessPlaceholders (legacy) vs ProcessPlaceholdersCtx(Background).
			legacyStr := ep.ProcessPlaceholders(expr, dto, nil, map[string]string{}, nil)
			ctxStr := ep.ProcessPlaceholdersCtx(context.Background(), expr, dto, nil, map[string]string{}, nil)
			assert.Equal(t, legacyStr, ctxStr, "string-mode ctx output must equal legacy")

			// Typed mode: ProcessPlaceholdersWithType (legacy) vs ...Ctx(Background).
			legacyTyped := ep.ProcessPlaceholdersWithType(expr, TypeModeTyped, dto, nil, map[string]string{}, nil)
			ctxTyped := ep.ProcessPlaceholdersWithTypeCtx(context.Background(), expr, TypeModeTyped, dto, nil, map[string]string{}, nil)
			assert.Equal(t, legacyTyped, ctxTyped, "typed-mode ctx output must equal legacy")
		})
	}
}

// TestProcessPlaceholders_NilCtx_Safe confirms a nil context is treated as "no deadline" (via ctxErr)
// and resolves normally rather than panicking.
func TestProcessPlaceholders_NilCtx_Safe(t *testing.T) {
	ep := NewExpressionProcessor()
	dto := newCtxTestMasterDTO()

	var nilCtx context.Context // nil
	got := ep.ProcessPlaceholdersCtx(nilCtx, "{{NUMERIC:5}}", dto, nil, map[string]string{}, nil)
	legacy := ep.ProcessPlaceholders("{{NUMERIC:5}}", dto, nil, map[string]string{}, nil)
	assert.Equal(t, legacy, got, "nil ctx must behave like no-deadline and resolve normally")
}

// TestProcessPlaceholders_DeadlineAborts proves the resolution loop observes the request deadline:
// with an already-expired context, a payload containing a very large number of placeholders returns
// promptly (loop bails) instead of grinding through all of them, and leaves placeholders unresolved.
// This is the guard against the silent CPU-bound hang.
func TestProcessPlaceholders_DeadlineAborts(t *testing.T) {
	ep := NewExpressionProcessor()
	dto := newCtxTestMasterDTO()

	// ~100k placeholders. Without the deadline check this loop would re-scan/splice a multi-hundred-KB
	// string ~100k times (seconds+). With the check it must bail almost immediately.
	input := strings.Repeat("{{NUMERIC:1}} ", 100000)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // ensure the deadline is firmly in the past

	start := time.Now()
	result := ep.ProcessPlaceholdersCtx(ctx, input, dto, nil, map[string]string{}, nil)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 3*time.Second, "expired-deadline resolution must abort promptly, not grind the full loop")
	assert.True(t, strings.Contains(result, "{{NUMERIC"), "aborted resolution should leave placeholders unresolved (best-effort)")
}

// TestProcessPlaceholders_GenerousDeadline_Resolves is the counterpart: with a generous deadline a
// normal expression resolves fully (the deadline guard does not interfere with legitimate work).
func TestProcessPlaceholders_GenerousDeadline_Resolves(t *testing.T) {
	ep := NewExpressionProcessor()
	dto := newCtxTestMasterDTO()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := ep.ProcessPlaceholdersCtx(ctx, "{{NUMERIC:5}}", dto, nil, map[string]string{}, nil)
	legacy := ep.ProcessPlaceholders("{{NUMERIC:5}}", dto, nil, map[string]string{}, nil)
	assert.Equal(t, legacy, got)
	assert.NotContains(t, got, "{{", "with a generous deadline the expression must resolve fully")
}
