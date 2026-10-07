package sequence_service

import (
	"fmt"
	"testing"

	"esa/internal/app/dto/common_dto"
)

func TestArithRegressions(t *testing.T) {
	ep := NewExpressionProcessor()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	cases := []struct{ expr, want string }{
		// HIGH: literal slash division not swallowed as date
		{"{{CALC:10/2}}", "5"},
		{"{{CALC:12/25}}", "0.48"},
		{"{{CALC:3/4}}", "0.75"},
		{"{{CALC:2024-01}}", "2023"},
		// Full ISO date literal still guarded
		{"{{CALC:2024-01-15}}", ""},
		// dash-like arithmetic still works in CALC (multi-operand)
		{"{{CALC:100-200-300}}", "-400"},
		// MEDIUM: general (non-CALC) path stays legacy 2-operand -> 3-dash value not coerced to a number
		{"{{1234-5678-9012}}", ""}, // legacy: 3-operand split unsupported -> empty (not "-13456")
		// LOW: unknown function -> empty (not 0)
		{"{{CALC:FOO(1) + 2}}", ""},
		// grouping still works
		{"{{CALC:2 * (3 + 4)}}", "14"},
		{"{{CALC:((TestSvcMissing.x)) + 1}}", "1"}, // missing placeholder -> 0, +1
	}
	for _, c := range cases {
		got := ep.ProcessPlaceholders(c.expr, dto, nil, map[string]string{}, nil)
		status := "OK"
		if got != c.want {
			status = "MISMATCH"
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
		fmt.Printf("[%s] %-30s => %q\n", status, c.expr, got)
	}
}
