package unit

import (
	"testing"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
	"esa/internal/app/models/esa_models"
	"esa/internal/app/service/sequence_service"
	"esa/internal/app/utility"

	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

// helpers -------------------------------------------------------------------

func rec(fields map[string]interface{}) map[string]interface{} { return fields }

func recordsObj(records ...map[string]interface{}) map[string]interface{} {
	arr := make([]interface{}, 0, len(records))
	for _, r := range records {
		arr = append(arr, r)
	}
	return map[string]interface{}{"records": arr}
}

func newProc() (*sequence_service.ExpressionProcessor, map[string]*models.EsaLog, map[string]string) {
	return sequence_service.NewExpressionProcessor(), map[string]*models.EsaLog{}, make(map[string]string)
}

// ---------------------------------------------------------------------------
// Numeric index resolution: <Object[N].Field>
// ---------------------------------------------------------------------------

func TestNumericIndex_ArrayAccessAndOrder(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 5}),
			rec(map[string]interface{}{"Clearance_Flag__c": false, "Delinquent_Days__c": 45}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 12}),
		),
	}}

	assert.Equal(t, "5", p.ProcessPlaceholders("<Payment_Schedules__c[0].Delinquent_Days__c>", dto, svc, cache, nil))
	assert.Equal(t, "45", p.ProcessPlaceholders("<Payment_Schedules__c[1].Delinquent_Days__c>", dto, svc, cache, nil))
	assert.Equal(t, "12", p.ProcessPlaceholders("<Payment_Schedules__c[2].Delinquent_Days__c>", dto, svc, cache, nil))
	assert.Equal(t, "true", p.ProcessPlaceholders("<Payment_Schedules__c[0].Clearance_Flag__c>", dto, svc, cache, nil))
	assert.Equal(t, "false", p.ProcessPlaceholders("<Payment_Schedules__c[1].Clearance_Flag__c>", dto, svc, cache, nil))
}

func TestNumericIndex_OutOfRangeIsBlank(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": 5}),
			rec(map[string]interface{}{"Delinquent_Days__c": 7}),
		),
	}}
	assert.Equal(t, "", p.ProcessPlaceholders("<Payment_Schedules__c[2].Delinquent_Days__c>", dto, svc, cache, nil))
	assert.Equal(t, "", p.ProcessPlaceholders("<Payment_Schedules__c[99].Delinquent_Days__c>", dto, svc, cache, nil))
}

func TestNumericIndex_EquivalentToSimpleAccessForFirstRecord(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": 5}),
			rec(map[string]interface{}{"Delinquent_Days__c": 7}),
		),
	}}
	simple := p.ProcessPlaceholders("<Payment_Schedules__c.Delinquent_Days__c>", dto, svc, cache, nil)
	indexed := p.ProcessPlaceholders("<Payment_Schedules__c[0].Delinquent_Days__c>", dto, svc, cache, nil)
	assert.Equal(t, simple, indexed)
	assert.Equal(t, "5", indexed)
}

func TestNumericIndex_CaseInsensitiveFieldName(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": 9}),
		),
	}}
	// Config uses different casing than the stored record key.
	assert.Equal(t, "9", p.ProcessPlaceholders("<Payment_Schedules__c[0].delinquent_days__c>", dto, svc, cache, nil))
}

func TestNumericIndex_BareMapSingleRecord(t *testing.T) {
	p, svc, cache := newProc()
	// Single-record object stored as a bare map (no "records" wrapper).
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"loan__c": {"Status__c": "Active"},
	}}
	assert.Equal(t, "Active", p.ProcessPlaceholders("<Loan__c[0].Status__c>", dto, svc, cache, nil))
	assert.Equal(t, "", p.ProcessPlaceholders("<Loan__c[1].Status__c>", dto, svc, cache, nil))
	// Equivalence with simple access.
	assert.Equal(t, "Active", p.ProcessPlaceholders("<Loan__c.Status__c>", dto, svc, cache, nil))
}

func TestNumericIndex_NegativeAndNonIntegerFallToFilterPath(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": 5}),
		),
	}}
	// Negative index is not a valid index -> treated as filter condition "-1" -> no match -> blank.
	assert.Equal(t, "", p.ProcessPlaceholders("<Payment_Schedules__c[-1].Delinquent_Days__c>", dto, svc, cache, nil))
	// Non-integer bracket with no operator -> no match -> blank (existing filter behavior).
	assert.Equal(t, "", p.ProcessPlaceholders("<Payment_Schedules__c[abc].Delinquent_Days__c>", dto, svc, cache, nil))
}

func TestNumericIndex_Float64AndNilValues(t *testing.T) {
	p, svc, cache := newProc()
	// Salesforce numbers arrive as float64 after JSON unmarshal; a null field is nil.
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": float64(5), "Amount__c": float64(1234567), "Note__c": nil}),
			rec(map[string]interface{}{"Delinquent_Days__c": float64(29.5)}),
		),
	}}
	assert.Equal(t, "5", p.ProcessPlaceholders("<Payment_Schedules__c[0].Delinquent_Days__c>", dto, svc, cache, nil))
	assert.Equal(t, "29.5", p.ProcessPlaceholders("<Payment_Schedules__c[1].Delinquent_Days__c>", dto, svc, cache, nil))
	// Large float64 must render in plain decimal, not scientific notation, and identically for
	// simple and indexed access (both go through navigateNestedField -> formatResolvedScalar).
	assert.Equal(t, "1234567", p.ProcessPlaceholders("<Payment_Schedules__c[0].Amount__c>", dto, svc, cache, nil))
	assert.Equal(t,
		p.ProcessPlaceholders("<Payment_Schedules__c.Amount__c>", dto, svc, cache, nil),
		p.ProcessPlaceholders("<Payment_Schedules__c[0].Amount__c>", dto, svc, cache, nil),
		"indexed access must match simple access formatting")
	// nil field resolves to blank (matches the simple-path contract).
	assert.Equal(t, "", p.ProcessPlaceholders("<Payment_Schedules__c[0].Note__c>", dto, svc, cache, nil))
	// float64 compares numerically in CONDITION.
	assert.Equal(t, "1", p.ProcessPlaceholders("{{CONDITION:<Payment_Schedules__c[1].Delinquent_Days__c> < 30:1:0}}", dto, svc, cache, nil))
}

func TestFilterCondition_StillWorks(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Installment_No__c": 1, "Amount__c": 100}),
			rec(map[string]interface{}{"Installment_No__c": 2, "Amount__c": 200}),
		),
	}}
	assert.Equal(t, "200", p.ProcessPlaceholders("<Payment_Schedules__c[Installment_No__c == 2].Amount__c>", dto, svc, cache, nil))
}

// ---------------------------------------------------------------------------
// Empty-operand relational comparison fix (CONDITION path)
// ---------------------------------------------------------------------------

func TestEmptyOperand_RelationalIsFalse(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Delinquent_Days__c": 5}),
		),
	}}
	// Out-of-range index -> blank operand -> relational must be false -> CONDITION picks "0".
	assert.Equal(t, "0", p.ProcessPlaceholders("{{CONDITION:<Payment_Schedules__c[2].Delinquent_Days__c> < 30:1:0}}", dto, svc, cache, nil))
	// Absent field -> blank -> false -> "0".
	assert.Equal(t, "0", p.ProcessPlaceholders("{{CONDITION:<Payment_Schedules__c[0].Missing__c> < 30:1:0}}", dto, svc, cache, nil))
	// Present numeric value still compares correctly.
	assert.Equal(t, "1", p.ProcessPlaceholders("{{CONDITION:<Payment_Schedules__c[0].Delinquent_Days__c> < 30:1:0}}", dto, svc, cache, nil))
}

func TestNonEmptyLexicalComparisonPreserved(t *testing.T) {
	p, svc, cache := newProc()
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"loan__c": {"Disbursed_Date__c": "2024-01-15"},
	}}
	// ISO date strings sort lexically; non-empty relational comparison must still work.
	assert.Equal(t, "1", p.ProcessPlaceholders("{{CONDITION:<Loan__c.Disbursed_Date__c> < '2024-06-01':1:0}}", dto, svc, cache, nil))
	assert.Equal(t, "0", p.ProcessPlaceholders("{{CONDITION:<Loan__c.Disbursed_Date__c> < '2023-06-01':1:0}}", dto, svc, cache, nil))
}

// ---------------------------------------------------------------------------
// End-to-end Vintage_3M style config
// ---------------------------------------------------------------------------

const vintage3M = "{{NUMERIC:{{CONDITION:<Payment_Schedules__c[0].Clearance_Flag__c> == true && <Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance_Flag__c> == true && <Payment_Schedules__c[1].Delinquent_Days__c> < 30 && <Payment_Schedules__c[2].Clearance_Flag__c> == true && <Payment_Schedules__c[2].Delinquent_Days__c> < 30:1:0}} || NUMERIC:0}}"

func TestVintage3M_EndToEnd(t *testing.T) {
	p, svc, cache := newProc()

	good := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 5}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 10}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 2}),
		),
	}}
	assert.Equal(t, "1", p.ProcessPlaceholders(vintage3M, good, svc, cache, nil))

	bad := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Clearance_Flag__c": false, "Delinquent_Days__c": 45}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 60}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 2}),
		),
	}}
	assert.Equal(t, "0", p.ProcessPlaceholders(vintage3M, bad, svc, cache, nil))

	// Fewer than 3 schedules: index [2] is out of range -> blank -> false -> 0 (no false positive).
	short := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"payment_schedules__c": recordsObj(
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 5}),
			rec(map[string]interface{}{"Clearance_Flag__c": true, "Delinquent_Days__c": 10}),
		),
	}}
	assert.Equal(t, "0", p.ProcessPlaceholders(vintage3M, short, svc, cache, nil))
}

// ---------------------------------------------------------------------------
// A3: evaluateFilterCondition (ARRAY:filter) empty-operand behavior
// ---------------------------------------------------------------------------

func TestArrayFilter_EmptyOperandExcluded(t *testing.T) {
	p, svc, cache := newProc()
	// Single record whose relational field is empty. Before the fix, "" < 30 was lexically true and
	// the record was kept; after the fix it is excluded, so the filtered array is empty.
	dto := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"accts__c": recordsObj(
			rec(map[string]interface{}{"Amt__c": ""}),
		),
	}}
	got := p.ProcessPlaceholders("{{CONDITION:{{ARRAY:<accts__c>:filter:Amt__c < 30}} != '[]':HAS:NONE}}", dto, svc, cache, nil)
	assert.Equal(t, "NONE", got)
}

// ---------------------------------------------------------------------------
// Extraction: fields inside <Object[N].Field> must be pulled into the SOQL query
// ---------------------------------------------------------------------------

func TestExtraction_NumericIndexPlaceholderFields(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID:          77,
		ServiceName: "VintageService",
		RequestBody: datatypes.JSON([]byte(`{
			"Vintage_3M": "{{NUMERIC:{{CONDITION:<Payment_Schedules__c[0].Clearance_Flag__c> == true && <Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance_Flag__c> == true && <Payment_Schedules__c[2].Delinquent_Days__c> < 30:1:0}} || NUMERIC:0}}"
		}`)),
	}

	objectsWithFields, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assert.Contains(t, objectsWithFields, "payment_schedules__c", "object referenced via [N] index must be queried")
	fields := objectsWithFields["payment_schedules__c"]
	assert.Contains(t, fields, "clearance_flag__c", "field accessed via [N] index must be added to the query")
	assert.Contains(t, fields, "delinquent_days__c", "field accessed via [N] index must be added to the query")
	// The numeric index itself must NOT be injected as a bogus field.
	assert.NotContains(t, fields, "0")
	assert.NotContains(t, fields, "1")
	assert.NotContains(t, fields, "2")
}
