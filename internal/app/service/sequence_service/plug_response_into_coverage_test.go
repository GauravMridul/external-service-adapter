package sequence_service

import (
	"testing"

	"esa/internal/app/models"

	"github.com/stretchr/testify/assert"
)

// TestPlugResponseInto_CRIFConfig covers the plug_response_into / apply_to construct present in the
// production DB (service "CRIF", id 168) but previously unexercised by the test suite. It is a
// white-box test (package sequence_service) because parsePlugResponseInto and plugWalk are
// unexported and therefore unreachable from the external enhanced harness.
//
// Verbatim additional_config from the DB:
//
//	{"pre_execution": {...},
//	 "plug_response_into": {"apply_to": ["pick_response_from"],
//	                        "template": {"raw_response": "<Actual_response_json>"}}}
func TestPlugResponseInto_CRIFConfig(t *testing.T) {
	s := &SequenceService{}
	esaLog := models.NewEsaLog()
	esaLog.SetField("additional_config", `{"pre_execution": {"enabled": false, "validations": ["{{CONDITION:{{CUSTOM:dateWithinDays:<Multibureau_Consolidate_Data__c[Multibureau__r.Bureau__c == 'CRIF'].Multibureau_Date__c>:30}} == 'true':CONTINUE:EXECUTE}}"], "pick_response_from": "<Multibureau_Consolidate_Data__c[Multibureau__r.Bureau__c == 'CRIF'].Multibureau__r.Response__c>"}, "plug_response_into": {"apply_to": ["pick_response_from"], "template": {"raw_response": "<Actual_response_json>"}}}`)

	template, applyTo, ok := s.parsePlugResponseInto(esaLog)
	assert.True(t, ok, "plug_response_into should be parsed")

	// apply_to restricts wrapping to the pick_response_from source only.
	assert.True(t, applyTo[responseSourcePickResponseFrom], "pick_response_from must be allow-listed")
	assert.False(t, applyTo[responseSourceAPICall], "api_call must NOT be allow-listed (apply_to restricts it)")

	// Template shape is preserved.
	tmplMap, isMap := template.(map[string]interface{})
	assert.True(t, isMap, "template root must be a JSON object")
	assert.Equal(t, plugResponseMarkerJSON, tmplMap["raw_response"], "template marker preserved before wrapping")

	// Wrapping a response body: the <Actual_response_json> marker is replaced by the STRINGIFIED body.
	body := map[string]interface{}{"score": 750}
	wrapped := plugWalk(template, body)
	wrappedMap, ok := wrapped.(map[string]interface{})
	assert.True(t, ok, "wrapped result must be a JSON object")
	assert.Equal(t, `{"score":750}`, wrappedMap["raw_response"], "response body must be embedded as a stringified JSON at raw_response")
}

// TestPlugResponseInto_RootLevelAndDefaultApplyTo covers the root-level bare-template form and the
// default apply_to set (used when apply_to is omitted), the other structural variant supported by
// parsePlugResponseInto.
func TestPlugResponseInto_RootLevelAndDefaultApplyTo(t *testing.T) {
	s := &SequenceService{}
	esaLog := models.NewEsaLog()
	esaLog.SetField("additional_config", `{"plug_response_into": {"custom_response_key": "<Actual_response>"}}`)

	template, applyTo, ok := s.parsePlugResponseInto(esaLog)
	assert.True(t, ok)
	// No apply_to provided -> default set includes all successful sources.
	assert.True(t, applyTo[responseSourceAPICall])
	assert.True(t, applyTo[responseSourcePickResponseFrom])

	// Exact <Actual_response> (object marker) is replaced by the body value itself (not stringified).
	body := map[string]interface{}{"a": 1}
	wrapped := plugWalk(template, body)
	wrappedMap := wrapped.(map[string]interface{})
	assert.Equal(t, body, wrappedMap["custom_response_key"], "object marker replaced with the body object itself")
}
