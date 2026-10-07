package unit

import (
	"testing"

	"esa/internal/app/dto/common_dto"

	"github.com/stretchr/testify/assert"
)

// End-to-end coverage for the v2.4 `??` per-element default in ARRAY mappings, driven through
// ProcessPlaceholders so the full pipeline (source resolution → transform → JSON rendering) is
// exercised. Unit-level parser/evaluator coverage lives in
// internal/app/service/sequence_service/expression_processor_arraydefault_test.go.

func aScoreDTO() *common_dto.MasterDTO {
	return &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"a_score__c": recordsObj(
			rec(map[string]interface{}{"Type__c": "MSME_APP", "Decile__c": "7"}),
			rec(map[string]interface{}{"Type__c": "RETAIL_APP", "Decile__c": nil}),
		),
	}}
}

// The App_Deciles payload: a null Decile__c becomes the JSON number -999 instead of null.
func TestArrayElementDefault_AppDecilesPayload(t *testing.T) {
	p, svc, cache := newProc()

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Decile__c->value@NUMERIC??-999}}",
		aScoreDTO(), svc, cache, nil)

	assert.Equal(t, `[{"key":"MSME_APP","value":7},{"key":"RETAIL_APP","value":-999}]`, got)
}

// Without ??, the same config keeps its v2.3 output: the null stays null.
func TestArrayElementDefault_AbsentSuffixKeepsNull(t *testing.T) {
	p, svc, cache := newProc()

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Decile__c->value@NUMERIC}}",
		aScoreDTO(), svc, cache, nil)

	assert.Equal(t, `[{"key":"MSME_APP","value":7},{"key":"RETAIL_APP","value":null}]`, got)
}

// A quoted default survives the mapping-pair comma split.
func TestArrayElementDefault_QuotedDefaultWithComma(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"a_score__c": recordsObj(
			rec(map[string]interface{}{"Type__c": "MSME_APP", "Status__c": "OK"}),
			rec(map[string]interface{}{"Type__c": "RETAIL_APP", "Status__c": nil}),
		),
	}}

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Status__c->status??'N/A, unknown'}}",
		dto, svc, cache, nil)

	assert.Equal(t, `[{"key":"MSME_APP","status":"OK"},{"key":"RETAIL_APP","status":"N/A, unknown"}]`, got)
}

// `?? []`-style fallback interaction: the outer || fallback still handles the empty record set,
// and the ?? default never fires for it (the array stays an array, not a scalar).
func TestArrayElementDefault_EmptyRecordSetStillYieldsEmptyArray(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"a_score__c": recordsObj(),
	}}

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Decile__c->value@NUMERIC??-999}} || []",
		dto, svc, cache, nil)

	assert.Equal(t, `[]`, got)
}

// A service response whose VALUES contain "??" must flow through an ARRAY transform untouched.
// This is the end-to-end version of the concern: only the config spec is scanned for the `??`
// operator, so response data can never drop or corrupt a mapping.
func TestArrayElementDefault_ServiceResponseDataContainingDoubleQuestionMark(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"a_score__c": recordsObj(
			rec(map[string]interface{}{"Type__c": "??", "Note__c": "a??b"}),
			rec(map[string]interface{}{"Type__c": "OK", "Note__c": nil}),
		),
	}}

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Note__c->note??'MISSING'}}",
		dto, svc, cache, nil)

	assert.Equal(t, `[{"key":"??","note":"a??b"},{"key":"OK","note":"MISSING"}]`, got)
}

// A "??" inside a quoted static literal in the SPEC is data too: the literal is injected and the
// sibling mappings survive. Before quote awareness this dropped the whole `'??'->tag` pair.
func TestArrayElementDefault_QuotedDoubleQuestionMarkInSpec(t *testing.T) {
	p, svc, cache := newProc()

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:'??'->tag,Type__c->key,Decile__c->value@NUMERIC??-999}}",
		aScoreDTO(), svc, cache, nil)

	assert.Equal(t,
		`[{"key":"MSME_APP","tag":"??","value":7},{"key":"RETAIL_APP","tag":"??","value":-999}]`,
		got)
}

// A string sentinel on a @NUMERIC field survives as a string instead of being cast away to null.
// The populated element still becomes a JSON number, so only the defaulted element changes type.
func TestArrayElementDefault_CrossTypeStringOnNumericField(t *testing.T) {
	p, svc, cache := newProc()

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Decile__c->value@NUMERIC??'N/A'}}",
		aScoreDTO(), svc, cache, nil)

	assert.Equal(t, `[{"key":"MSME_APP","value":7},{"key":"RETAIL_APP","value":"N/A"}]`, got)
}

// A numeric sentinel on a @BOOLEAN field survives as a number instead of collapsing to false.
func TestArrayElementDefault_CrossTypeNumericOnBooleanField(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"a_score__c": recordsObj(
			rec(map[string]interface{}{"Type__c": "MSME_APP", "Verified__c": "true"}),
			rec(map[string]interface{}{"Type__c": "RETAIL_APP", "Verified__c": nil}),
		),
	}}

	got := p.ProcessPlaceholders(
		"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Verified__c->flag@BOOLEAN??-999}}",
		dto, svc, cache, nil)

	assert.Equal(t, `[{"flag":true,"key":"MSME_APP"},{"flag":-999,"key":"RETAIL_APP"}]`, got)
}
