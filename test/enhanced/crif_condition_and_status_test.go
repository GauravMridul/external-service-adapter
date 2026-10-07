// Package main_test contains enhanced, behavior-level tests for the ESA expression engine.
//
// This file covers the CRIF Bureau_Score -998 investigation fixes end-to-end via the public
// ExpressionProcessor API:
//
//   - BUG-1: findOperatorPosition is quote-aware, so a CONDITION gate whose operand expands to a
//     large service body (JSON with stray/unbalanced parentheses inside quoted string values) is
//     evaluated correctly instead of silently taking the false branch.
//   - BUG-2: a CONDITION whose condition genuinely cannot be evaluated (a comparison operator that
//     cannot be located because unbalanced parentheses from a raw, unquoted value trap it) resolves
//     to blank, while operator-less / bare conditions keep their legacy false-branch behavior.
//   - ENH-2: the reserved ((Service.__status)) and ((Service.__statusCode)) accessors expose a small,
//     reliable success signal that configs can gate on.
package main_test

import (
	"testing"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
	"esa/internal/app/service/sequence_service"

	"github.com/stretchr/testify/assert"
)

// acticoServiceMap returns a service map that mirrors the production shape: the Actico decision
// engine ("acticoData") carries the parsed bureau block at body.crif.features.bureau_score, and the
// raw CRIF service ("CRIF") stores its (large) raw_response string whose values contain stray
// parentheses / pipes exactly like a real bureau address field.
func acticoServiceMap(crifStatus string, crifStatusCode int, includeCRIF bool) map[string]*models.EsaLog {
	m := map[string]*models.EsaLog{
		"acticoData": {
			ServiceId:   2,
			ServiceName: "acticoData",
			Status:      "COMPLETED",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"body": map[string]interface{}{
						"crif": map[string]interface{}{
							"features": map[string]interface{}{
								// Note lowercase key; config references Bureau_Score (case-insensitive match).
								"bureau_score": 745,
							},
						},
					},
				},
			},
		},
	}
	if includeCRIF {
		m["CRIF"] = &models.EsaLog{
			ServiceId:   101,
			ServiceName: "CRIF",
			Status:      crifStatus,
			Response: models.ResponseDetails{
				StatusCode: crifStatusCode,
				Body: map[string]interface{}{
					// Real-world style: stray '(' with no ')', embedded '||' and colons — all inside
					// a quoted JSON string value once the whole body is marshaled for ((CRIF)).
					"raw_response": `{"CIR":{"ADDRESSES":[{"ADDRESSTEXT":"NO:1/1, SAMPLE STREET( SF NO:0, D NO:1/1 || 600001 TN"}]}}`,
				},
			},
		}
	}
	return m
}

// TestBug1_ConditionGateOnWholeServiceBodyResolvesValue verifies the CRIF Bureau_Score fix.
// Config 1 (direct read) and Config 2 (guarded by ((CRIF)) != ”) must both yield 745. Before the
// quote-aware findOperatorPosition fix, Config 2 returned -998 because the '!=' operator could not
// be located past the unbalanced '(' inside the marshaled CRIF body.
func TestBug1_ConditionGateOnWholeServiceBodyResolvesValue(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	svc := acticoServiceMap("COMPLETED", 200, true)
	dto := &common_dto.MasterDTO{}

	config1 := `{{NUMERIC:((acticoData.body.crif.features.Bureau_Score)) || NUMERIC:0}}`
	config2 := `{{NUMERIC:{{CONDITION:((CRIF)) != '':((acticoData.body.crif.features.Bureau_Score)):-998}} || NUMERIC:0}}`

	assert.Equal(t, "745", p.ProcessPlaceholders(config1, dto, svc, nil, nil),
		"direct read of the bureau score should be 745")
	assert.Equal(t, "745", p.ProcessPlaceholders(config2, dto, svc, nil, nil),
		"gated read must resolve the true branch (745), not the -998 false branch")
}

// TestBug1_GateFalseWhenServiceAbsent confirms the gate still fails closed: when CRIF is not in the
// resolution scope, ((CRIF)) is empty, ” != ” is false, and the configured -998 is returned.
func TestBug1_GateFalseWhenServiceAbsent(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	svc := acticoServiceMap("", 0, false) // no CRIF
	dto := &common_dto.MasterDTO{}

	config2 := `{{NUMERIC:{{CONDITION:((CRIF)) != '':((acticoData.body.crif.features.Bureau_Score)):-998}} || NUMERIC:0}}`
	assert.Equal(t, "-998", p.ProcessPlaceholders(config2, dto, svc, nil, nil),
		"absent CRIF -> gate false -> -998")
}

// TestBug2_UnevaluableConditionResolvesToBlank exercises the BUG-2 path end-to-end. The service
// field resolves to a raw, unquoted text value containing an unbalanced '(' (like a bureau address
// dropped directly into a condition). That traps operator detection so the '!=' cannot be located;
// the condition has genuine comparison intent, so the whole CONDITION resolves to blank instead of
// silently taking the false branch.
func TestBug2_UnevaluableConditionResolvesToBlank(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	svc := map[string]*models.EsaLog{
		"CRIF": {
			ServiceName: "CRIF",
			Status:      "COMPLETED",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					// Plain text (no surrounding quotes) with an unmatched '(' — traps bracket depth.
					"addressText": "NO 1/1 SAMPLE STREET( SF NO 0 D NO 1/1 TN",
				},
			},
		},
	}
	dto := &common_dto.MasterDTO{}

	expr := `{{CONDITION:((CRIF.addressText)) != '':FOUND:MISSING}}`
	assert.Equal(t, "", p.ProcessPlaceholders(expr, dto, svc, nil, nil),
		"unevaluable condition (operator trapped by unbalanced '(') must resolve to blank, not the false branch")
}

// TestBug2_BareAndNormalConditionsPreserveLegacyBehavior guards against over-reach: a bare/
// operator-less condition (a nested expression that resolved to empty) and a normal false comparison
// must still take the false branch, and a normal true comparison the true branch.
func TestBug2_BareAndNormalConditionsPreserveLegacyBehavior(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	dto := &common_dto.MasterDTO{}
	svc := map[string]*models.EsaLog{}

	// Bare condition: unknown/empty CUSTOM resolves to "" -> no comparison intent -> legacy false branch.
	assert.Equal(t, "EXECUTE",
		p.ProcessPlaceholders(`{{CONDITION:{{CUSTOM:notARealFunction:x}}:CONTINUE:EXECUTE}}`, dto, svc, nil, nil),
		"bare (operator-less) empty condition should take the false branch, unchanged")

	// Normal comparisons are unaffected.
	assert.Equal(t, "NO",
		p.ProcessPlaceholders(`{{CONDITION:'a' == 'b':YES:NO}}`, dto, svc, nil, nil),
		"definite false comparison -> false branch")
	assert.Equal(t, "YES",
		p.ProcessPlaceholders(`{{CONDITION:'a' == 'a':YES:NO}}`, dto, svc, nil, nil),
		"definite true comparison -> true branch")
}

// TestEnh2_ReservedStatusAccessors verifies ((Service.__status)) and ((Service.__statusCode)).
func TestEnh2_ReservedStatusAccessors(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	dto := &common_dto.MasterDTO{}
	svc := acticoServiceMap("COMPLETED", 200, true)

	assert.Equal(t, "COMPLETED", p.ProcessPlaceholders(`((CRIF.__status))`, dto, svc, nil, nil),
		"__status exposes the recorded service status")
	assert.Equal(t, "200", p.ProcessPlaceholders(`((CRIF.__statusCode))`, dto, svc, nil, nil),
		"__statusCode exposes the recorded HTTP status code")

	// Absent service -> blank (so == 'COMPLETED' is a safe affirmative gate).
	svcNoCRIF := acticoServiceMap("", 0, false)
	assert.Equal(t, "", p.ProcessPlaceholders(`((CRIF.__status))`, dto, svcNoCRIF, nil, nil),
		"absent service -> blank status")
}

// TestEnh2_StatusGateSelectsValueOrSentinel shows the recommended reliable gate: use the value when
// CRIF completed, otherwise the -998 sentinel.
func TestEnh2_StatusGateSelectsValueOrSentinel(t *testing.T) {
	p := sequence_service.NewExpressionProcessor()
	dto := &common_dto.MasterDTO{}
	gate := `{{NUMERIC:{{CONDITION:((CRIF.__status)) == 'COMPLETED':((acticoData.body.crif.features.Bureau_Score)):-998}} || NUMERIC:0}}`

	assert.Equal(t, "745", p.ProcessPlaceholders(gate, dto, acticoServiceMap("COMPLETED", 200, true), nil, nil),
		"CRIF completed -> use bureau score")
	assert.Equal(t, "-998", p.ProcessPlaceholders(gate, dto, acticoServiceMap("", 0, false), nil, nil),
		"CRIF absent/failed -> -998 sentinel")
}
