package sequence_service

import (
	"encoding/json"
	"strings"
	"testing"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
)

// TestParsePickResponseFrom_RelationshipPath ensures a __r relationship path is parsed into
// object + full nested field path (not truncated at the first dot).
func TestParsePickResponseFrom_RelationshipPath(t *testing.T) {
	ac := `{"pre_execution": {"enabled": true, "pick_response_from": "<Multibureau_Consolidate_Data__c.Multibureau__r.Response__c>"}}`
	esaLog := models.NewEsaLog()
	esaLog.SetField("additional_config", ac)

	s := &SequenceService{}
	object, condition, field, ok := s.parsePickResponseFrom(esaLog)
	if !ok {
		t.Fatal("expected parsePickResponseFrom to succeed for relationship path")
	}
	if object != "Multibureau_Consolidate_Data__c" {
		t.Errorf("object = %q, want Multibureau_Consolidate_Data__c", object)
	}
	if condition != "" {
		t.Errorf("condition = %q, want empty for non-conditional expression", condition)
	}
	if field != "Multibureau__r.Response__c" {
		t.Errorf("field = %q, want Multibureau__r.Response__c", field)
	}
}

// TestParsePickResponseFrom_ConditionalRelationshipPath ensures an object-level [condition] filter
// (as used in the CRIF config) is parsed into object + condition + nested field path.
func TestParsePickResponseFrom_ConditionalRelationshipPath(t *testing.T) {
	ac := `{"pre_execution": {"pick_response_from": "<Multibureau_Consolidate_Data__c[Multibureau__r.Bureau__c == 'CRIF'].Multibureau__r.Response__c>"}}`
	esaLog := models.NewEsaLog()
	esaLog.SetField("additional_config", ac)

	s := &SequenceService{}
	object, condition, field, ok := s.parsePickResponseFrom(esaLog)
	if !ok {
		t.Fatal("expected parsePickResponseFrom to succeed for conditional relationship path")
	}
	if object != "Multibureau_Consolidate_Data__c" {
		t.Errorf("object = %q", object)
	}
	if condition != "Multibureau__r.Bureau__c == 'CRIF'" {
		t.Errorf("condition = %q, want Multibureau__r.Bureau__c == 'CRIF'", condition)
	}
	if field != "Multibureau__r.Response__c" {
		t.Errorf("field = %q, want Multibureau__r.Response__c", field)
	}
}

// TestResolvePickFieldValue_ConditionalSelectsMatchingRecord verifies the conditional pick selects
// the CRIF record among multiple bureau records (not records[0]) and returns its nested field value.
func TestResolvePickFieldValue_ConditionalSelectsMatchingRecord(t *testing.T) {
	s := &SequenceService{expressionProcessor: NewExpressionProcessor()}
	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"multibureau_consolidate_data__c": {
				"records": []interface{}{
					map[string]interface{}{"Multibureau__r": map[string]interface{}{"Bureau__c": "CIBIL", "Response__c": `{"b":"cibil"}`}},
					map[string]interface{}{"Multibureau__r": map[string]interface{}{"Bureau__c": "CRIF", "Response__c": `{"b":"crif"}`}},
				},
			},
		},
	}

	// Conditional: must pick the CRIF row (records[1]), not records[0].
	got := s.resolvePickFieldValue(masterDTO, "Multibureau_Consolidate_Data__c", "Multibureau__r.Bureau__c == 'CRIF'", "Multibureau__r.Response__c")
	if str, ok := got.(string); !ok || str != `{"b":"crif"}` {
		t.Fatalf("conditional pick => %#v, want the CRIF record's Response__c", got)
	}

	// No condition: falls back to records[0] (CIBIL).
	got0 := s.resolvePickFieldValue(masterDTO, "Multibureau_Consolidate_Data__c", "", "Multibureau__r.Response__c")
	if str, ok := got0.(string); !ok || str != `{"b":"cibil"}` {
		t.Fatalf("non-conditional pick => %#v, want records[0] (CIBIL)", got0)
	}
}

// TestResolvePickFieldValue_SkipsEmptyMatchKeepsScanning verifies that when two records match the
// condition but the first has an empty field, the later populated record wins (not the empty one).
func TestResolvePickFieldValue_SkipsEmptyMatchKeepsScanning(t *testing.T) {
	s := &SequenceService{expressionProcessor: NewExpressionProcessor()}
	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"multibureau_consolidate_data__c": {
				"records": []interface{}{
					map[string]interface{}{"Multibureau__r": map[string]interface{}{"Bureau__c": "CRIF", "Response__c": ""}},
					map[string]interface{}{"Multibureau__r": map[string]interface{}{"Bureau__c": "CRIF", "Response__c": `{"b":"crif2"}`}},
				},
			},
		},
	}
	got := s.resolvePickFieldValue(masterDTO, "Multibureau_Consolidate_Data__c", "Multibureau__r.Bureau__c == 'CRIF'", "Multibureau__r.Response__c")
	if str, ok := got.(string); !ok || str != `{"b":"crif2"}` {
		t.Fatalf("expected the second (populated) CRIF match, got %#v", got)
	}
}

// TestParsePickResponseFrom_MalformedConditionalRejected ensures a conditional with no trailing
// .Field (which would mis-split) is rejected rather than producing a garbage object name.
func TestParsePickResponseFrom_MalformedConditionalRejected(t *testing.T) {
	ac := `{"pre_execution": {"pick_response_from": "<Multibureau_Consolidate_Data__c[Multibureau__r.Bureau__c == 'CRIF']>"}}`
	esaLog := models.NewEsaLog()
	esaLog.SetField("additional_config", ac)

	s := &SequenceService{}
	if _, _, _, ok := s.parsePickResponseFrom(esaLog); ok {
		t.Fatal("expected ok=false for a conditional expression with no trailing .Field")
	}
}

// TestPickResponseFrom_NestedNavigationResolves verifies the fix: a nested relationship field
// is resolved via navigateNestedFieldRaw, whereas the old flat record[fieldName] lookup missed it.
func TestPickResponseFrom_NestedNavigationResolves(t *testing.T) {
	stored := `{"score": 742, "band": "A"}`
	record := map[string]interface{}{
		"Multibureau__r": map[string]interface{}{
			"Response__c": stored,
		},
	}

	ep := NewExpressionProcessor()

	// Old behaviour (flat key) — demonstrates why it was blank.
	if _, exists := record["Multibureau__r.Response__c"]; exists {
		t.Fatal("flat key should NOT exist for a nested relationship path")
	}

	// New behaviour (nested navigation).
	fieldParts := ep.splitPathWithArrayAccess(strings.Split("Multibureau__r.Response__c", "."))
	got := ep.navigateNestedFieldRaw(record, fieldParts)
	str, isStr := got.(string)
	if !isStr || str != stored {
		t.Fatalf("navigateNestedFieldRaw => %#v, want stored JSON string", got)
	}

	// The picked JSON string parses into the expected response body.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(str), &parsed); err != nil {
		t.Fatalf("picked value should be valid JSON: %v", err)
	}
	body := asResponseBodyMap(parsed)
	if body["band"] != "A" {
		t.Errorf("resolved body band = %v, want A", body["band"])
	}
}

// TestPickResponseFrom_FlatFieldStillResolves guards backward compatibility for simple fields.
func TestPickResponseFrom_FlatFieldStillResolves(t *testing.T) {
	record := map[string]interface{}{"Response__c": `{"ok": true}`}
	ep := NewExpressionProcessor()

	fieldParts := ep.splitPathWithArrayAccess(strings.Split("Response__c", "."))
	got := ep.navigateNestedFieldRaw(record, fieldParts)
	if s, ok := got.(string); !ok || s != `{"ok": true}` {
		t.Fatalf("flat field navigation => %#v, want the stored string", got)
	}
}
