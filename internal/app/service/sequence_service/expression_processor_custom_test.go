package sequence_service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseCustomLogicExpression_Characterization locks in the EXACT current behavior of
// parseCustomLogicExpression before it is refactored for performance. Every case here must pass
// against the existing implementation first; the refactor must keep them all passing unchanged.
//
// Behaviors deliberately pinned (subtle, must be preserved byte-for-byte):
//   - methodName is the trimmed text before the first ':'; no ':' => whole input is the methodName, no params.
//   - top-level ':' splits params; ':' inside quotes or inside {{...}} does NOT split.
//   - each emitted param is strings.TrimSpace'd.
//   - a final param is appended only when the raw (untrimmed) buffer is non-empty:
//       "m:a:"      -> ["a"]        (trailing empty buffer dropped)
//       "m:a:   "   -> ["a", ""]    (trailing whitespace buffer kept, then trimmed to "")
//   - mid-string empty/whitespace params are kept (trimmed): "m:  :b" -> ["", "b"].
//   - multi-byte UTF-8 is reassembled byte-by-byte and stays intact.
func TestParseCustomLogicExpression_Characterization(t *testing.T) {
	ep := NewExpressionProcessor()

	// The current implementation appends bytes via `current += string(byte)`. In Go,
	// string(b) for a byte b is the UTF-8 encoding of the CODE POINT b, so bytes > 0x7F are
	// re-encoded (e.g. the two bytes of "é" => "Ã©"). This is pre-existing behavior we must
	// preserve exactly; the refactor reproduces it with WriteRune(rune(b)). Build the expected
	// value the same way so the test pins reality (NOT an idealized UTF-8 result).
	mojibakeCafe := "caf" + string(rune(0xC3)) + string(rune(0xA9)) // current output for input bytes c,a,f,0xC3,0xA9

	cases := []struct {
		name       string
		expression string
		wantMethod string
		wantParams []string
	}{
		{"single param", "sha256Hash:abc", "sha256Hash", []string{"abc"}},
		{"multiple params", "method:a:b:c", "method", []string{"a", "b", "c"}},
		{"trims each param", "m: a : b ", "m", []string{"a", "b"}},
		{"method only, no colon", "methodOnly", "methodOnly", []string{}},
		{"empty input", "", "", []string{}},
		{"leading colon => empty method", ":x", "", []string{"x"}},
		{"quote protects colon", "fmt:'a:b':c", "fmt", []string{"'a:b'", "c"}},
		{"double-quote protects colon", "fmt:\"a:b\":c", "fmt", []string{"\"a:b\"", "c"}},
		{"nested braces protect colons", "f:{{A:{{B:1}}}}:y", "f", []string{"{{A:{{B:1}}}}", "y"}},
		{"trailing empty buffer dropped", "m:a:", "m", []string{"a"}},
		{"trailing whitespace buffer kept as empty", "m:a:   ", "m", []string{"a", ""}},
		{"mid empty/whitespace param kept", "m:  :b", "m", []string{"", "b"}},
		// Pins the pre-existing byte->rune re-encoding of non-ASCII input (input bytes are c,a,f,0xC3,0xA9).
		{"non-ascii bytes re-encoded (pre-existing)", "m:caf\xC3\xA9:y", "m", []string{mojibakeCafe, "y"}},
		// Pins the known joinDistinct separator behavior: ", " separator is trimmed to ",".
		{"trimspace strips separator space", "joinDistinct:source:, ", "joinDistinct", []string{"source", ","}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMethod, gotParams := ep.parseCustomLogicExpression(tc.expression)
			assert.Equal(t, tc.wantMethod, gotMethod, "methodName mismatch")
			assert.Equal(t, tc.wantParams, gotParams, "params mismatch")
		})
	}
}

// buildLargeCustomArg returns a CUSTOM expression whose single parameter is sizeBytes long.
// It mimics the production shape {{CUSTOM:getJsonPath:<big-json>:<path>}} (minus the outer {{CUSTOM: }}),
// i.e. the parser must walk the whole multi-MB argument.
func buildLargeCustomArg(sizeBytes int) string {
	var sb strings.Builder
	sb.Grow(sizeBytes + 32)
	sb.WriteString("getJsonPath:")
	// A realistic-ish JSON-ish blob with no top-level ':' breaks; quotes/braces exercise the state machine.
	chunk := `{"k":"vvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"},`
	for sb.Len() < sizeBytes {
		sb.WriteString(chunk)
	}
	sb.WriteString(":CIR-REPORT-FILE.REPORT-DATA")
	return sb.String()
}

// BenchmarkParseCustomLogicExpression_Large measures parsing a ~3.7MB argument (the production
// CRIF case). NOTE: this is intentionally a Benchmark (not a Test) so `go test ./...` never runs it
// against the pre-fix O(n^2) code where it would hang. Run explicitly AFTER the fix:
//
//	go test -run x -bench BenchmarkParseCustomLogicExpression_Large -benchtime 1x ./internal/app/service/sequence_service/
func BenchmarkParseCustomLogicExpression_Large(b *testing.B) {
	ep := NewExpressionProcessor()
	expr := buildLargeCustomArg(3_700_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ep.parseCustomLogicExpression(expr)
	}
}
