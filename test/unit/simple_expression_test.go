package unit

import (
	"strings"
	"testing"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
	"esa/internal/app/service/sequence_service"

	"github.com/stretchr/testify/assert"
)

// TestSimpleExpressionProcessing tests basic expression processing functionality
func TestSimpleExpressionProcessing(t *testing.T) {
	// Create expression processor
	processor := sequence_service.NewExpressionProcessor()

	// Create test data
	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Name":         "John Michael Doe",
				"MailingState": "Maharashtra",
				"gender__c":    "M",
				"PAN_ID__c":    "ABCDE1234F",
			},
		},
	}

	// Create service map
	serviceMap := map[string]*models.EsaLog{
		"service1": {
			ServiceName: "TestService1",
		},
	}

	// Create placeholder cache
	placeholderCache := make(map[string]string)

	// Test basic placeholder resolution
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple field access",
			input:    "<contact.Name>",
			expected: "John Michael Doe",
		},
		{
			name:     "State field access",
			input:    "<contact.MailingState>",
			expected: "Maharashtra",
		},
		{
			name:     "Gender field access",
			input:    "<contact.gender__c>",
			expected: "M",
		},
		{
			name:     "PAN field access",
			input:    "<contact.PAN_ID__c>",
			expected: "ABCDE1234F",
		},
		{
			name:     "Literal text",
			input:    "Hello World",
			expected: "Hello World",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := processor.ProcessPlaceholders(tc.input, masterDTO, serviceMap, placeholderCache, nil)
			assert.Equal(t, tc.expected, result, "Test case: %s", tc.name)
		})
	}
}

// TestExpressionProcessingWithFallback tests fallback functionality
func TestExpressionProcessingWithFallback(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Name":         "John Doe",
				"MailingState": "Maharashtra",
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{}
	placeholderCache := make(map[string]string)

	// Test fallback scenarios
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Fallback to existing field",
			input:    "<contact.NonExistent> || <contact.Name>",
			expected: "John Doe",
		},
		{
			name:     "Fallback to literal",
			input:    "<contact.NonExistent> || Default Value",
			expected: "Default Value",
		},
		{
			name:     "Multiple fallbacks",
			input:    "<contact.NonExistent1> || <contact.NonExistent2> || <contact.MailingState>",
			expected: "Maharashtra",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := processor.ProcessPlaceholders(tc.input, masterDTO, serviceMap, placeholderCache, nil)
			assert.Equal(t, tc.expected, result, "Test case: %s", tc.name)
		})
	}
}

// TestComplexExpressions tests more complex expression scenarios
func TestComplexExpressions(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Name":         "John Michael Doe",
				"MailingState": "Maharashtra",
				"gender__c":    "M",
				"PAN_ID__c":    "ABCDE1234F",
			},
			"lead": {
				"Amount_in_Rs__c": "1000000",
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{}
	placeholderCache := make(map[string]string)

	// Test complex expressions (if they work with current processor)
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Concatenation with spaces",
			input:    "<contact.Name> from <contact.MailingState>",
			expected: "John Michael Doe from Maharashtra",
		},
		{
			name:     "Multiple field access",
			input:    "Name: <contact.Name>, State: <contact.MailingState>",
			expected: "Name: John Michael Doe, State: Maharashtra",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := processor.ProcessPlaceholders(tc.input, masterDTO, serviceMap, placeholderCache, nil)
			assert.Equal(t, tc.expected, result, "Test case: %s", tc.name)
		})
	}
}

// TestEmptyAndNullValues tests handling of empty and null values
func TestEmptyAndNullValues(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Name":       "John Doe",
				"EmptyField": "",
				"NullField":  nil,
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{}
	placeholderCache := make(map[string]string)

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Non-existent field",
			input:    "<contact.NonExistent>",
			expected: "", // Should return empty string for non-existent fields
		},
		{
			name:     "Empty field",
			input:    "<contact.EmptyField>",
			expected: "",
		},
		{
			name:     "Null field",
			input:    "<contact.NullField>",
			expected: "",
		},
		{
			name:     "Fallback from empty",
			input:    "<contact.EmptyField> || Fallback Value",
			expected: "Fallback Value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := processor.ProcessPlaceholders(tc.input, masterDTO, serviceMap, placeholderCache, nil)
			assert.Equal(t, tc.expected, result, "Test case: %s", tc.name)
		})
	}
}

func TestLeadSourcingProgramFallbackToQuotedNull(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	testCases := []struct {
		name       string
		fieldValue interface{}
		expected   string
	}{
		{
			name:       "blank string falls back to literal null",
			fieldValue: "",
			expected:   "null",
		},
		{
			name:       "nil falls back to literal null",
			fieldValue: nil,
			expected:   "null",
		},
		{
			name:       "non-empty value wins over fallback",
			fieldValue: "Offline Store",
			expected:   "Offline Store",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			masterDTO := &common_dto.MasterDTO{
				Data: map[string]map[string]interface{}{
					"lead": {
						"Sourcing_Program__c": tc.fieldValue,
					},
				},
			}

			result := processor.ProcessPlaceholders("<lead.Sourcing_Program__c>|| 'null'", masterDTO, nil, nil, nil)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestLeadSourcingProgramFallbackToQuotedNullTypedMode(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"lead": {
				"Sourcing_Program__c": "",
			},
		},
	}

	result := processor.ProcessPlaceholdersWithType("<lead.Sourcing_Program__c>|| 'null'", sequence_service.TypeModeTyped, masterDTO, nil, nil, nil)
	assert.Equal(t, "null", result)
}

func TestCustomStringifyJSONSerializesServiceObjects(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	serviceMap := map[string]*models.EsaLog{
		"BankStatementService": {
			ServiceName: "BankStatementService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"data": map[string]interface{}{
						"traceId":   "trace-123",
						"status":    "COMPLETED",
						"dataRange": map[string]interface{}{"fromDate": "2025-01-12", "toDate": "2026-01-12"},
						"fips": []interface{}{
							map[string]interface{}{
								"fipID": "BANK_1",
								"accounts": []interface{}{
									map[string]interface{}{"maskedAccNumber": "XXXX4405"},
								},
							},
						},
					},
				},
			},
		},
	}

	result := processor.ProcessPlaceholders("{{CUSTOM:stringifyJSON:((BankStatementService.data))}}", &common_dto.MasterDTO{}, serviceMap, nil, nil)
	assert.NotContains(t, result, "map[")
	assert.JSONEq(t, `{"traceId":"trace-123","status":"COMPLETED","dataRange":{"fromDate":"2025-01-12","toDate":"2026-01-12"},"fips":[{"fipID":"BANK_1","accounts":[{"maskedAccNumber":"XXXX4405"}]}]}`, result)
}

// TestPreExecutionCIBILAccountsValidation validates the pre_execution condition used for
// MobileUANService: when CIBIL has consumerCreditData as array (real API shape), the path
// CIBIL.consumerCreditData[0].accounts must resolve to the accounts array, and the
// CONDITION must return CONTINUE when at least one account has accountType in the allowed list.
//
// In production: (1) Use path consumerCreditData[0].accounts (not consumerCreditData.accounts).
// (2) Run CIBIL before MobileUAN so CIBIL response is available when pre-exec runs for 12;
//
//	e.g. sequenceString "{4};{12}" (two groups), not "{4,12}" (same group).
func TestPreExecutionCIBILAccountsValidation(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	// Real CIBIL response shape: consumerCreditData is an array of consumer objects;
	// each has "accounts" array. One account with accountType 52 (in allowed list).
	cibilBody := map[string]interface{}{
		"consumerCreditData": []interface{}{
			map[string]interface{}{
				"accounts": []interface{}{
					map[string]interface{}{"accountType": "06", "currentBalance": float64(42721)},
					map[string]interface{}{"accountType": "52", "currentBalance": float64(6028), "emiAmount": float64(6361)},
					map[string]interface{}{"accountType": "05", "currentBalance": float64(146864)},
				},
				"addresses": []interface{}{},
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{
		"CIBIL": {
			ServiceId:   4,
			ServiceName: "CIBIL",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body:       cibilBody,
			},
		},
	}

	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	placeholderCache := make(map[string]string)

	// Exact pre_execution validation from config: filtered array != '[]' -> CONTINUE, else EXECUTE
	expr := `{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':CONTINUE:EXECUTE}}`

	result := processor.ProcessPlaceholders(expr, masterDTO, serviceMap, placeholderCache, nil)
	result = strings.TrimSpace(strings.ToUpper(result))

	assert.Equal(t, "CONTINUE", result,
		"Pre-execution should evaluate to CONTINUE when CIBIL has accountType 52 in consumerCreditData[0].accounts; got %q", result)
}

// TestPreExecutionCIBILAccountsOldPathFails confirms that without [0] the path resolves
// incorrectly (first consumer object instead of accounts array), so the condition yields EXECUTE.
func TestPreExecutionCIBILAccountsOldPathFails(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	// Real API shape: consumerCreditData is array of consumer objects
	cibilBody := map[string]interface{}{
		"consumerCreditData": []interface{}{
			map[string]interface{}{
				"accounts": []interface{}{
					map[string]interface{}{"accountType": "52", "currentBalance": float64(6028)},
				},
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{
		"CIBIL": {
			ServiceId:   4,
			ServiceName: "CIBIL",
			Response:    models.ResponseDetails{StatusCode: 200, Body: cibilBody},
		},
	}

	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	placeholderCache := make(map[string]string)

	// OLD path (no [0]): resolves to first consumer object, not accounts array -> ARRAY gets non-array -> empty -> EXECUTE
	exprOld := `{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData.accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':CONTINUE:EXECUTE}}`

	resultOld := processor.ProcessPlaceholders(exprOld, masterDTO, serviceMap, placeholderCache, nil)
	resultOld = strings.TrimSpace(strings.ToUpper(resultOld))
	assert.Equal(t, "EXECUTE", resultOld,
		"Old path (no [0]) must yield EXECUTE because path does not resolve to accounts array; got %q", resultOld)
}

// TestPreExecutionCIBILMissingFromServiceMapYieldsEXECUTE ensures that when CIBIL is not in
// serviceNameMap (e.g. only COMPLETED services are included and CIBIL has not run yet),
// ((CIBIL.consumerCreditData[0].accounts)) resolves to empty, so the condition yields EXECUTE.
// This protects the evaluateExecutionExpressions behaviour: serviceNameMap contains only
// COMPLETED services, so a missing service correctly leads to EXECUTE (run the current service).
func TestPreExecutionCIBILMissingFromServiceMapYieldsEXECUTE(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	// Empty service map: no CIBIL (e.g. PreExecution for first service or CIBIL not COMPLETED)
	serviceMap := map[string]*models.EsaLog{}

	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	placeholderCache := make(map[string]string)

	expr := `{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':CONTINUE:EXECUTE}}`

	result := processor.ProcessPlaceholders(expr, masterDTO, serviceMap, placeholderCache, nil)
	result = strings.TrimSpace(strings.ToUpper(result))

	assert.Equal(t, "EXECUTE", result,
		"When CIBIL is not in serviceNameMap, ((CIBIL...)) resolves empty so condition should yield EXECUTE; got %q", result)
}

// Validations from MobileUANService pre_execution config (exact strings)
var mobileUANPreExecValidations = []struct {
	Index int
	Expr  string
	Name  string
}{
	{0, `{{CONDITION:<Lead.LoanCategory__c> IN ('TopUp Loan', 'Repeat'):CONTINUE:EXECUTE}}`, "LoanCategory in TopUp/Repeat → skip"},
	{1, `{{CONDITION:<Contact.Id> == '' || <Contact.Id> == null:CONTINUE:EXECUTE}}`, "No Contact.Id → skip"},
	{2, `{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('Self Employed','Self Employed Professional','SENP','SEP','self employed professional','senp','sep','self employed'):CONTINUE:EXECUTE}}`, "Self-employed → skip"},
	{3, `{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':CONTINUE:EXECUTE}}`, "CIBIL has allowed account types → skip"},
	{4, `{{CONDITION:<Lead.SourcingProgram_c> IN ('Offline Store', 'QRO2O'):CONTINUE:EXECUTE}}`, "Sourcing program Offline/QRO2O → skip"},
	{5, `{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:{{TRANSFORM:{{CUSTOM:cleanSpecialChars:<Contact.MobilePhone> || <Contact.Phone>}}:remove_spaces}}:10}}:length}} == 10:EXECUTE:CONTINUE}}`, "10-digit mobile → EXECUTE, else skip"},
}

// TestPreExecutionAllValidationsWithRealData runs all 6 pre_execution validations with
// the same request/response shape as production (from user's data). It reports each
// validation's result so we can see which condition(s) yield CONTINUE vs EXECUTE.
func TestPreExecutionAllValidationsWithRealData(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()

	// data.contact and data.lead from user's response (Untitled-2)
	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Id":                "0039H00000GqU5yQAF",
				"Name":              "Appisetty Sandhya",
				"MobilePhone":       "9000000002",
				"Phone":             "9000000002",
				"PAN_ID__c":         "BFFFP6666F",
				"Gender__c":         "Female",
				"Birthdate":         "1994-06-25",
				"MailingState":      "Karnataka",
				"MailingStreet":     "12 th cross,Indira nagar...",
				"MailingPostalCode": "560068",
				// employmentType__c not present in response → condition 3 gets empty
			},
			"lead": {
				"Id":               "00Q9H00000FK6dLUAT",
				"Business_Type__c": "Personal Loan",
				// LoanCategory__c, SourcingProgram_c not in response → conditions 1, 5 get empty
			},
		},
	}

	// CIBIL response: consumerCreditData as array (real API shape), one account with accountType 52
	cibilBody := map[string]interface{}{
		"consumerCreditData": []interface{}{
			map[string]interface{}{
				"accounts": []interface{}{
					map[string]interface{}{"accountType": "06", "currentBalance": float64(42721)},
					map[string]interface{}{"accountType": "52", "currentBalance": float64(6028), "emiAmount": float64(6361)},
					map[string]interface{}{"accountType": "05", "currentBalance": float64(146864)},
				},
			},
		},
	}

	serviceMap := map[string]*models.EsaLog{
		"CIBIL": {
			ServiceId:   4,
			ServiceName: "CIBIL",
			Response:    models.ResponseDetails{StatusCode: 200, Body: cibilBody},
		},
	}

	placeholderCache := make(map[string]string)

	var outcomes []string
	for _, v := range mobileUANPreExecValidations {
		result := processor.ProcessPlaceholders(v.Expr, masterDTO, serviceMap, placeholderCache, nil)
		decision := strings.TrimSpace(strings.ToUpper(result))
		outcomes = append(outcomes, decision)
		t.Logf("Validation %d (%s): result=%q → %s", v.Index, v.Name, result, decision)
	}

	// Merge logic: first CONTINUE wins; EXIT wins over all; default EXECUTE
	final := "EXECUTE"
	for _, d := range outcomes {
		if d == "EXIT" {
			final = "EXIT"
			break
		}
		if d == "CONTINUE" && final == "EXECUTE" {
			final = "CONTINUE"
		}
	}
	t.Logf("Final decision (merge): %s", final)

	// With this data: only validation 4 (CIBIL accounts) should return CONTINUE.
	// 1: LoanCategory__c missing → not IN list → EXECUTE
	// 2: Contact.Id present → EXECUTE
	// 3: employmentType__c missing → empty not IN list → EXECUTE
	// 4: CIBIL has accountType 52 → filtered array not empty → CONTINUE
	// 5: SourcingProgram_c missing → not IN list → EXECUTE
	// 6: mobile length 10 → EXECUTE (run), else CONTINUE
	assert.Equal(t, "EXECUTE", outcomes[0], "validation 0: LoanCategory missing → EXECUTE")
	assert.Equal(t, "EXECUTE", outcomes[1], "validation 1: Contact.Id present → EXECUTE")
	assert.Equal(t, "EXECUTE", outcomes[2], "validation 2: employmentType missing → EXECUTE")
	assert.Equal(t, "CONTINUE", outcomes[3], "validation 3 (CIBIL): accountType 52 in list → CONTINUE")
	assert.Equal(t, "EXECUTE", outcomes[4], "validation 4: SourcingProgram_c missing → EXECUTE")
	assert.Equal(t, "EXECUTE", outcomes[5], "validation 5: 10-digit mobile → EXECUTE")
	assert.Equal(t, "CONTINUE", final, "merged: only validation 3 returns CONTINUE, so final = CONTINUE (skip MobileUAN)")
}

// TestPlaceholderCleanupOmitDoublePipe preserves literal || in resolved output (vendor pipe-delimited empty slots).
func TestPlaceholderCleanupOmitDoublePipe(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()
	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	serviceMap := map[string]*models.EsaLog{
		"Bureau": {
			ServiceName: "Bureau",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"VALUES": "50000|50000||50000|",
				},
			},
		},
	}
	cache := make(map[string]string)

	raw := processor.ProcessPlaceholders(`((Bureau.VALUES))`, masterDTO, serviceMap, cache, nil)
	assert.Equal(t, "50000|50000", raw, "default: text-level fallback splits on || and takes first segment; cleanup then strips remaining ||")

	opts := &sequence_service.PlaceholderCleanupOptions{
		Omit: []string{sequence_service.CleanupOmitRemoveDoublePipe},
	}
	preserved := processor.ProcessPlaceholders(`((Bureau.VALUES))`, masterDTO, serviceMap, cache, opts)
	assert.Equal(t, "50000|50000||50000|", preserved, "omit remove_double_pipe: skip text-level || fallback and || cleanup")
}

func TestCustomJoinDistinctFieldForPANs(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()
	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{}}
	cache := make(map[string]string)

	serviceMap := map[string]*models.EsaLog{
		"BureauService": {
			ServiceName: "BureauService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"multibureau_idlist__c": map[string]interface{}{
						"records": []interface{}{
							map[string]interface{}{"Id_Value__c": "BCCCP3333C", "Iden_Type__c": "PAN"},
							map[string]interface{}{"Id_Value__c": "AAAAP1111A", "Iden_Type__c": "PAN"},
							map[string]interface{}{"Id_Value__c": "CDDDP4444D", "Iden_Type__c": "PAN"},
							map[string]interface{}{"Id_Value__c": "BCCCP3333C", "Iden_Type__c": "PAN"},
							map[string]interface{}{"Id_Value__c": "BBBBP2222B", "Iden_Type__c": "PAN"},
							map[string]interface{}{"Id_Value__c": "123412341234", "Iden_Type__c": "AADHAAR"},
						},
					},
				},
			},
		},
	}

	// Delimiter is single-quoted (', ') so its trailing space is preserved. CUSTOM params are
	// TrimSpace'd (pinned by TestParseCustomLogicExpression_Characterization), so an UNQUOTED ", "
	// would collapse to ",". Quote any delimiter/param whose leading/trailing whitespace matters.
	result := processor.ProcessPlaceholders(
		`{{CUSTOM:joinDistinctField:((BureauService.multibureau_idlist__c.records)):Id_Value__c:', ':Iden_Type__c:PAN}}`,
		masterDTO,
		serviceMap,
		cache,
		nil,
	)

	assert.Equal(t, "BCCCP3333C, AAAAP1111A, CDDDP4444D, BBBBP2222B", result)
}

func TestCustomJoinDistinctFieldForPANsFromObjectSource(t *testing.T) {
	processor := sequence_service.NewExpressionProcessor()
	cache := make(map[string]string)

	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"multibureau_idlist__c": {
				"records": []interface{}{
					map[string]interface{}{"Id_Value__c": "BCCCP3333C", "Iden_Type__c": "PAN"},
					map[string]interface{}{"Id_Value__c": "AAAAP1111A", "Iden_Type__c": "PAN"},
					map[string]interface{}{"Id_Value__c": "CDDDP4444D", "Iden_Type__c": "PAN"},
					map[string]interface{}{"Id_Value__c": "BCCCP3333C", "Iden_Type__c": "PAN"},
					map[string]interface{}{"Id_Value__c": "BBBBP2222B", "Iden_Type__c": "PAN"},
					map[string]interface{}{"Id_Value__c": "999999999999", "Iden_Type__c": "AADHAAR"},
				},
			},
		},
	}

	// Single-quoted delimiter (', ') preserves its trailing space (unquoted would collapse to ",").
	result := processor.ProcessPlaceholders(
		`{{CUSTOM:joinDistinctField:{{ARRAY:<multibureau_idlist__c>}}:Id_Value__c:', ':Iden_Type__c:PAN}}`,
		masterDTO,
		map[string]*models.EsaLog{},
		cache,
		nil,
	)

	assert.Equal(t, "BCCCP3333C, AAAAP1111A, CDDDP4444D, BBBBP2222B", result)
}
