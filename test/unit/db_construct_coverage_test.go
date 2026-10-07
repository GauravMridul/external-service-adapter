package unit

import (
	"testing"

	"esa/internal/app/dto/common_dto"

	"github.com/stretchr/testify/assert"
)

// dbConstructDTO provides the data referenced by the previously-uncovered DB constructs.
func dbConstructDTO() *common_dto.MasterDTO {
	return &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"contact": {
			"Office_Address__c":    "Plot 5, MG Road, Bengaluru 560001",
			"Office_Pincode__c":    "560001",
			"Permanent_Address__c": "12 Residency Rd, Pune 411001",
		},
		"lead": {
			"Sector__c": "Salaried",
		},
		"multibureau_idlist__c": recordsObj(
			rec(map[string]interface{}{"Id_Value__c": "ABCDE1234F", "Iden_Type__c": "PAN"}),
			rec(map[string]interface{}{"Id_Value__c": "123412341234", "Iden_Type__c": "Aadhaar"}),
			rec(map[string]interface{}{"Id_Value__c": "ABCDE1234F", "Iden_Type__c": "PAN"}),
		),
	}}
}

// TestDbConstruct_Unmapped documents that an {{UNMAPPED:...}} runtime-only token resolves to "0".
func TestDbConstruct_Unmapped(t *testing.T) {
	p, svc, cache := newProc()
	out := p.ProcessPlaceholders(`{{UNMAPPED:DL_Status - runtime DL verification API response}}`, dbConstructDTO(), svc, cache, nil)
	assert.Equal(t, "0", out)
}

// TestDbConstruct_JoinDistinctField covers the IMPLEMENTED joinDistinctField: distinct values of the
// target field, filtered by (Iden_Type__c == PAN), joined by ", ".
func TestDbConstruct_JoinDistinctField(t *testing.T) {
	p, svc, cache := newProc()
	out := p.ProcessPlaceholders(`{{CUSTOM:joinDistinctField:{{ARRAY:<MultiBureau_IdList__c>:map:Id_Value__c,Iden_Type__c}}:Id_Value__c:, :Iden_Type__c:PAN}}`, dbConstructDTO(), svc, cache, nil)
	assert.Equal(t, "ABCDE1234F", out)
}

// TestDbConstruct_PincodeCustomFns covers the IMPLEMENTED extractPincode (and its getOfficePincode
// alias) against the verbatim shapes used by the production DB configs.
func TestDbConstruct_PincodeCustomFns(t *testing.T) {
	p, svc, cache := newProc()
	dto := dbConstructDTO()

	assert.Equal(t, "560001", p.ProcessPlaceholders(`{{CUSTOM:getOfficePincode:<Contact.Office_Address__c>:<Contact.Office_Pincode__c>}}`, dto, svc, cache, nil))
	assert.Equal(t, "411001", p.ProcessPlaceholders(`{{CUSTOM:extractPincode:<Contact.Permanent_Address__c>}}`, dto, svc, cache, nil))
}

// TestDbConstruct_UnimplementedCustomFns documents current resolver behavior for CUSTOM functions
// referenced by DB configs that are NOT implemented in the resolver: they return "".
// This is a characterization test — if any of these is implemented later, this test will fail and
// force a deliberate expectation update.
func TestDbConstruct_UnimplementedCustomFns(t *testing.T) {
	p, svc, cache := newProc()
	dto := dbConstructDTO()

	assert.Equal(t, "", p.ProcessPlaceholders(`{{CUSTOM:equals:<Lead.Sector__c>:Salaried}}`, dto, svc, cache, nil),
		"equals is not implemented in the resolver")
}

// TestDbConstruct_EqualsInsideCondition documents the downstream effect of the unimplemented equals
// inside a CONDITION: the empty result is falsy, so the else branch (EXECUTE) is taken regardless of
// the Sector value. Flagged: the config's intended gating is currently a no-op.
func TestDbConstruct_EqualsInsideCondition(t *testing.T) {
	p, svc, cache := newProc()
	out := p.ProcessPlaceholders(`{{CONDITION:{{CUSTOM:equals:<Lead.Sector__c>:Salaried}}:CONTINUE:EXECUTE}}`, dbConstructDTO(), svc, cache, nil)
	assert.Equal(t, "EXECUTE", out)
}
