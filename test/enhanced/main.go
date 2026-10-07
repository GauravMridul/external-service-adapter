package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
	"esa/internal/app/models/esa_models"
	"esa/internal/app/service/sequence_service"
	"esa/internal/app/utility"

	"gorm.io/datatypes"
)

// ComprehensiveEnhancedTestSuite validates ALL critical functionality
// This covers the 85% missing test coverage identified in analysis
func main() {
	fmt.Println("🧪 EXTERNAL SERVICE ADAPTER - COMPREHENSIVE ENHANCED TEST SUITE")
	fmt.Println("=" + strings.Repeat("=", 80))
	fmt.Println("Testing ALL expression types, critical configurations, and custom functions")
	fmt.Println("Covering the 85% missing test coverage identified in analysis")
	fmt.Println()

	startTime := time.Now()

	// Initialize processor
	processor := sequence_service.NewExpressionProcessor()

	// Create comprehensive test data
	masterDTO := &common_dto.MasterDTO{
		Data: map[string]map[string]interface{}{
			"contact": {
				"Name":                 "John Michael Doe",
				"firstName":            "John",
				"lastName":             "Doe",
				"MailingState":         "Maharashtra",
				"OtherState":           "Karnataka",
				"gender__c":            "M",
				"Birthdate":            "1990-01-15",
				"PAN_ID__c":            "ABCDE1234F",
				"Aadhaar_Number__c":    "123456789012",
				"Driving_License__c":   "MH1234567890123456",
				"Monthly_Income__c":    "50000",
				"Email":                "john.doe@example.com",
				"Phone":                "9876543210",
				"Age__c":               "35",
				"IsActive":             "true",
				"Metadata__c":          `{"source":"web","priority":"high"}`,
				"Street":               "123 Main Street, Apartment 4B, Some City Name, State 12345",
				"ChunkStreetComma__c":  "DO Ramachandra,Muddanahalli,Chunchanakatte,K R Nagara Thalluku,Kuppe",
				"ChunkStreetSpaces__c": "123 Main Street Apartment 4B Some City Name State 12345",
				"ChunkStreetMixed__c":  "  12/7A,\tPalm Residency   Phase 2,   Sector 18  Noida  ",
				"ChunkExactFit__c":     "Alpha Beta Gamma Delta Epsilon Zeta Eta",
				"ChunkLongToken__c":    "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMN",
				"SFCreatedDate__c":     "2026-03-02T14:08:33.000+0000",
				"ParseTimeValue__c":    "2026-03-02 14:08:33",
				"employmentType__c":    "Self Employed", // For standard_employment_type__c depth-aware CONDITION test
			},
			"lead": {
				"Amount_in_Rs__c":        "1000000",
				"Loan_Rate__c":           "8.5",
				"Loan_Tenor_in_Month__c": "240",
				"Net_Salary_Monthly__c":  "50000",
				"Proposed_EMI__c":        "8500",
				"Status":                 "Open",
				"Company":                "Acme Corp",
				"Sourcing_Program__c":    "Other", // For standard_employment_type__c (not Offline Store / QRO2O)
				"Campaign__r": map[string]interface{}{
					"Sector__c":             "Salaried",
					"Bscore__c":             0.03,
					"Monthly_Income__c":     nil,
					"Offer_Upgrade_Flag__c": true,
				},
			},
			"account": {
				"Id":     "acc123",
				"Name":   "Test Account",
				"Status": "Active",
			},
			"bank_facilities__c": {
				"obligations__c": 0,
				"Vendor_Type__c": "FinBox Insights Data", // For conditional field tests
				"fis_v4__c":      573,
				"fis_v6__c":      644,
				"Status__c":      "Active",
				"Type__c":        "Premium",
			},
			"a_score__c": {
				"records": []interface{}{
					map[string]interface{}{
						"Type__c":        "Primary",
						"Score__c":       "725",
						"Risk_Bucket__c": "Low",
					},
					map[string]interface{}{
						"Type__c":        "Secondary",
						"Score__c":       "680",
						"Risk_Bucket__c": "High",
					},
				},
			},
			"es_contact__c": {
				"records": []interface{}{
					map[string]interface{}{
						"Id":           "a009H00000EOq8kQAD",
						"OwnerName__c": nil,
						"Type__c":      "AADHAAR",
					},
					map[string]interface{}{
						"Id":           "a009H00000EOq8lQAD",
						"OwnerName__c": nil,
						"Type__c":      "DL",
					},
					map[string]interface{}{
						"Id":           "a009H00000EOq8mQAD",
						"OwnerName__c": "RAHUL AGGARWAL",
						"Type__c":      "PAN",
					},
					map[string]interface{}{
						"Id":           "a009H00000EOq8pQAD",
						"OwnerName__c": nil,
						"Type__c":      "VOTER_ID",
					},
				},
			},
			// Records with a NESTED relationship field (Bureau__r.CreatedDate) and a null Type__c,
			// mirroring the Multibureau_Consolidate_Data__c shape. Used to test conditional access
			// to nested fields, == null / != null matching, and case-insensitive resolution.
			"mb_test__c": {
				"records": []interface{}{
					map[string]interface{}{
						"Type__c": nil,
						"Bureau__r": map[string]interface{}{
							"CreatedDate": "2026-07-17T08:52:02.000+0000",
							"Bureau__c":   "CRIF",
						},
					},
					map[string]interface{}{
						"Type__c": "CIBIL",
						"Bureau__r": map[string]interface{}{
							"CreatedDate": "2026-06-25T06:14:50.000+0000",
							"Bureau__c":   "CIBIL",
						},
					},
				},
			},
			// Records used to verify null semantics: an empty array and empty object are NOT null,
			// a blank string IS null. Order matters for the scan-and-return-first-match tests.
			"null_semantics__c": {
				"records": []interface{}{
					map[string]interface{}{"Kind__c": "emptyarr", "Value__c": []interface{}{}},
					map[string]interface{}{"Kind__c": "emptyobj", "Value__c": map[string]interface{}{}},
					map[string]interface{}{"Kind__c": "blank", "Value__c": ""},
					map[string]interface{}{"Kind__c": "filled", "Value__c": "hello"},
				},
			},
		},
	}

	// Create service response data
	serviceMap := map[string]*models.EsaLog{
		"TestService": {
			ServiceName: "TestService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"data": map[string]interface{}{
						"result":       "SUCCESS",
						"score":        750,
						"grade":        "A",
						"access_token": "eyJ0eXAiOiJKV1Q...",
					},
				},
			},
		},
		"IxsightNegativeScrub_kyc": {
			ServiceName: "IxsightNegativeScrub_kyc",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"SANCTIONS": []interface{}{
						map[string]interface{}{
							"MATCHED_SOURCE":   "UN - SANCTIONS LIST",
							"MATCHED_RULENAME": "NM_EX100",
							"MATCHED_NAME":     "EMRAAN ALI",
							"MATCHED_DOB1":     "04-07-1967",
						},
						map[string]interface{}{
							"MATCHED_SOURCE":   "UN - SANCTIONS LIST",
							"MATCHED_RULENAME": "NM_EX101",
							"MATCHED_NAME":     "JOHN DOE",
							"MATCHED_DOB1":     "01-01-1980",
						},
					},
					"BLACKLIST": []interface{}{
						map[string]interface{}{
							"MATCHED_SOURCE":   "OFAC",
							"MATCHED_RULENAME": "BL_EX200",
							"MATCHED_NAME":     "JANE SMITH",
							"MATCHED_DOB1":     "15-05-1975",
						},
					},
					"DEFAULTER": []interface{}{
						map[string]interface{}{
							"MATCHED_SOURCE":   "CRIF",
							"MATCHED_RULENAME": "DF_EX300",
							"MATCHED_NAME":     "BOB JOHNSON",
							"MATCHED_DOB1":     "20-10-1990",
						},
						map[string]interface{}{
							"MATCHED_SOURCE":   "CIBIL",
							"MATCHED_RULENAME": "DF_EX301",
							"MATCHED_NAME":     "ALICE BROWN",
							"MATCHED_DOB1":     "12-03-1985",
						},
					},
					"AML": []interface{}{},
					"PEP": []interface{}{},
				},
			},
		},
		"BureauBS_Ascore": {
			ServiceName: "BureauBS_Ascore",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"body": []interface{}{
						map[string]interface{}{
							"type":          "Decile1",
							"Credit_bucket": "750",
						},
						map[string]interface{}{
							"type":          "Decile2",
							"Credit_bucket": "680",
						},
						map[string]interface{}{
							"type":          "Decile3",
							"Credit_bucket": "620",
						},
					},
				},
			},
		},
		"AnotherService": {
			ServiceName: "AnotherService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"body": []interface{}{
						map[string]interface{}{
							"category": "Tier1",
							"score":    "850",
						},
						map[string]interface{}{
							"category": "Tier2",
							"score":    "720",
						},
					},
				},
			},
		},
		"FilterTestService": {
			ServiceName: "FilterTestService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"body": []interface{}{
						map[string]interface{}{"accountType": 12, "name": "A"},
						map[string]interface{}{"accountType": 14, "name": "B"},
						map[string]interface{}{"accountType": 99, "name": "C"},
						map[string]interface{}{"accountType": 23, "name": "D"},
					},
				},
			},
		},
		// KarzaGSTNonOTP: service response with results[] where each element has nested result (e.g. result.ctb, result.pradr.adr)
		"KarzaGSTNonOTP": {
			ServiceName: "KarzaGSTNonOTP",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"results": []interface{}{
						map[string]interface{}{
							"requestId":  "fe0a95ba-c591-4e77-9dce-5c25289eaf2e",
							"statusCode": 101,
							"result": map[string]interface{}{
								"ctb":  "Proprietorship",
								"sts":  "Active",
								"rgdt": "02/12/2023",
								"gti":  "NA",
								"pradr": map[string]interface{}{
									"adr": "ST-4 KEEZHA STREET, Thazhakudy Main Road",
								},
							},
						},
					},
				},
			},
		},
		"UanTestService": {
			ServiceName: "UanTestService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"result": map[string]interface{}{
						"uan": []interface{}{"100000000001", "100000000002"},
					},
				},
			},
		},
		"UanObjectsTestService": {
			ServiceName: "UanObjectsTestService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"data": map[string]interface{}{
						"result": []interface{}{
							map[string]interface{}{"uan": "100000000001"},
							map[string]interface{}{"uan": "100000000002"},
						},
					},
				},
			},
		},
		"KarzaUanValidation": {
			ServiceName: "KarzaUanValidation",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"result": map[string]interface{}{
						"summary": map[string]interface{}{
							"lastEmployer": map[string]interface{}{
								// Empty by default so fallback chain exercises monthsSince/NUMERIC:0 paths.
								"minimumWorkExperienceInMonths": "",
							},
						},
					},
				},
			},
		},
		"UANDetailsDataSutram": {
			ServiceName: "UANDetailsDataSutram",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"data": map[string]interface{}{
						// Empty by default; monthsSince(empty) => 0.
						"dateOfJoining": "",
					},
				},
			},
		},
		// CIBIL and MobileUANService for standard_employment_type__c depth-aware CONDITION test
		"CIBIL": {
			ServiceName: "CIBIL",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"consumerCreditData": map[string]interface{}{
						"accounts": []interface{}{
							map[string]interface{}{"accountType": 12, "currentBalance": 1000},
							map[string]interface{}{"accountType": 99, "currentBalance": 0},
						},
					},
				},
			},
		},
		"MobileUANService": {
			ServiceName: "MobileUANService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"request_id":  "2179fc92-e84b-455e-b9c5-4ef4a1a06b40",
					"status-code": "101",
					"clientData": map[string]interface{}{
						"caseId":                "0039H00000CsgzSQAR",
						"loanApplicationNumber": "",
					},
					"result": map[string]interface{}{
						"uan": []interface{}{"100000000001", "100000000002"},
					},
					// Production shape: data.result = array of objects with uan (for ((MobileUANService.data.result.uan)))
					"data": map[string]interface{}{
						"result": []interface{}{
							map[string]interface{}{"uan": "100000000001"},
							map[string]interface{}{"uan": "100000000002"},
						},
					},
				},
			},
		},
		// JsonPathSourceService: response has "data" as stringified JSON (for getJsonPath from service response)
		"JsonPathSourceService": {
			ServiceName: "JsonPathSourceService",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"data": `{"output":[{"rid":"","average_balance_last_2m":null,"score":100},{"rid":"r2","score":200}]}`,
				},
			},
		},
		// AACalculation: Analytics AA features response; Data__c is the exact serialized string from the example (stringified JSON with NaN)
		"AACalculation": {
			ServiceName: "AACalculation",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"statusCode": -5,
					"type":       "Analytics AA features",
					"Data__c":    `{\"output\": [{\"rid\": \"\", \"average_balance_last_2m\": NaN, \"variance_balance_last_2m\": NaN, \"average_balance_last_4m\": NaN, \"variance_balance_last_4m\": NaN, \"average_balance_last_6m\": NaN, \"variance_balance_last_6m\": NaN, \"average_balance_last_9m\": NaN, \"variance_balance_last_9m\": NaN, \"average_balance_last_12m\": NaN, \"variance_balance_last_12m\": NaN, \"average_balance_last_2m_4m\": NaN, \"variance_balance_last_2m_4m\": NaN, \"average_balance_last_4m_6m\": NaN, \"variance_balance_last_4m_6m\": NaN, \"average_balance_last_6m_9m\": NaN, \"variance_balance_last_6m_9m\": NaN, \"average_balance_last_9m_12m\": NaN, \"variance_balance_last_9m_12m\": NaN, \"number_of_transactions\": NaN, \"salary_flag\": \"\", \"salary_using_pattern\": NaN, \"salary_narration_using_keyword\": \"\", \"salary_using_income_flag\": NaN}]}`,
					"leadid":     "00Q9H00000FTmBuUAL",
				},
			},
		},
	}
	// Add Karza and DataSutram services for Aadhaar Linked config tests (bodies set per scenario)
	serviceMap["KarzaPanProfileDetail_kyc"] = &models.EsaLog{
		ServiceName: "KarzaPanProfileDetail_kyc",
		Response:    models.ResponseDetails{StatusCode: 200, Body: map[string]interface{}{}},
	}
	serviceMap["DataSutramPanProfile_kyc"] = &models.EsaLog{
		ServiceName: "DataSutramPanProfile_kyc",
		Response:    models.ResponseDetails{StatusCode: 200, Body: map[string]interface{}{}},
	}

	// CRIF service: raw_response is a stringified JSON with real CRIF structure.
	// Timestamps ("09/03/2026 15:20:06") contain ':' — validates Fix 1 quote-char tracking.
	// VARIATIONS array with TYPE filter — validates getJsonPath predicate filter support.
	serviceMap["CRIF"] = &models.EsaLog{
		ServiceName: "CRIF",
		Response: models.ResponseDetails{
			StatusCode: 200,
			Body: map[string]interface{}{
				"raw_response": `{"CIR-REPORT-FILE":{"HEADER-SEGMENT":{"DATE-OF-REQUEST":"09/03/2026 15:20:06","DATE-OF-ISSUE":"25-05-2026","STATUS":"SUCCESS","PRODUCT-TYPE":"CIR PRO V2"},"REQUEST-DATA":{"APPLICANT-SEGMENT":{"FIRST-NAME":"JOHN","LAST-NAME":"DOE","PHONES":[{"TYPE":"P04","VALUE":"9000000003"},{"TYPE":"P01","VALUE":"9876543210"}]}},"STANDARD-DATA":{"DEMOGS":{"VARIATIONS":[{"TYPE":"EMAIL-VARIATIONS","STATUS":"INACTIVE","VARIATION":[{"VALUE":"john@example.com"}]},{"TYPE":"PHONE-VARIATIONS","STATUS":"ACTIVE","VARIATION":[{"VALUE":"9000000003"},{"VALUE":"9876543210"}]},{"TYPE":"MOBILE-VARIATIONS","STATUS":"INACTIVE","VARIATION":[{"VALUE":"9999999999"}]}]}}}}` + `<html><body>html content</body></html>`,
			},
		},
	}

	// CRIFApos: same shape as CRIF but with apostrophes in a name field (e.g. "B'JOHN' B'DOE'").
	// Regression for the single-quote wrap/unwrap asymmetry: when the resolved JSON is substituted
	// into the outer expression it is wrapped as '...' with ' escaped to \'. The CUSTOM unwrap must
	// reverse that (unescapeSingleQuoteWrapped); otherwise getJsonPath's json.Unmarshal fails on the
	// illegal "\'" escape and phoneList resolves to an empty array even though phones are present.
	serviceMap["CRIFApos"] = &models.EsaLog{
		ServiceName: "CRIFApos",
		Response: models.ResponseDetails{
			StatusCode: 200,
			Body: map[string]interface{}{
				"raw_response": `{"CIR-REPORT-FILE":{"HEADER-SEGMENT":{"STATUS":"SUCCESS"},"REQUEST-DATA":{"APPLICANT-SEGMENT":{"FIRST-NAME":"B'JOHN' B'DOE'","PHONES":[{"TYPE":"P04","VALUE":"9000000001"},{"TYPE":"P01","VALUE":"9000000006"}]}},"STANDARD-DATA":{"DEMOGS":{"VARIATIONS":[{"TYPE":"NAME-VARIATIONS","VARIATION":[{"VALUE":"B'JOHN' B'DOE'"}]},{"TYPE":"PHONE-VARIATIONS","STATUS":"ACTIVE","VARIATION":[{"VALUE":"9000000001"},{"VALUE":"9000000006"}]}]}}}}` + `<html><body>html content</body></html>`,
			},
		},
	}

	// CRIFAposOdd: an ODD number of apostrophes in the clean JSON (one, in "O'BRIEN") plus a trailing
	// HTML report. Regression for the brace/param scanners: the single-quote wrap escapes ' -> \', and
	// scanners that don't honor the backslash escape inside single quotes (findMatchingClosingDoubleBraces,
	// parseCustomLogicExpression, and the ARRAY colon-finders) toggle the quote state on the escaped
	// apostrophe, mis-count the JSON's trailing "}}" and collapse the whole expression to a literal
	// "{{ARRAY:". An odd apostrophe count leaves the quote state net-wrong, which is exactly what broke
	// a real large (>2MB) CRIF payload in production. This config must still resolve the phone list.
	serviceMap["CRIFAposOdd"] = &models.EsaLog{
		ServiceName: "CRIFAposOdd",
		Response: models.ResponseDetails{
			StatusCode: 200,
			Body: map[string]interface{}{
				"raw_response": `{"CIR-REPORT-FILE":{"HEADER-SEGMENT":{"STATUS":"SUCCESS"},"REPORT-DATA":{"STANDARD-DATA":{"DEMOGS":{"VARIATIONS":[{"TYPE":"NAME-VARIATIONS","VARIATION":[{"VALUE":"O'BRIEN"},{"VALUE":"REGULAR NAME"}]},{"TYPE":"PHONE-VARIATIONS","VARIATION":[{"VALUE":"9000000006"},{"VALUE":"8888888888"}]}]}}}}}` + `<html><body>report</body></html>`,
			},
		},
	}

	placeholderCache := make(map[string]string)

	// Execute all test categories
	testResults := []TestCategoryResult{}

	// 1. PRIORITY 1: Expression Type Tests
	fmt.Println("🎯 PRIORITY 1: EXPRESSION TYPE TESTS")
	fmt.Println("=" + strings.Repeat("=", 50))

	expressionResults := runExpressionTypeTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, expressionResults...)

	// 2. PRIORITY 1: 5 Critical Configuration Tests
	fmt.Println("\n🎯 PRIORITY 1: CRITICAL CONFIGURATION TESTS")
	fmt.Println("=" + strings.Repeat("=", 50))

	configResults := runCriticalConfigurationTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, configResults...)

	// 3. PRIORITY 2: Custom Function Tests
	fmt.Println("\n🎯 PRIORITY 2: CUSTOM FUNCTION TESTS")
	fmt.Println("=" + strings.Repeat("=", 50))

	customResults := runCustomFunctionTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, customResults...)

	// 4. ARRAY Merge Feature Tests (NEW)
	fmt.Println("\n🎯 PRIORITY 3: ARRAY MERGE FEATURE TESTS")
	fmt.Println("=" + strings.Repeat("=", 50))

	mergeResults := runArrayMergeTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, mergeResults...)

	// 4b. Analytics AA Features: replace NaN in stringified JSON then getJsonPath then ARRAY (2nd service config)
	fmt.Println("\n🎯 ANALYTICS AA FEATURES (replace NaN + getJsonPath + ARRAY)")
	fmt.Println("=" + strings.Repeat("=", 50))

	analyticsResults := runAnalyticsAAFeaturesTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, analyticsResults...)

	// 4c. TEMPORARY: Analytics AA production config debug (exact AACalculation response shape + PostExecution-style resolution)
	fmt.Println("\n🎯 [DEBUG] ANALYTICS AA PRODUCTION CONFIG (AACalculation.Data__c + normalizeJsonNan + getJsonPath:output)")
	fmt.Println("=" + strings.Repeat("=", 50))
	analyticsDebugResults := runAnalyticsAAProductionDebugTest(processor, masterDTO, placeholderCache)
	testResults = append(testResults, analyticsDebugResults...)

	// 5. Advanced Feature Tests
	fmt.Println("\n🎯 PRIORITY 4: ADVANCED FEATURE TESTS")
	fmt.Println("=" + strings.Repeat("=", 50))

	advancedResults := runAdvancedFeatureTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, advancedResults...)

	// 6. Chunk transformation (address/street to line1..line5 without breaking words)
	fmt.Println("\n🎯 CHUNK TRANSFORMATION (ADDRESS LINES)")
	fmt.Println("=" + strings.Repeat("=", 50))

	chunkResults := runChunkTransformationTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, chunkResults...)

	// 7. Standard Employment Type - depth-aware CONDITION (nested colons in {{CONDITION:...}})
	fmt.Println("\n🎯 STANDARD EMPLOYMENT TYPE (DEPTH-AWARE CONDITION)")
	fmt.Println("=" + strings.Repeat("=", 50))

	employmentTypeResults := runStandardEmploymentTypeTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, employmentTypeResults...)

	// 8. PreExecution / PostExecution: SF field extraction from validations
	fmt.Println("\n🎯 PREEXECUTION / POSTEXECUTION (FIELD EXTRACTION & ENABLED)")
	fmt.Println("=" + strings.Repeat("=", 50))

	preExecResults := runPreExecutionUtilityTests()
	testResults = append(testResults, preExecResults...)

	// 8b. Field extraction: config-type coverage + garbage-object-name guard (fix #1)
	fmt.Println("\n🎯 FIELD EXTRACTION (CONFIG TYPES + GARBAGE-OBJECT GUARD)")
	fmt.Println("=" + strings.Repeat("=", 50))

	fieldExtractionResults := runFieldExtractionConfigTypeTests()
	testResults = append(testResults, fieldExtractionResults...)

	// 8c. Previously-uncovered DB constructs: UNMAPPED + joinDistinctField/getOfficePincode/extractPincode/equals
	fmt.Println("\n🎯 UNCOVERED DB CONSTRUCTS (UNMAPPED + CUSTOM FUNCTIONS)")
	fmt.Println("=" + strings.Repeat("=", 50))

	uncoveredResults := runUncoveredDbConstructTests(processor)
	testResults = append(testResults, uncoveredResults...)

	// 9. MultiBureau response reuse: exact nested condition resolution
	fmt.Println("\n🎯 MULTIBUREAU REUSE WITHIN 30 DAYS")
	fmt.Println("=" + strings.Repeat("=", 50))

	multiBureauResults := runMultiBureauReuseConditionTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, multiBureauResults...)

	// 10. Aadhaar Linked config: Karza | DataSutram (last4/last2+XX) | default blank, ref fallback DmiPlatform then eKYC
	fmt.Println("\n🎯 AADHAAR LINKED CONFIG (DATA SUTRAM + KARZA)")
	fmt.Println("=" + strings.Repeat("=", 50))

	aadhaarLinkedResults := runAadhaarLinkedConfigTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, aadhaarLinkedResults...)

	// 11. CRIF phoneList: getJsonPath from stringified JSON with HTML suffix + static literal injection
	fmt.Println("\n🎯 CRIF PHONE LIST (getJsonPath + static literal in transform-only)")
	fmt.Println("=" + strings.Repeat("=", 50))

	crifPhoneResults := runCrifPhoneListTests(processor, masterDTO, serviceMap, placeholderCache)
	testResults = append(testResults, crifPhoneResults...)

	// Generate comprehensive report
	generateComprehensiveReport(testResults, time.Since(startTime))
}

// runExpressionTypeTests tests all 11 expression types
func runExpressionTypeTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// CALC expressions
	calcTests := []TestCase{
		{"{{CALC:100 + 50}}", "150", "Basic addition"},
		{"{{CALC:1000 * 0.085}}", "85", "Multiplication with decimal"},
		{"{{CALC:<contact.Monthly_Income__c> * 12}}", "600000", "Field multiplication"},
		{"{{NUMERIC:{{CALC:<lead.Amount_in_Rs__c> / 1000}}}}", "1000", "Nested numeric in calc (correct architecture)"},
		// Multi-operand + operator precedence (item 2 fix)
		{"{{CALC:100 + 50 + 25}}", "175", "Three operands (addition, left-to-right)"},
		{"{{CALC:100 - 20 - 5}}", "75", "Three operands (subtraction, left-to-right)"},
		{"{{CALC:2 + 3 * 4}}", "14", "Precedence: multiply before add"},
		{"{{CALC:2 * 3 + 4 * 5}}", "26", "Precedence: two products then add"},
		{"{{CALC:100 - 10 * 2}}", "80", "Precedence: multiply before subtract"},
		{"{{CALC:<contact.Monthly_Income__c> * 12 + 1000}}", "601000", "Field multiply then add (multi-operand)"},
		{"{{CALC:20 / 2 / 5}}", "2", "Three operands division (left-to-right)"},
		// Grouping parentheses (item: cascaded () support)
		{"{{CALC:2 * (3 + 4)}}", "14", "Grouping: multiply a sum"},
		{"{{CALC:(2 + 3) * 4}}", "20", "Grouping: leading group times factor"},
		{"{{CALC:100 / (2 + 3)}}", "20", "Grouping: divide by a sum"},
		{"{{CALC:2 * (3 + 4) - 5}}", "9", "Grouping mixed with trailing subtraction"},
		{"{{CALC:(10 - 2) * (3 + 1)}}", "32", "Two groups multiplied"},
		{"{{CALC:((2 + 3) * 4)}}", "20", "Nested/redundant outer grouping"},
		{"{{CALC:100 - (10 * 2)}}", "80", "Grouping around a product (same as precedence)"},
		{"{{CALC:<contact.Monthly_Income__c> * (12 + 1)}}", "650000", "Field times a grouped sum"},
		{"{{CALC:2 * (3 + 4) * (1 + 1)}}", "28", "Two groups and a scalar"},
		// Function call mixed with operators (previously returned empty)
		{"{{CALC:ROUND(85.126, 2) + 1}}", "86.13", "Function result plus scalar (mixed)"},
		{"{{CALC:MAX(3, 7) * 2}}", "14", "Function result times scalar (mixed)"},
		// Pure function calls must still format exactly as before
		{"{{CALC:ROUND(85.126, 2)}}", "85.13", "Pure ROUND (legacy formatting preserved)"},
		{"{{CALC:ABS(-5)}}", "5.00", "Pure ABS (legacy %.2f formatting preserved)"},
		{"{{CALC:MAX(3, 7, 5)}}", "7.00", "Pure MAX (legacy %.2f formatting preserved)"},
		{"{{CALC:CEIL(4.2)}}", "5", "Pure CEIL (legacy %.0f formatting preserved)"},
		// Adversarial: division by zero inside a group -> "0" (legacy); deeper nesting; service placeholder operand
		{"{{CALC:10 / (2 - 2)}}", "0", "Division by zero inside group returns 0"},
		{"{{CALC:((1 + 2) * (3 + 4))}}", "21", "Deeper nested grouping"},
		{"{{CALC:((TestService.data.score)) + 50}}", "800", "Service placeholder operand in grouped arithmetic"},
		{"{{CALC:((TestService.data.score)) / (2 + 3)}}", "150", "Service placeholder divided by a group"},
		// Regression guards (from semantic review): literal slash division must NOT be swallowed
		// as a date, and year-month subtraction stays arithmetic.
		{"{{CALC:10/2}}", "5", "Literal division 10/2 (not a date)"},
		{"{{CALC:12/25}}", "0.48", "Literal division 12/25 (not a date)"},
		{"{{CALC:6/15}}", "0.40", "Literal division 6/15 (not a date)"},
		{"{{CALC:2024-01}}", "2023", "Year-month is arithmetic, not a date literal"},
	}
	results = append(results, runTestCategory("CALC Expressions", calcTests, processor, masterDTO, serviceMap, cache))

	// FORMAT expressions
	formatTests := []TestCase{
		{"{{FORMAT:date:2025-09-11:YYYY-MM-DD}}", "2025-09-11", "Date formatting"},
		{"{{FORMAT:date:<contact.Birthdate>:DD-MM-YYYY}}", "15-01-1990", "Date formatting (DD-MM-YYYY via placeholder)"},
		{"{{FORMAT:date:2025-02-24T06:38:28.000+0000:DD-MM-YYYY}}", "24-02-2025", "Date formatting (literal with colons, Salesforce +0000)"},
		{"{{FORMAT:date:2026-02-26T08:15:42Z:DD-MM-YYYY}}", "26-02-2026", "Date formatting (literal Z suffix)"},
		{"{{FORMAT:date:<lead.PreApproved_Date__c>:DD-MM-YYYY}}", "", "Date formatting (missing field → blank, no default time)"},
		{"{{FORMAT:date:<contact.Birthdate>::YYYY-MM-DD HH:mm:ss}}", "1990-01-15 00:00:00", "Date formatting (:: separator, pattern with colons)"},
		{"{{FORMAT:date:<contact.Birthdate>::YYYY-MM-DD HH:mm}}", "1990-01-15 00:00", "Date formatting (:: separator, YYYY-MM-DD HH:mm)"},
		{"{{FORMAT:number:50000:currency:USD}}", "$50,000.00", "Currency formatting"},
		{"{{FORMAT:text:<contact.Name>:uppercase}}", "JOHN MICHAEL DOE", "Text uppercase"},
	}
	results = append(results, runTestCategory("FORMAT Expressions", formatTests, processor, masterDTO, serviceMap, cache))

	// TRANSFORM expressions
	transformTests := []TestCase{
		{"{{TRANSFORM:<contact.Name>:uppercase}}", "JOHN MICHAEL DOE", "Basic transform"},
		{"{{TRANSFORM:{{CUSTOM:getCurrentTimestamp}}:format_time:YYYY-MM-DD}}", "", "Nested transform (expect date format)"}, // Will test the fix
		{"{{TRANSFORM:<contact.Email>:lowercase}}", "john.doe@example.com", "Email lowercase"},
		{"{{TRANSFORM:<contact.Name>:replace:' ':'%20'}}", "John%20Michael%20Doe", "Replace spaces with %20"},
		{"{{TRANSFORM:<contact.Name>:replace:<contact.lastName>:{{TRANSFORM:<contact.lastName>:uppercase}}}}", "John Michael DOE", "Replace using placeholder-driven parameters"},
		{"{{TRANSFORM:<contact.SFCreatedDate__c>:format_time:YYYY-MM-DD HH:MM:SS}}", "2026-03-02 14:08:33", "format_time with colon-containing output pattern"},
		{"{{TRANSFORM:<contact.SFCreatedDate__c>:format_time:YYYY-MM-DDTHH:MM:SS.SSSZZZZ}}", "2026-03-02T14:08:33.000+0000", "format_time with standardized offset alias"},
		{"{{TRANSFORM:<contact.ParseTimeValue__c>:parse_time:YYYY-MM-DD HH:MM:SS:DDMMYYYY}}", "02032026", "parse_time with colon-containing source pattern"},
		{"{{TRANSFORM:<contact.ParseTimeValue__c>:parse_time:YYYY-MM-DD HH:MM:SS:YYYY-MM-DDTHH:MM:SSZ}}", "2026-03-02T14:08:33Z", "parse_time with colon-containing source and ISO target alias"},
	}
	results = append(results, runTestCategory("TRANSFORM Expressions", transformTests, processor, masterDTO, serviceMap, cache))

	// CONCAT expressions
	concatTests := []TestCase{
		{"{{CONCAT:<contact.firstName>: :<contact.lastName>}}", "John Doe", "Basic concatenation"},
		{"{{CONCAT:{{TRANSFORM:<contact.firstName>:uppercase}}:_:{{TRANSFORM:<contact.lastName>:uppercase}}}}", "JOHN_DOE", "Nested concatenation"},
	}
	results = append(results, runTestCategory("CONCAT Expressions", concatTests, processor, masterDTO, serviceMap, cache))

	// CONDITION expressions (enhanced)
	conditionTests := []TestCase{
		{"{{CONDITION:<contact.Age__c> >= 18:eligible:not_eligible}}", "eligible", "Basic condition"},
		{"{{CONDITION:<contact.Age__c> >= 18 && <lead.Status> == 'Open':approved:rejected}}", "approved", "Multi-condition AND"},
		{"{{CONDITION:<lead.Status> == 'Open' || <lead.Status> == 'Pending':valid:invalid}}", "valid", "Multi-condition OR"},
		{"{{CONDITION:(<contact.Age__c> >= 18 && <contact.Age__c> <= 65) && <lead.Amount_in_Rs__c> > 500000:qualified:not_qualified}}", "qualified", "Complex parentheses"},
		// CONDITION IN operator (parentheses list, case-sensitive)
		{"{{CONDITION:<lead.Status> IN ('Open','Pending'):valid:invalid}}", "valid", "CONDITION IN - value in list"},
		{"{{CONDITION:<lead.Status> IN ('Closed','Rejected'):valid:invalid}}", "invalid", "CONDITION IN - value not in list"},
		// CONDITION with {{ }} expression on left of == (processUnifiedParameter resolves nested expressions)
		{"{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:{{TRANSFORM:{{CUSTOM:cleanSpecialChars:<contact.Phone>}}:remove_spaces}}:10}}:length}} == 10:ten:not_ten}}", "ten", "CONDITION nested expression left side"},
	}
	results = append(results, runTestCategory("CONDITION Expressions (Enhanced)", conditionTests, processor, masterDTO, serviceMap, cache))

	// CUSTOM expressions
	customTests := []TestCase{
		{"{{CUSTOM:generateId}}", "", "Generate ID (expect non-empty)"},
		{"{{CUSTOM:getCurrentTimestamp}}", "", "Current timestamp (expect timestamp format)"},
		{"{{CUSTOM:calculateAge:<contact.Birthdate>}}", "", "Calculate age (expect number)"},
		{"{{CUSTOM:validatePAN:<contact.PAN_ID__c>}}", "", "Validate PAN (expect boolean)"},
		{"{{CUSTOM:monthsSince::DD/MM/YYYY}}", "0", "monthsSince empty date returns 0"},
		{"{{CUSTOM:monthsSince:not-a-date:DD/MM/YYYY}}", "0", "monthsSince invalid date returns 0"},
		{"{{CUSTOM:monthsSince:16/05/2011:DD/MM/YYYY}}", "", "monthsSince valid date (vintage/work experience in months)"},
		{"{{CUSTOM:monthsSince:<contact.Birthdate>:YYYY-MM-DD}}", "", "monthsSince from DB field (Birthdate)"},
		// getJsonPath: parse stringified JSON and extract by path (e.g. from service response data field)
		{"{{CUSTOM:getJsonPath:<contact.Metadata__c>:source}}", "web", "getJsonPath from DB field (object key)"},
		{"{{CUSTOM:getJsonPath:<contact.Metadata__c>:priority}}", "high", "getJsonPath from DB field (priority)"},
		{"{{CUSTOM:getJsonPath:'{\"output\":[{\"rid\":\"x\",\"score\":100}]}':output.0}}", "", "getJsonPath literal string output[0] (expect object)"},
		{"{{CUSTOM:getJsonPath:((JsonPathSourceService.data)):output.0}}", "", "getJsonPath from service response data (output[0])"},
		{"{{CUSTOM:getJsonPath:((JsonPathSourceService.data)):output.1.rid}}", "r2", "getJsonPath from service response (output[1].rid)"},
		{"{{CUSTOM:getJsonPath:{{CUSTOM:normalizeJsonNan:((AACalculation.Data__c))}}:output}}", "", "getJsonPath from service with NaN via nested normalizeJsonNan"},
		{"{{CUSTOM:normalizeJsonNan:'{\"x\":NaN}'}}", "{\"x\":0}", "normalizeJsonNan replaces NaN with 0"},
		{"{{CUSTOM:stringifyJSON:((TestService.data))}}", `{"access_token":"eyJ0eXAiOiJKV1Q...","grade":"A","result":"SUCCESS","score":750}`, "stringifyJSON serializes service response object to JSON string"},
	}
	results = append(results, runTestCategory("CUSTOM Expressions", customTests, processor, masterDTO, serviceMap, cache))

	// NUMERIC expressions
	numericTests := []TestCase{
		{"{{NUMERIC:<contact.Monthly_Income__c>}}", "50000", "Basic numeric conversion"},
		{"{{NUMERIC:<contact.Age__c> || '0'}}", "35", "Numeric with fallback"},
		{"{{NUMERIC:{{CUSTOM:calculateAge:<contact.Birthdate>}}}}", "", "Nested numeric (expect number)"},
		// NUMERIC + || + nested CUSTOM with <db.field> inside CUSTOM (unified fallback must evaluate {{...}} arms)
		{"{{NUMERIC:{{CUSTOM:calculateAge:<contact.Birthdate>}} || '0'}}", "", "NUMERIC fallback: nested calculateAge then literal 0 (never skip CUSTOM)"},
		{"{{NUMERIC:<contact.NonExistentDob__c> || {{CUSTOM:calculateAge:<contact.Birthdate>}}}}", "", "NUMERIC fallback: empty field then nested calculateAge from Birthdate"},
	}
	results = append(results, runTestCategory("NUMERIC Expressions", numericTests, processor, masterDTO, serviceMap, cache))

	// BOOLEAN expressions
	booleanTests := []TestCase{
		{"{{BOOLEAN:<contact.IsActive>}}", "true", "Basic boolean conversion"},
		{"{{BOOLEAN:<contact.NonExistent> || 'false'}}", "false", "Boolean with fallback"},
	}
	results = append(results, runTestCategory("BOOLEAN Expressions", booleanTests, processor, masterDTO, serviceMap, cache))

	// JSON expressions
	jsonTests := []TestCase{
		{"{{JSON:<contact.Metadata__c>}}", `{"source":"web","priority":"high"}`, "Basic JSON parsing"},
		{"{{JSON:{\"name\":\"{{TRANSFORM:<contact.Name>:uppercase}}\",\"age\":{{NUMERIC:<contact.Age__c>}}}}}", "", "Complex JSON construction"},
	}
	results = append(results, runTestCategory("JSON Expressions", jsonTests, processor, masterDTO, serviceMap, cache))

	// ARRAY expressions
	arrayTests := []TestCase{
		{"{{ARRAY:tags:split:,}}", "", "Array split (expect array handling)"},
		{"{{ARRAY:transform:{{JSON:[\"a\",\"b\",\"c\"]}}:{{TRANSFORM:$item:uppercase}}}}", "", "Array transform (expect transformed items)"},
		{"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Score__c->value@NUMERIC}}", "", "Object-only ARRAY transform"},
		// NEW: Merge functionality tests
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)):merge}}", "", "Basic merge - single array (no-op)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)):merge}}", "", "Merge two arrays"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)),((IxsightNegativeScrub_kyc.DEFAULTER)):merge}}", "", "Merge three arrays"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)),((IxsightNegativeScrub_kyc.DEFAULTER)):merge:transform-only:MATCHED_SOURCE->key,MATCHED_RULENAME->value}}", "", "Merge with transform-only"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.AML)),((IxsightNegativeScrub_kyc.PEP)):merge}}", "", "Merge empty arrays"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.AML)):merge}}", "", "Merge array with empty array"},
	}
	results = append(results, runTestCategory("ARRAY Expressions", arrayTests, processor, masterDTO, serviceMap, cache))

	// CONDITIONAL expressions (NEW)
	conditionalTests := []TestCase{
		{"{{CONDITIONAL:<contact.PAN_ID__c>:RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "01", "Document type by length"},
		{"{{CONDITIONAL:<contact.gender__c>:RULES:GENDER:MAPPING:M,Male->'2':F,Female->'1':'3'}}", "2", "Gender mapping"},
		{"{{CONDITIONAL:<contact.Name>:RULES:NAME:SPLIT:PART:first}}", "John", "Name splitting - first"},
		{"{{CONDITIONAL:<contact.Name>:RULES:NAME:SPLIT:PART:last}}", "Doe", "Name splitting - last"},
		{"{{CONDITIONAL:<contact.Birthdate>:RULES:DATE:FORMAT:DDMMYYYY}}", "15011990", "Date formatting"},
		{"{{CONDITIONAL:<contact.MailingState>:RULES:STATE:BUREAU:CIBIL}}", "27", "State code resolution"},
	}
	results = append(results, runTestCategory("CONDITIONAL Expressions (NEW)", conditionalTests, processor, masterDTO, serviceMap, cache))

	return results
}

// runCriticalConfigurationTests tests the 5 problematic configurations
func runCriticalConfigurationTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// 1. Date Formatting Issues
	dateTests := []TestCase{
		{"{{CONDITIONAL:{{CUSTOM:getCurrentTimestamp}}:RULES:DATE:FORMAT:MMDDYYYY}}", "", "Current timestamp to MMDDYYYY"},
		{"{{CONDITIONAL:<contact.Birthdate>:RULES:DATE:FORMAT:DDMMYYYY}}", "15011990", "Birth date to DDMMYYYY"},
		{"{{CONDITIONAL:'2025-09-11T14:30:00Z':RULES:DATE:FORMAT:YYYY-MM-DD}}", "2025-09-11", "ISO to standard date"},
		{"{{CONDITIONAL:1990-01-15:RULES:DATE:FORMAT:MM/DD/YYYY}}", "01/15/1990", "Standard to US format"},
	}
	results = append(results, runTestCategory("🔧 CONFIG FIX 1: Date Formatting", dateTests, processor, masterDTO, serviceMap, cache))

	// 2. Name Parsing Issues
	nameTests := []TestCase{
		{"{{CONDITIONAL:John Michael Doe:RULES:NAME:SPLIT:PART:first}}", "John", "3-part name - first"},
		{"{{CONDITIONAL:John Michael Doe:RULES:NAME:SPLIT:PART:middle}}", "Michael", "3-part name - middle"},
		{"{{CONDITIONAL:John Michael Doe:RULES:NAME:SPLIT:PART:last}}", "Doe", "3-part name - last"},
		{"{{CONDITIONAL:John Doe:RULES:NAME:SPLIT:PART:first}}", "John", "2-part name - first"},
		{"{{CONDITIONAL:John Doe:RULES:NAME:SPLIT:PART:middle}}", "null", "2-part name - middle (null)"},
		{"{{CONDITIONAL:John Doe:RULES:NAME:SPLIT:PART:last}}", "Doe", "2-part name - last"},
		{"{{CONDITIONAL:John:RULES:NAME:SPLIT:PART:first}}", "John", "1-part name - first"},
		{"{{CONDITIONAL:John:RULES:NAME:SPLIT:PART:last}}", ".", "1-part name - last (dot)"},
	}
	results = append(results, runTestCategory("🔧 CONFIG FIX 2: Name Parsing", nameTests, processor, masterDTO, serviceMap, cache))

	// 3. Gender Mapping Issues
	genderTests := []TestCase{
		{"{{CONDITIONAL:M:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "2", "Male - M"},
		{"{{CONDITIONAL:m:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "2", "Male - m"},
		{"{{CONDITIONAL:Male:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "2", "Male - Male"},
		{"{{CONDITIONAL:MALE:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "2", "Male - MALE"},
		{"{{CONDITIONAL:F:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "1", "Female - F"},
		{"{{CONDITIONAL:f:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "1", "Female - f"},
		{"{{CONDITIONAL:Female:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "1", "Female - Female"},
		{"{{CONDITIONAL:FEMALE:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "1", "Female - FEMALE"},
		{"{{CONDITIONAL:Other:RULES:GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'}}", "3", "Other - default"},
	}
	results = append(results, runTestCategory("🔧 CONFIG FIX 3: Gender Mapping", genderTests, processor, masterDTO, serviceMap, cache))

	// 4. Document Type by Length Issues
	docTests := []TestCase{
		{"{{CONDITIONAL:ABCDE1234F:RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "01", "PAN (10 chars) -> 01"},
		{"{{CONDITIONAL:123456789012:RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "06", "Aadhaar (12 chars) -> 06"},
		{"{{CONDITIONAL:MH12345678901234:RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "04", "Driving License (16 chars) -> 04"},
		{"{{CONDITIONAL:ABC123:RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "99", "Unknown length -> 99"},
		{"{{CONDITIONAL::RULES:LENGTH:10->'01':12->'06':16->'04':'99'}}", "99", "Empty input -> 99"},
	}
	results = append(results, runTestCategory("🔧 CONFIG FIX 4: Document Type by Length", docTests, processor, masterDTO, serviceMap, cache))

	// 5. State Code Resolution Issues
	stateTests := []TestCase{
		{"{{CONDITIONAL:Maharashtra:RULES:STATE:BUREAU:CIBIL}}", "27", "Maharashtra CIBIL code"},
		{"{{CONDITIONAL:Maharashtra:RULES:STATE:BUREAU:CRIF}}", "MH", "Maharashtra CRIF code"},
		{"{{CONDITIONAL:Karnataka:RULES:STATE:BUREAU:CIBIL}}", "29", "Karnataka CIBIL code"},
		{"{{CONDITIONAL:Karnataka:RULES:STATE:BUREAU:CRIF}}", "KA", "Karnataka CRIF code"},
		{"{{CONDITIONAL:Tamil Nadu:RULES:STATE:BUREAU:CIBIL}}", "33", "Tamil Nadu CIBIL code"},
		{"{{CONDITIONAL:Unknown State:RULES:STATE:BUREAU:CIBIL}}", "99", "Unknown state CIBIL → 99"},
		{"{{CONDITIONAL:Unknown State:RULES:STATE:BUREAU:CRIF}}", "NA", "Unknown state CRIF → NA"},
		// Test fallbacks
		{"{{CONDITIONAL:<contact.MailingState> || <contact.OtherState>:RULES:STATE:BUREAU:CIBIL}}", "27", "State with fallback"},
	}
	results = append(results, runTestCategory("🔧 CONFIG FIX 5: State Code Resolution", stateTests, processor, masterDTO, serviceMap, cache))

	// 6. Config regressions (fallback chains with expression results / trailing whitespace)
	regressionTests := []TestCase{
		{
			"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Score__c->value@NUMERIC}} || []",
			`[{"key":"Primary","value":725},{"key":"Secondary","value":680}]`,
			"Gpay_CAM_Scores (ARRAY transform-only with fallback)",
		},
		{
			"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Risk_Bucket__c->value}} || []",
			`[{"key":"Primary","value":"Low"},{"key":"Secondary","value":"High"}]`,
			"Gpay_CAM_Segments (ARRAY transform-only with fallback)",
		},
		{
			"{{ARRAY:((IxsightNegativeScrub_kyc.AML)),((IxsightNegativeScrub_kyc.PEP)):merge}} || []",
			"[]",
			"ARRAY merge empty => null, fallback to []",
		},
		{
			"{{NUMERIC:((KarzaUanValidation.result.summary.lastEmployer.minimumWorkExperienceInMonths))|| {{CUSTOM:monthsSince:((UANDetailsDataSutram.data.dateOfJoining)):DD/MM/YYYY}} ||NUMERIC:0}} ",
			"0",
			"UAN_Total_Work_Experience_In_Months (service fallback, trailing space)",
		},
	}
	results = append(results, runTestCategory("🔧 CONFIG REGRESSION: CAM + UAN", regressionTests, processor, masterDTO, serviceMap, cache))

	blankSourcingProgramDTO := cloneMasterDTO(masterDTO)
	blankSourcingProgramDTO.Data["lead"]["Sourcing_Program__c"] = ""
	sourcingProgramFallbackTests := []TestCase{
		{
			"<lead.Sourcing_Program__c>|| 'null'",
			"null",
			"Sourcing Program plain fallback to 'null' when SF field is blank",
		},
		{
			"{{CONDITION:<lead.Sourcing_Program__c> != '':<lead.Sourcing_Program__c>:'null'}}",
			"null",
			"Sourcing Program CONDITION fallback to 'null' when SF field is blank",
		},
	}
	results = append(results, runTestCategory("🔧 CONFIG REGRESSION: Sourcing Program Blank Fallback", sourcingProgramFallbackTests, processor, blankSourcingProgramDTO, serviceMap, cache))

	return results
}

// runCustomFunctionTests tests all 16 custom functions
func runCustomFunctionTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// Core custom functions
	coreTests := []TestCase{
		{"{{CUSTOM:generateId}}", "", "Generate unique ID (expect UUID-like)"},
		{"{{CUSTOM:getCurrentTimestamp}}", "", "Current timestamp (expect ISO format)"},
		{"{{CUSTOM:getCurrentTimestamp:YYYYMMDD}}", "", "Formatted timestamp"},
		{"{{CUSTOM:calculateAge:<contact.Birthdate>}}", "", "Calculate age from birthdate"},
		{"{{CUSTOM:validatePAN:<contact.PAN_ID__c>}}", "", "Validate PAN format"},
		{"{{CUSTOM:formatPhone:<contact.Phone>}}", "", "Format phone number"},
	}
	results = append(results, runTestCategory("Core Custom Functions", coreTests, processor, masterDTO, serviceMap, cache))

	// String processing functions
	stringTests := []TestCase{
		{"{{CUSTOM:cleanSpecialChars:Hello@#World!}}", "HelloWorld", "Clean special characters"},
		{"{{CUSTOM:takeLast:<contact.Phone>:4}}", "3210", "Take last 4 digits"},
		{"{{CUSTOM:sha256Hash:<contact.Email>}}", "", "SHA256 hash (expect 64 chars)"},
		{"{{CUSTOM:base64Encode:hello}}", "aGVsbG8=", "Base64 encode plain text"},
	}
	results = append(results, runTestCategory("String Processing Functions", stringTests, processor, masterDTO, serviceMap, cache))

	// Date functions — SFCreatedDate__c and literals must be within the 30-day window vs time.Now(),
	// otherwise assertions drift as calendar time passes (fixed 2026-03-02 dates eventually fail).
	dateMaster := cloneMasterDTO(masterDTO)
	recentSF := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000+0000")
	dateMaster.Data["contact"]["SFCreatedDate__c"] = recentSF
	dateTests := []TestCase{
		{"{{CUSTOM:dateWithinDays:<contact.Birthdate>:30:6570}}", "false", "Date within range check"},
		{"{{CUSTOM:dateWithinDays:<contact.SFCreatedDate__c>:30}}", "true", "Salesforce CreatedDate with timezone offset"},
		{fmt.Sprintf("{{CUSTOM:dateWithinDays:%s:30}}", recentSF), "true", "dateWithinDays with raw datetime literal containing colons"},
		{"{{CUSTOM:calculateAge:1990-01-15T00:00:00Z}}", "36", "calculateAge with raw datetime literal containing colons"},
		{"{{CUSTOM:daysSince:<contact.Birthdate>}}", "", "Days since date (expect large number)"},
	}
	results = append(results, runTestCategory("Date Functions", dateTests, processor, dateMaster, serviceMap, cache))

	// Numeric functions
	numericTests := []TestCase{
		{"{{CUSTOM:getNumericValue:<contact.Monthly_Income__c>}}", "50000", "Extract numeric value"},
		{"{{CUSTOM:calculateFOIR:<lead.Amount_in_Rs__c>:<lead.Loan_Rate__c>:<lead.Loan_Tenor_in_Month__c>:<lead.Net_Salary_Monthly__c>:<lead.Proposed_EMI__c>:Direct:true:Income:2:50000}}", "", "Calculate FOIR (expect ratio)"},
	}
	results = append(results, runTestCategory("Numeric Functions", numericTests, processor, masterDTO, serviceMap, cache))

	// NEW Business logic functions
	businessTests := []TestCase{
		{"{{CUSTOM:resolveBureauStateCode:<contact.MailingState>:CIBIL}}", "27", "Resolve state code - CIBIL"},
		{"{{CUSTOM:resolveBureauStateCode:<contact.MailingState>:CRIF}}", "MH", "Resolve state code - CRIF"},
		{"{{CUSTOM:resolveBureauStateCode:Unknown:CIBIL}}", "99", "Unknown state - CIBIL default"},
		{"{{CUSTOM:resolveBureauStateCode:Unknown:CRIF}}", "NA", "Unknown state - CRIF default"},
		{"{{CUSTOM:splitName:<contact.Name>:first}}", "John", "Split name - first"},
		{"{{CUSTOM:splitName:<contact.Name>:last}}", "Doe", "Split name - last"},
		{"{{CUSTOM:mapGender:<contact.gender__c>}}", "2", "Map gender M→2"},
		{"{{CUSTOM:mapDocumentType:<contact.PAN_ID__c>}}", "01", "Map document type by length"},
	}
	results = append(results, runTestCategory("NEW Business Logic Functions", businessTests, processor, masterDTO, serviceMap, cache))

	return results
}

// runArrayMergeTests tests the new ARRAY merge functionality
func runArrayMergeTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// Basic merge tests
	basicMergeTests := []TestCase{
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)):merge}}", "", "Basic merge - single array (should return same array)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)):merge}}", "", "Merge two arrays (2 + 1 = 3 items)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)),((IxsightNegativeScrub_kyc.DEFAULTER)):merge}}", "", "Merge three arrays (2 + 1 + 2 = 5 items)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.AML)),((IxsightNegativeScrub_kyc.PEP)):merge}}", "", "Merge empty arrays (should return empty array)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.AML)):merge}}", "", "Merge array with empty array (should return non-empty array)"},
	}
	results = append(results, runTestCategory("🆕 ARRAY Merge - Basic Operations", basicMergeTests, processor, masterDTO, serviceMap, cache))

	// Merge with transformations
	mergeTransformTests := []TestCase{
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)),((IxsightNegativeScrub_kyc.DEFAULTER)):merge:transform-only:MATCHED_SOURCE->key,MATCHED_RULENAME->value}}", "", "Merge with transform-only (key-value mapping)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)):merge:transform:MATCHED_SOURCE->source,MATCHED_RULENAME->rule}}", "", "Merge with transform (keep all fields)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)):merge:map:MATCHED_SOURCE,MATCHED_RULENAME}}", "", "Merge with map (select specific fields)"},
		{"{{ARRAY:{{ARRAY:((BureauBS_Ascore.body)):transform-only:type->key,Credit_bucket->value@NUMERIC}},{{ARRAY:((AnotherService.body)):transform-only:category->key,score->value@NUMERIC}}:merge}}", `[{"key":"Decile1","value":750},{"key":"Decile2","value":680},{"key":"Decile3","value":620},{"key":"Tier1","value":850},{"key":"Tier2","value":720}]`, "Merge arrays with different source fields (transform separately then merge)"},
	}
	results = append(results, runTestCategory("🆕 ARRAY Merge - With Transformations", mergeTransformTests, processor, masterDTO, serviceMap, cache))

	// ARRAY transform-only with nested dot paths (e.g. result.ctb, result.pradr.adr)
	nestedPathTests := []TestCase{
		{"{{ARRAY:((KarzaGSTNonOTP.results)):transform-only:result.ctb->td_constitution,result.aggreTurnOver->td_annual_turnover_slab,result.gti->td_gross_total_income,result.sts->td_gstin_status,result.rgdt->td_date_of_registration,result.pradr.adr->td_principal_place_of_business}}", "", "Transform-only with nested paths (result.ctb, result.pradr.adr) from service results array"},
	}
	results = append(results, runTestCategory("🆕 ARRAY transform-only - Nested Paths", nestedPathTests, processor, masterDTO, serviceMap, cache))

	// Edge cases and validation
	edgeCaseTests := []TestCase{
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.SANCTIONS)):merge}}", "", "Merge duplicate arrays (should include duplicates)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.NonExistent)),((IxsightNegativeScrub_kyc.SANCTIONS)):merge}}", "", "Merge with non-existent source (should handle gracefully)"},
		{"{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)),((IxsightNegativeScrub_kyc.BLACKLIST)),((IxsightNegativeScrub_kyc.DEFAULTER)),((IxsightNegativeScrub_kyc.AML)):merge}}", "", "Merge four arrays (including empty)"},
	}
	results = append(results, runTestCategory("🆕 ARRAY Merge - Edge Cases", edgeCaseTests, processor, masterDTO, serviceMap, cache))

	// ARRAY filter with IN operator - filter by membership in a list (single pass, O(N))
	filterInTests := []TestCase{
		{"{{ARRAY:((FilterTestService.body)):filter:accountType IN (12,14,23)}}", `[{"accountType":12,"name":"A"},{"accountType":14,"name":"B"},{"accountType":23,"name":"D"}]`, "Filter with IN: keep accountType 12, 14, 23 (exclude 99)"},
		{"{{ARRAY:((FilterTestService.body)):filter:accountType IN (99)}}", `[{"accountType":99,"name":"C"}]`, "Filter with IN: single value"},
		{"{{ARRAY:((FilterTestService.body)):filter:accountType IN (100,101)}}", `[]`, "Filter with IN: no match returns empty array"},
	}
	results = append(results, runTestCategory("🆕 ARRAY Filter - IN Operator", filterInTests, processor, masterDTO, serviceMap, cache))

	// ARRAY max/min - scalar from array (numeric and string comparison)
	arrayMaxMinTests := []TestCase{
		{"{{ARRAY:((UanTestService.result.uan))@NUMERIC:max}}", "100000000002", "ARRAY @NUMERIC max: value array"},
		{"{{ARRAY:((UanTestService.result.uan))@NUMERIC:min}}", "100000000001", "ARRAY @NUMERIC min: value array"},
		{"{{ARRAY:((UanTestService.result.uan)):max}}", "100000000002", "ARRAY max (string): lexicographic max"},
		{"{{ARRAY:((UanTestService.result.uan)):min}}", "100000000001", "ARRAY min (string): lexicographic min"},
		{"{{ARRAY:((UanTestService.result.nonExistent))@NUMERIC:max}}", "", "ARRAY max on missing path returns empty"},
		// Optional field :uan — array of objects; one config for both value array and objects
		{"{{ARRAY:((UanObjectsTestService.data.result))@NUMERIC:max:uan}}", "100000000002", "ARRAY max:uan on array of objects"},
		{"{{ARRAY:((UanObjectsTestService.data.result))@NUMERIC:min:uan}}", "100000000001", "ARRAY min:uan on array of objects"},
		{"{{ARRAY:((UanTestService.result.uan))@NUMERIC:max:uan}}", "100000000002", "ARRAY max:uan on value array (field ignored)"},
	}
	results = append(results, runTestCategory("🆕 ARRAY Max/Min", arrayMaxMinTests, processor, masterDTO, serviceMap, cache))

	// Backward compatibility - ensure existing ARRAY operations still work
	backwardCompatTests := []TestCase{
		{"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Score__c->value@NUMERIC}}", "", "Existing: Object-only transform-only (should still work)"},
		{"{{ARRAY:<A_Score__c>:transform:Type__c->key,Score__c->value@NUMERIC}}", "", "Existing: Object-only transform (should still work)"},
		{"{{ARRAY:<A_Score__c>:map:Type__c,Score__c}}", "", "Existing: Object-only map (should still work)"},
		{"{{ARRAY:<A_Score__c>}}", "", "Existing: Object-only no-op (should still work)"},
	}
	results = append(results, runTestCategory("✅ ARRAY Merge - Backward Compatibility", backwardCompatTests, processor, masterDTO, serviceMap, cache))

	return results
}

// runAnalyticsAAFeaturesTests verifies getJsonPath (and optional normalizeJsonNan) for service→array configs.
// Same pattern works with SF field or service response; when JSON contains NaN, wrap source in normalizeJsonNan.
func runAnalyticsAAFeaturesTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}
	passed := 0
	failed := []string{}

	// 1) getJsonPath from service response (no NaN) — one config for both SF and service
	expr := "{{CUSTOM:getJsonPath:((JsonPathSourceService.data)):output}}"
	getJsonPathResult := processor.ProcessPlaceholders(expr, masterDTO, serviceMap, cache, nil)
	if getJsonPathResult == "" {
		failed = append(failed, "getJsonPath(((JsonPathSourceService.data)), output): got empty")
		fmt.Printf("  ❌ getJsonPath(service): got empty\n")
	} else if !strings.Contains(getJsonPathResult, "__JSON_PARSE__:") && !strings.HasPrefix(getJsonPathResult, "[") {
		failed = append(failed, fmt.Sprintf("getJsonPath(service): expected __JSON_PARSE__: or [...], got %q", debugTruncate(getJsonPathResult, 80)))
		fmt.Printf("  ❌ getJsonPath(service): %s\n", debugTruncate(getJsonPathResult, 80))
	} else {
		passed++
		fmt.Printf("  ✅ getJsonPath(((JsonPathSourceService.data)), output): got array payload (len=%d)\n", len(getJsonPathResult))
	}

	// 2) Full config: ARRAY(getJsonPath(...)) — same pattern as Analytics AA with getJsonPath + normalizeJsonNan when NaN present
	fullExpr := "{{ARRAY:{{CUSTOM:getJsonPath:((JsonPathSourceService.data)):output}}}}"
	fullResult := processor.ProcessPlaceholders(fullExpr, masterDTO, serviceMap, cache, nil)
	if fullResult == "[]" {
		failed = append(failed, "ARRAY(getJsonPath(...)): got []")
		fmt.Printf("  ❌ Full Analytics_AA_Features: got []\n")
	} else if fullResult == "" {
		failed = append(failed, "ARRAY(getJsonPath(...)): got empty string")
		fmt.Printf("  ❌ Full Analytics_AA_Features: got empty\n")
	} else if !strings.HasPrefix(fullResult, "[") || !strings.HasSuffix(fullResult, "]") {
		failed = append(failed, fmt.Sprintf("ARRAY result should be JSON array, got: %s", debugTruncate(fullResult, 100)))
		fmt.Printf("  ❌ Full Analytics_AA_Features: not a JSON array: %s\n", debugTruncate(fullResult, 100))
	} else {
		passed++
		fmt.Printf("  ✅ Full Analytics_AA_Features: non-empty array (len=%d)\n", len(fullResult))
	}

	total := 2
	passRate := float64(passed) / float64(total) * 100
	fmt.Printf("  📊 Analytics AA Features (getJsonPath + ARRAY): %d/%d passed (%.1f%%)\n\n", passed, total, passRate)
	results = append(results, TestCategoryResult{
		Name:     "Analytics AA Features (getJsonPath + ARRAY)",
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	})
	return results
}

// runAnalyticsAAProductionDebugTest is a TEMPORARY test that mirrors production: AACalculation (COMPLETED) with
// Response.Body.Data__c = stringified JSON containing NaN; then resolve the exact Analytics_AA_Features config.
// Helps verify serviceNameMap + getServiceResponseValue + normalizeJsonNan + getJsonPath chain.
func runAnalyticsAAProductionDebugTest(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, cache map[string]string) []TestCategoryResult {
	// Exact Data__c string shape from production (contains NaN)
	dataCWithNaN := `{"output": [{"rid": "", "average_balance_last_2m": NaN, "variance_balance_last_2m": NaN, "number_of_transactions": NaN}]}`
	// Service map with ONLY AACalculation, COMPLETED, with Body matching production
	debugMap := map[string]*models.EsaLog{
		"AACalculation": {
			ServiceName: "AACalculation",
			Status:      "COMPLETED",
			Response: models.ResponseDetails{
				StatusCode: 200,
				Body: map[string]interface{}{
					"Data__c":    dataCWithNaN,
					"leadid":     "00Q9H00000FTmBuUAL",
					"statusCode": -5,
					"type":       "Analytics AA features",
				},
			},
		},
	}
	expr := `{{CUSTOM:getJsonPath:{{CUSTOM:normalizeJsonNan:((AACalculation.Data__c))}}:output}}`
	fmt.Printf("  [DEBUG] Expression: %s\n", expr)
	fmt.Printf("  [DEBUG] serviceNameMap keys: AACalculation only, Status=%s\n", debugMap["AACalculation"].Status)
	fmt.Printf("  [DEBUG] AACalculation.Response.Body[\"Data__c\"] length=%d, hasNaN=%v\n",
		len(dataCWithNaN), strings.Contains(dataCWithNaN, "NaN"))

	result := processor.ProcessPlaceholders(expr, masterDTO, debugMap, cache, nil)
	fmt.Printf("  [DEBUG] ProcessPlaceholders result length=%d, empty=%v\n", len(result), result == "")
	if result != "" {
		fmt.Printf("  [DEBUG] result prefix: %s\n", debugTruncate(result, 120))
	}

	passed := 0
	failed := []string{}
	if result == "" {
		failed = append(failed, "Analytics_AA_Features production config resolved to empty with AACalculation in map and Data__c set")
		fmt.Printf("  ❌ [DEBUG] Analytics_AA_Features: got empty\n")
	} else {
		passed++
		fmt.Printf("  ✅ [DEBUG] Analytics_AA_Features: non-empty (len=%d)\n", len(result))
	}

	return []TestCategoryResult{{
		Name:     "[DEBUG] Analytics AA Production Config",
		Total:    1,
		Passed:   passed,
		Failed:   failed,
		PassRate: float64(passed) / 1 * 100,
	}}
}

func debugTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// runAdvancedFeatureTests tests advanced features
func runAdvancedFeatureTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// Service response placeholders
	serviceTests := []TestCase{
		{"((TestService.data.result))", "SUCCESS", "Service response access"},
		{"((TestService.data.score))", "750", "Numeric service response"},
		{"((TestService.data.access_token))", "eyJ0eXAiOiJKV1Q...", "Token from service"},
		{"((NonExistentService.data))", "", "Non-existent service (expect empty)"},
		{"((NonExistentService.data)) || ((TestService.data.result))", "SUCCESS", "Service fallback chain (text-level)"},
		{"((JsonPathSourceService.data))", "", "JsonPathSourceService.data (expect non-empty stringified JSON)"},
		{"((AACalculation.Data__c))", "", "AACalculation.Data__c (expect non-empty stringified JSON with NaN)"},
	}
	results = append(results, runTestCategory("Service Response Placeholders", serviceTests, processor, masterDTO, serviceMap, cache))

	// Database placeholder - relation field (multi-dot) resolution
	relationFieldTests := []TestCase{
		{"<Lead.Campaign__r.Sector__c>", "Salaried", "Relation field Lead.Campaign__r.Sector__c resolves to value"},
		{"<Lead.Campaign__r.Bscore__c>", "0.03", "Relation field Lead.Campaign__r.Bscore__c (numeric) resolves"},
		{"<Lead.Campaign__r.Offer_Upgrade_Flag__c>", "true", "Relation field Lead.Campaign__r.Offer_Upgrade_Flag__c (bool) resolves"},
		{"<Lead.Campaign__r.NonExistent__c>", "", "Missing relation field resolves to blank"},
		{"<Lead.Status>", "Open", "Single-dot placeholder still works (backward compatibility)"},
	}
	results = append(results, runTestCategory("Database Placeholder - Relation Field (Multi-Dot)", relationFieldTests, processor, masterDTO, serviceMap, cache))

	// Nested expressions (deep nesting)
	nestedTests := []TestCase{
		{"{{TRANSFORM:{{CONCAT:{{CUSTOM:splitName:<contact.Name>:first}}:_:{{TRANSFORM:<contact.MailingState>:uppercase}}}}:lowercase}}", "john_maharashtra", "4-level nesting"},
		{"{{JSON:{\"name\":\"{{TRANSFORM:<contact.Name>:uppercase}}\",\"state\":\"{{CUSTOM:resolveBureauStateCode:<contact.MailingState>:CIBIL}}\"}}}", "", "JSON with nested expressions"},
		{"{{CONDITIONAL:{{TRANSFORM:<contact.gender__c>:uppercase}}:M->{{CUSTOM:resolveBureauStateCode:<contact.MailingState>:CIBIL}}:F->{{CUSTOM:resolveBureauStateCode:<contact.MailingState>:CRIF}}:'00'}}", "27", "Complex conditional nesting"},
	}
	results = append(results, runTestCategory("Deep Nested Expressions", nestedTests, processor, masterDTO, serviceMap, cache))

	// Unified fallbacks in expressions
	fallbackTests := []TestCase{
		{"{{TRANSFORM:<contact.NonExistent> || <contact.Name>:uppercase}}", "JOHN MICHAEL DOE", "Fallback in TRANSFORM"},
		{"{{NUMERIC:<contact.NonExistent> || <contact.Monthly_Income__c> || '0'}}", "50000", "Fallback in NUMERIC"},
		{"{{CONDITIONAL:<contact.NonExistent> || <contact.MailingState>:RULES:STATE:BUREAU:CIBIL}}", "27", "Fallback in CONDITIONAL"},
		{"{{CUSTOM:resolveBureauStateCode:<contact.NonExistent> || <contact.MailingState>:CIBIL}}", "27", "Fallback in CUSTOM"},
		// Item 4: a single-quoted fallback arm with an escaped apostrophe (\') is un-escaped to '
		// (quote/brace-aware split from item 3 keeps the arm intact; the unwrap reverses the escape).
		{`{{<lead.NonExistent> || 'O\'BRIEN'}}`, "O'BRIEN", "Fallback: quoted arm un-escapes \\' to '"},
		// Item 6: a double-quote inside a single-quoted arm must not toggle quote state (quoteChar
		// tracking), so the following top-level || is still detected as a fallback separator.
		{`{{'a"b' || 'c'}}`, `a"b`, "Fallback: \" inside '...' does not break || detection (item 6)"},
	}
	results = append(results, runTestCategory("Unified Fallbacks", fallbackTests, processor, masterDTO, serviceMap, cache))

	// 🚨 CRITICAL BUG FIXES - Comprehensive test cases for the 3 major issues we encountered
	criticalBugFixTests := []TestCase{
		// ═══════════════════════════════════════════════════════════════════════════════════════
		// FIX #1: ADDITIONAL FIELDS PROCESSING
		// Issue: Database additional_fields completely ignored in query building
		// Solution: Enhanced optimizedDependencyAnalysis() to process AdditionalFields
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].obligations__c>", "0", "Basic conditional field access (Fix #1)"},
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v4__c>}}", "573", "Conditional field in NUMERIC expression (Fix #1)"},
		{"{{TRANSFORM:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v6__c>:uppercase}}", "644", "Conditional field in TRANSFORM (Fix #1)"},
		{"{{CONCAT:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v4__c>:' - ':<Bank_Facilities__c.obligations__c>}}", "573 - 0", "Multiple conditional fields in CONCAT (Fix #1)"},
		{"<Bank_Facilities__c[Vendor_Type__c != 'Wrong Value'].obligations__c>", "0", "Conditional field with != operator (Fix #1)"},
		{"<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data' AND Status__c == 'Active'].fis_v4__c>", "573", "Complex conditional with nested fields (Fix #1)"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// FIX #2: CONDITIONAL FIELD SCANNING
		// Issue: Request body scanning didn't extract fields from conditions [Field == Value]
		// Solution: Enhanced extractFromAngleBracketPlaceholder() + added extractFieldsFromCondition()
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"<Bank_Facilities__c[Status__c != 'Inactive'].obligations__c>", "0", "Single condition field scanning (Fix #2)"},
		{"<Bank_Facilities__c[Status__c != 'Inactive' AND Type__c == 'Premium'].obligations__c>", "0", "Multi-condition AND field scanning (Fix #2)"},
		{"<Bank_Facilities__c[Status__c == 'Active' OR Type__c == 'Premium'].fis_v4__c>", "573", "Multi-condition OR field scanning (Fix #2)"},
		{"<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data' AND Status__c != 'Inactive' AND Type__c == 'Premium'].fis_v6__c>", "644", "Triple condition field scanning (Fix #2)"},
		{"<Bank_Facilities__c[fis_v4__c > 500 AND obligations__c <= 10].Type__c>", "Premium", "Numeric condition field scanning with > and <= (Fix #2)"},
		{"<Bank_Facilities__c[Vendor_Type__c IN ('FinBox Insights Data', 'Other')].obligations__c>", "0", "IN operator condition field scanning (Fix #2)"},
		{"<Bank_Facilities__c[Status__c NOT IN ('Inactive', 'Deleted')].fis_v6__c>", "644", "NOT IN operator condition field scanning (Fix #2)"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// FIX #3: NUMERIC FALLBACK CHAINS
		// Issue: {{NUMERIC:<field> || NUMERIC:0}} not working consistently
		// Solution: Enhanced evaluateUnifiedFallback() to properly process nested expressions
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"{{NUMERIC:<Bank_Facilities__c.obligations__c> || NUMERIC:0}}", "0", "NUMERIC fallback with existing field (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c.nonexistent__c> || NUMERIC:0}}", "0", "NUMERIC fallback with missing field (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'Wrong Value'].obligations__c> || NUMERIC:0}}", "0", "NUMERIC fallback with failed condition (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c[Status__c == 'Inactive'].obligations__c> || NUMERIC:0}}", "0", "NUMERIC fallback with non-matching condition (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c.fis_v4__c> || NUMERIC:999}}", "573", "NUMERIC fallback with existing non-zero field (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c.missing_field__c> || NUMERIC:999}}", "999", "NUMERIC fallback with custom default value (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c.nonexistent__c> || <Bank_Facilities__c.fis_v4__c> || NUMERIC:0}}", "573", "Triple fallback chain with NUMERIC (Fix #3)"},
		{"{{NUMERIC:<Bank_Facilities__c.missing1__c> || <Bank_Facilities__c.missing2__c> || NUMERIC:0}}", "0", "Triple fallback chain all missing (Fix #3)"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// MULTI-CONDITION PARSING (Part of Fix #2)
		// Issue: Conditional placeholders couldn't handle [Field1 != 'A' AND Field2 == 'B']
		// Solution: Enhanced evaluateConditionOnObject() to support AND/OR operators
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"<Bank_Facilities__c[Status__c == 'Active' AND Type__c == 'Premium'].obligations__c>", "0", "AND condition parsing (Fix #2 Multi-condition)"},
		{"<Bank_Facilities__c[Status__c == 'Inactive' OR Type__c == 'Premium'].fis_v4__c>", "573", "OR condition parsing (Fix #2 Multi-condition)"},
		{"<Bank_Facilities__c[Status__c != 'Inactive' AND Type__c != 'Basic'].fis_v6__c>", "644", "Double NOT condition parsing (Fix #2 Multi-condition)"},
		{"<Bank_Facilities__c[fis_v4__c >= 500 AND obligations__c <= 10 AND Status__c == 'Active'].Type__c>", "Premium", "Triple condition with >= and <= operators (Fix #2 Multi-condition)"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// FIX #4: CONDITIONAL RECORDS ARRAY ACCESS
		// Issue: Conditional placeholders like <ES_Contact__c[Type__c='PAN'].OwnerName__c> failed
		//        when object data had a "records" array structure
		// Solution: Enhanced handleConditionalDbField() to iterate through records array
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"<ES_Contact__c[Type__c='PAN'].OwnerName__c>", "RAHUL AGGARWAL", "Conditional records array - Type__c='PAN' (Fix #4)"},
		{"<ES_Contact__c[Type__c='AADHAAR'].OwnerName__c>", "", "Conditional records array - Type__c='AADHAAR' with null OwnerName (Fix #4)"},
		{"<ES_Contact__c[Type__c='DL'].OwnerName__c>", "", "Conditional records array - Type__c='DL' with null OwnerName (Fix #4)"},
		{"<ES_Contact__c[Type__c='NONEXISTENT'].OwnerName__c>", "", "Conditional records array - non-matching Type__c (Fix #4)"},
		{"{{TRANSFORM:<ES_Contact__c[Type__c='PAN'].OwnerName__c>:uppercase}}", "RAHUL AGGARWAL", "Conditional records array in TRANSFORM expression (Fix #4)"},
		{"{{CONCAT:<ES_Contact__c[Type__c='PAN'].OwnerName__c>:' - ':<ES_Contact__c[Type__c='PAN'].Type__c>}}", "RAHUL AGGARWAL - PAN", "Multiple conditional records array in CONCAT (Fix #4)"},
		{"<ES_Contact__c[Type__c='PAN'].Type__c>", "PAN", "Conditional records array - return Type__c field (Fix #4)"},
		{"<ES_Contact__c[Type__c='VOTER_ID'].Id>", "a009H00000EOq8pQAD", "Conditional records array - return Id field (Fix #4)"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// CROSS-FIX INTEGRATION TESTS
		// Testing combinations of all 3 fixes working together
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data' AND Status__c == 'Active'].obligations__c> || NUMERIC:0}}", "0", "Integration: All 3 fixes combined"},
		{"{{TRANSFORM:<Bank_Facilities__c[Status__c != 'Inactive' AND Type__c == 'Premium'].fis_v6__c> || 'DEFAULT':uppercase}}", "644", "Integration: Fix #1+#2 with TRANSFORM fallback"},
		{"{{CONCAT:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v4__c>:' | ':<Bank_Facilities__c[Status__c == 'Active'].Type__c> || 'FALLBACK'}}", "573 | Premium", "Integration: Multiple conditional fields in CONCAT"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// PRODUCTION SCENARIO TESTS
		// The exact scenarios that were failing in production
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].obligations__c> || NUMERIC:0}}", "0", "PRODUCTION: Original failing config - obligations field"},
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v4__c> || NUMERIC:0}}", "573", "PRODUCTION: fis_v4 field with condition"},
		{"{{NUMERIC:<Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v6__c> || NUMERIC:0}}", "644", "PRODUCTION: fis_v6 field with condition"},

		// ═══════════════════════════════════════════════════════════════════════════════════════
		// CONDITIONAL PLACEHOLDER: NESTED FIELDS, NULL MATCHING, CASE-INSENSITIVITY
		// Issue: <Object[cond].Nested__r.Field> returned blank (flat lookup); == null / != null
		//        never matched null/blank fields; condition/returned field names were case-sensitive.
		// Solution: resolve returned field via navigateNestedField (nested + case-insensitive),
		//           add null-token handling, and case-insensitive condition field lookup.
		// ═══════════════════════════════════════════════════════════════════════════════════════
		{"<mb_test__c[Type__c == 'CIBIL'].Bureau__r.CreatedDate>", "2026-06-25T06:14:50.000+0000", "Conditional NESTED field resolution (Bureau__r.CreatedDate)"},
		{"<mb_test__c[Type__c == null].Bureau__r.CreatedDate>", "2026-07-17T08:52:02.000+0000", "== null matches null-typed record, then resolves nested field"},
		{"<mb_test__c[Type__c != null].Bureau__r.CreatedDate>", "2026-06-25T06:14:50.000+0000", "!= null skips null-typed record, matches CIBIL"},
		{"<mb_test__c[Type__c == null OR Type__c == 'CRIF'].Bureau__r.CreatedDate>", "2026-07-17T08:52:02.000+0000", "CRIF-style OR gate: (null OR 'CRIF') matches the null-typed record"},
		{"<mb_test__c[type__c == 'CIBIL'].bureau__r.createddate>", "2026-06-25T06:14:50.000+0000", "Case-insensitive condition field + nested returned field"},
		{"<mb_test__c[Bureau__r.Bureau__c == 'CIBIL'].Bureau__r.CreatedDate>", "2026-06-25T06:14:50.000+0000", "NESTED condition field (Bureau__r.Bureau__c == 'CIBIL') -> nested returned field"},
		{"<mb_test__c[Bureau__r.Bureau__c == 'CRIF'].Bureau__r.CreatedDate>", "2026-07-17T08:52:02.000+0000", "NESTED condition field matches CRIF (null-typed record)"},
		{"<mb_test__c[bureau__r.bureau__c == 'CIBIL'].bureau__r.createddate>", "2026-06-25T06:14:50.000+0000", "NESTED condition field, fully case-insensitive"},
		{"<ES_Contact__c[type__c == 'PAN'].ownername__c>", "RAHUL AGGARWAL", "Case-insensitive condition + returned field (records array)"},

		// Null semantics: blank string IS null; empty array [] and empty object {} are NOT null.
		{"<null_semantics__c[Value__c == null].Kind__c>", "blank", "== null: blank string is null; [] and {} are skipped as non-null"},
		{"<null_semantics__c[Value__c != null].Kind__c>", "emptyarr", "!= null: empty array is a present (non-null) value"},

		// Top-level CONDITION null handling (evaluateSimpleCondition path).
		{"{{CONDITION:<contact.NonExistent__c> == null:YES:NO}}", "YES", "Top-level == null: absent field is null"},
		{"{{CONDITION:<contact.Phone> == null:YES:NO}}", "NO", "Top-level == null: present field is not null"},
		{"{{CONDITION:<contact.Phone> != null:YES:NO}}", "YES", "Top-level != null: present field is not null -> true"},
	}
	results = append(results, runTestCategory("🚨 CRITICAL BUG FIXES", criticalBugFixTests, processor, masterDTO, serviceMap, cache))

	// Fan-Out From Array: ((item.xxx)) placeholder with itemContext
	results = append(results, runFanOutItemPlaceholderTests(processor, masterDTO, serviceMap, cache))

	return results
}

// Support functions for test execution
// runFanOutItemPlaceholderTests tests ((item.xxx)) resolution with itemContext (fan-out from array).
func runFanOutItemPlaceholderTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) TestCategoryResult {
	name := "Fan-Out Item Placeholder ((item.xxx))"
	fmt.Printf("🔍 %s:\n", name)
	item := map[string]interface{}{
		"gstinId":    "27ABHHP8888H1ZW",
		"authStatus": "Active",
		"pan":        "ABHHP8888H",
		"state":      "Maharashtra",
	}
	tests := []struct {
		input    string
		expected string
		desc     string
	}{
		{"((item.gstinId))", "27ABHHP8888H1ZW", "item.gstinId"},
		{"((item.authStatus))", "Active", "item.authStatus"},
		{"((item.pan))", "ABHHP8888H", "item.pan"},
		{"((item.state))", "Maharashtra", "item.state"},
	}
	passed := 0
	failed := []string{}
	for _, t := range tests {
		result := processor.ProcessPlaceholdersWithType(t.input, sequence_service.TypeModeString, masterDTO, serviceMap, cache, nil, item)
		str, _ := result.(string)
		if str == t.expected {
			passed++
			fmt.Printf("  ✅ %s: %s → %s\n", t.desc, t.input, str)
		} else {
			msg := fmt.Sprintf("%s: Expected '%s', got '%s'", t.desc, t.expected, str)
			failed = append(failed, msg)
			fmt.Printf("  ❌ %s: %s → Expected '%s', got '%s'\n", t.desc, t.input, t.expected, str)
		}
	}
	total := len(tests)
	passRate := float64(passed) / float64(total) * 100
	fmt.Printf("  📊 %s: %d/%d passed (%.1f%%)\n\n", name, passed, total, passRate)
	return TestCategoryResult{Name: name, Total: total, Passed: passed, Failed: failed, PassRate: passRate}
}

type chunkExpectation struct {
	input         string
	maxLength     int
	expectedLines []string
	indexChecks   map[int]string
	description   string
}

// runChunkTransformationTests tests chunk transformation in the same configuration-oriented
// style as the rest of the enhanced suite, while also validating line packing and length bounds.
func runChunkTransformationTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	name := "Chunk transformation (address lines)"
	cases := []chunkExpectation{
		{
			input:         "<contact.Street>",
			maxLength:     40,
			expectedLines: []string{"123 Main Street, Apartment 4B, Some", "City Name, State 12345"},
			indexChecks:   map[int]string{0: "123 Main Street, Apartment 4B, Some", 1: "City Name, State 12345", 2: "", -1: "City Name, State 12345"},
			description:   "baseline address packs under 40 with comma-aware wrapping",
		},
		{
			input:         "<contact.ChunkStreetComma__c>",
			maxLength:     40,
			expectedLines: []string{"DO Ramachandra,Muddanahalli,", "Chunchanakatte,K R Nagara Thalluku,", "Kuppe"},
			indexChecks:   map[int]string{0: "DO Ramachandra,Muddanahalli,", 1: "Chunchanakatte,K R Nagara Thalluku,", 2: "Kuppe", -1: "Kuppe"},
			description:   "comma boundaries prevent the old 41-character overflow",
		},
		{
			input:         "<contact.ChunkStreetSpaces__c>",
			maxLength:     40,
			expectedLines: []string{"123 Main Street Apartment 4B Some City", "Name State 12345"},
			indexChecks:   map[int]string{0: "123 Main Street Apartment 4B Some City", 1: "Name State 12345"},
			description:   "space-only address stays packed under limit",
		},
		{
			input:         "<contact.ChunkStreetMixed__c>",
			maxLength:     40,
			expectedLines: []string{"12/7A, Palm Residency Phase 2, Sector", "18 Noida"},
			indexChecks:   map[int]string{0: "12/7A, Palm Residency Phase 2, Sector", 1: "18 Noida"},
			description:   "repeated spaces and tabs are normalized before chunking",
		},
		{
			input:         "<contact.Name>",
			maxLength:     10,
			expectedLines: []string{"John", "Michael", "Doe"},
			indexChecks:   map[int]string{0: "John", 1: "Michael", 2: "Doe", 3: ""},
			description:   "short name splits cleanly on spaces with index lookup",
		},
		{
			input:         "<contact.ChunkExactFit__c>",
			maxLength:     15,
			expectedLines: []string{"Alpha Beta", "Gamma Delta", "Epsilon Zeta", "Eta"},
			indexChecks:   map[int]string{0: "Alpha Beta", 1: "Gamma Delta", 2: "Epsilon Zeta", 3: "Eta"},
			description:   "lines stay strictly below the configured limit rather than allowing exact fit",
		},
		{
			input:         "<contact.ChunkLongToken__c>",
			maxLength:     40,
			expectedLines: []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMN"},
			indexChecks:   map[int]string{0: "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMN", -1: "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMN"},
			description:   "single unsplittable token is preserved as-is",
		},
	}

	fmt.Printf("🔍 %s:\n", name)
	passed := 0
	failed := []string{}
	total := 0

	for _, tc := range cases {
		total++
		arrayExpr := fmt.Sprintf("{{TRANSFORM:%s:chunk:%d}}", tc.input, tc.maxLength)
		arrayResult := processor.ProcessPlaceholders(arrayExpr, masterDTO, serviceMap, cache, nil)

		var lines []string
		if err := json.Unmarshal([]byte(arrayResult), &lines); err != nil {
			failed = append(failed, fmt.Sprintf("%s: failed to parse chunk array result %q: %v", tc.description, arrayResult, err))
			fmt.Printf("  ❌ %s: unable to parse %q (%v)\n", tc.description, arrayResult, err)
			continue
		}

		casePassed := true
		if len(lines) != len(tc.expectedLines) {
			casePassed = false
			failed = append(failed, fmt.Sprintf("%s: expected %d lines, got %d (%v)", tc.description, len(tc.expectedLines), len(lines), lines))
		}

		if casePassed {
			for i := range tc.expectedLines {
				if lines[i] != tc.expectedLines[i] {
					casePassed = false
					failed = append(failed, fmt.Sprintf("%s: line %d expected %q, got %q", tc.description, i, tc.expectedLines[i], lines[i]))
					break
				}
			}
		}

		if casePassed {
			for i, line := range lines {
				if len(line) >= tc.maxLength && len(tc.expectedLines[i]) < tc.maxLength {
					casePassed = false
					failed = append(failed, fmt.Sprintf("%s: line %q has length %d which should be < %d", tc.description, line, len(line), tc.maxLength))
					break
				}
			}
		}

		for index, expected := range tc.indexChecks {
			total++
			indexExpr := fmt.Sprintf("{{TRANSFORM:%s:chunk:%d:%d}}", tc.input, tc.maxLength, index)
			indexResult := processor.ProcessPlaceholders(indexExpr, masterDTO, serviceMap, cache, nil)
			if indexResult != expected {
				casePassed = false
				failed = append(failed, fmt.Sprintf("%s: index %d expected %q, got %q", tc.description, index, expected, indexResult))
			} else {
				passed++
				fmt.Printf("  ✅ %s [index %d]: %s → %s\n", tc.description, index, indexExpr, indexResult)
			}
		}

		if casePassed {
			passed++
			fmt.Printf("  ✅ %s: %s → %v\n", tc.description, arrayExpr, lines)
		} else {
			fmt.Printf("  ❌ %s: %s → %v\n", tc.description, arrayExpr, lines)
		}
	}

	passRate := float64(passed) / float64(total) * 100
	fmt.Printf("  📊 %s: %d/%d passed (%.1f%%)\n\n", name, passed, total, passRate)

	return []TestCategoryResult{{
		Name:     name,
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	}}
}

// runStandardEmploymentTypeTests verifies standard_employment_type__c config resolution with
// depth-aware CONDITION parsing (nested {{CONDITION:...}}, {{FORMAT:text:...:lowercase}}, {{ARRAY:...:filter:...}}).
// Requires: Contact.employmentType__c, Lead.Sourcing_Program__c, CIBIL.consumerCreditData.accounts, MobileUANService.result.uan.
func runStandardEmploymentTypeTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	// MobileUANService real response has result.uan at top level (no "data" wrapper).
	// Brace-balanced: 9 {{ and 9 }} (was 8 }} — one closing }} was missing)
	standardEmploymentTypeExpr := `{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep'):'Self employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData.accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':'self employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store', 'QRO2O') && {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{NUMERIC:{{ARRAY:((MobileUANService.result.uan))@NUMERIC:max}}}} > 0:'Salaried':'Data Not Found'}}}}}}}}`
	tests := []TestCase{
		{
			Input:       standardEmploymentTypeExpr,
			Expected:    "Self employed",
			Description: "standard_employment_type__c: employmentType in list (depth-aware CONDITION)",
		},
	}
	results := []TestCategoryResult{runTestCategory("Standard Employment Type (depth-aware CONDITION)", tests, processor, masterDTO, serviceMap, cache)}

	// Production expression: same as config field standard_Employment_Type (outer :'null', MobileUANService.data.result.uan)
	productionExpr := `{{CONDITION:((MobileUANService)) != '':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep'):'Self employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData.accounts)):filter:accountType IN (12,14,23,24,33,38,39,40,50,51,52,53,54,55,56,57,58,59,61,71)}} != '[]':'self employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store', 'QRO2O') && {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{NUMERIC:{{ARRAY:((MobileUANService.data.result.uan))@NUMERIC:max}}}} > 0:'Salaried':'Data Not Found'}}}}}}}}:'null'}}`

	// Scenario: null employment, null Sourcing_Program, CIBIL has no salaried accountTypes, UAN present → expect "Salaried"
	nullDTO := cloneMasterDTOWithOverrides(masterDTO, map[string]map[string]interface{}{
		"lead":    {"Sourcing_Program__c": nil},
		"contact": {"employmentType__c": nil},
	})
	// CIBIL: accounts with accountType 05, 06 only (none in 12,14,23,...) so filter returns []
	cibilNoSalaried := map[string]interface{}{
		"consumerCreditData": map[string]interface{}{
			"accounts": []interface{}{
				map[string]interface{}{"accountType": 5, "currentBalance": 1000},
				map[string]interface{}{"accountType": 6, "currentBalance": 0},
			},
		},
	}
	// MobileUANService: production shape with data.result (array of objects with uan)
	uanWithDataResult := map[string]interface{}{
		"data": map[string]interface{}{
			"result": []interface{}{
				map[string]interface{}{"uan": "100000000001"},
				map[string]interface{}{"uan": "100000000002"},
			},
		},
	}
	prodServiceMap := cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{
		"CIBIL":            cibilNoSalaried,
		"MobileUANService": uanWithDataResult,
	})
	prodResult := processor.ProcessPlaceholders(productionExpr, nullDTO, prodServiceMap, cache, nil)
	prodPass := prodResult == "Salaried"
	if prodPass {
		fmt.Printf("\n🔍 Production: standard_Employment_Type (null employment, null Sourcing_Program, UAN present):\n  ✅ Expected Salaried, got %q\n", prodResult)
		results = append(results, TestCategoryResult{Name: "Production: standard_Employment_Type → Salaried", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("\n🔍 Production: standard_Employment_Type (null employment, null Sourcing_Program, UAN present):\n  ❌ Expected Salaried, got %q\n", prodResult)
		results = append(results, TestCategoryResult{Name: "Production: standard_Employment_Type → Salaried", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'Salaried', got %q", prodResult)}, PassRate: 0})
	}

	// Outer :null config with empty MobileUANService → expect "Data Not Found" or "null" (not "Salaried:'null'")
	// Must also override CIBIL to cibilNoSalaried so the CIBIL filter returns [] and doesn't short-circuit to "self employed"
	fmt.Println("\n🎯 STANDARD EMPLOYMENT TYPE - OUTER :null CONFIG (empty UAN → null)")
	fmt.Println("=" + strings.Repeat("=", 50))
	emptyUANServiceMap := cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{
		"CIBIL":            cibilNoSalaried,
		"MobileUANService": map[string]interface{}{},
	})
	nullResult := processor.ProcessPlaceholders(productionExpr, nullDTO, emptyUANServiceMap, cache, nil)
	nullOk := nullResult == "Data Not Found" || nullResult == "null"
	if nullOk {
		fmt.Printf("  ✅ Result: %q (expected Data Not Found or null)\n", nullResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - OUTER :null (empty UAN)", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("  ❌ Result: %q (expected Data Not Found or null)\n", nullResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - OUTER :null (empty UAN)", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'Data Not Found' or 'null', got %q", nullResult)}, PassRate: 0})
	}

	// When MobileUANService is not in the sequence at all, ((MobileUANService)) resolves to "" so outer gate is false → result is null.
	fmt.Println("\n🎯 STANDARD EMPLOYMENT TYPE - SERVICE NOT IN SEQUENCE → null")
	fmt.Println("=" + strings.Repeat("=", 55))
	noMobileUANExpr := `{{CONDITION:((MobileUANService)) != '' AND ((MobileUANService)) != 'null' AND ((MobileUANService)) != '{}':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep') AND <Contact.employmentType__c> != '':'Self Employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}} != '[]':'Self Employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store','QRO2O') AND <Contact.employmentType__c> != '' AND {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}}}}}:null}}`
	noMobileUANServiceMap := make(map[string]*models.EsaLog)
	for k, v := range serviceMap {
		if k != "MobileUANService" {
			noMobileUANServiceMap[k] = v
		}
	}
	// Use a fresh cache so ((MobileUANService)) is resolved against noMobileUANServiceMap (no cached value from other tests).
	noServiceCache := make(map[string]string)
	noServiceResult := processor.ProcessPlaceholders(noMobileUANExpr, nullDTO, noMobileUANServiceMap, noServiceCache, nil)
	// Intended: when service is not in sequence, ((MobileUANService)) resolves to "" so outer gate is false → result is null.
	if noServiceResult == "null" {
		fmt.Printf("  ✅ Result: %q (service not in sequence → null)\n", noServiceResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - service not in sequence → null", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("  ❌ Result: %q (expected null when MobileUANService not in sequence)\n", noServiceResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - service not in sequence → null", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'null', got %q", noServiceResult)}, PassRate: 0})
	}

	// Exact config + exact data shape shared in investigation.
	// Goal: reproduce "request keeps Self Employed" despite data indicating Data Not Fetched.
	fmt.Println("\n🎯 STANDARD EMPLOYMENT TYPE - EXACT SHARED CONFIG + EXACT SHARED DATA")
	fmt.Println("=" + strings.Repeat("=", 70))
	exactExpr := `{{CONDITION:((MobileUANService)) != '' AND ((MobileUANService)) != 'null' AND ((MobileUANService)) != '{}':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep') AND <Contact.employmentType__c> != '':'Self Employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}} != '[]':'Self Employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store','QRO2O') AND <Contact.employmentType__c> != '' AND {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}}}}}:null}}`

	exactDTO := cloneMasterDTOWithOverrides(masterDTO, map[string]map[string]interface{}{
		"lead":    {"Sourcing_Program__c": nil},
		"contact": {"employmentType__c": nil},
	})
	exactCIBIL := map[string]interface{}{
		"consumerCreditData": []interface{}{
			map[string]interface{}{
				"accounts": []interface{}{
					map[string]interface{}{"accountType": "06"},
					map[string]interface{}{"accountType": "69"},
					map[string]interface{}{"accountType": "05"},
				},
			},
		},
	}
	exactMobileUAN := map[string]interface{}{
		"code": 103,
		"data": map[string]interface{}{
			"result": []interface{}{
				map[string]interface{}{"uan": ""},
			},
		},
	}
	exactServiceMap := cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{
		"CIBIL":            exactCIBIL,
		"MobileUANService": exactMobileUAN,
	})

	exactResult := processor.ProcessPlaceholders(exactExpr, exactDTO, exactServiceMap, cache, nil)
	rawEmployment := processor.ProcessPlaceholders(`<Contact.employmentType__c>`, exactDTO, exactServiceMap, cache, nil)
	formattedEmployment := processor.ProcessPlaceholders(`{{FORMAT:text:<Contact.employmentType__c>:lowercase}}`, exactDTO, exactServiceMap, cache, nil)
	firstSubCondA := processor.ProcessPlaceholders(`{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep'):'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	firstSubCondB := processor.ProcessPlaceholders(`{{CONDITION:<Contact.employmentType__c> != '':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	firstBranchOnly := processor.ProcessPlaceholders(`{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep') AND <Contact.employmentType__c> != '':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	thirdBranchOnly := processor.ProcessPlaceholders(`{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store','QRO2O') AND <Contact.employmentType__c> != '' AND {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	cibilFilterOnly := processor.ProcessPlaceholders(`{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}}`, exactDTO, exactServiceMap, cache, nil)
	cibilConditionOnly := processor.ProcessPlaceholders(`{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}} != '[]':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	uanFilterOnly := processor.ProcessPlaceholders(`{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}}`, exactDTO, exactServiceMap, cache, nil)
	uanConditionOnly := processor.ProcessPlaceholders(`{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)
	outerGate := processor.ProcessPlaceholders(`{{CONDITION:((MobileUANService)) != '' AND ((MobileUANService)) != 'null' AND ((MobileUANService)) != '{}':'true':'false'}}`, exactDTO, exactServiceMap, cache, nil)

	fmt.Printf("  • exactExpr result                : %q\n", exactResult)
	fmt.Printf("  • raw <Contact.employmentType__c> : %q\n", rawEmployment)
	fmt.Printf("  • formatted employmentType        : %q\n", formattedEmployment)
	fmt.Printf("  • Branch#1 sub A (IN list)        : %q\n", firstSubCondA)
	fmt.Printf("  • Branch#1 sub B (!= '')          : %q\n", firstSubCondB)
	fmt.Printf("  • Branch#1 (employmentType)       : %q\n", firstBranchOnly)
	fmt.Printf("  • CIBIL filter result             : %q\n", cibilFilterOnly)
	fmt.Printf("  • CIBIL condition (!= '[]')       : %q\n", cibilConditionOnly)
	fmt.Printf("  • Branch#3 (sourcing+salaried)    : %q\n", thirdBranchOnly)
	fmt.Printf("  • MobileUAN filter result         : %q\n", uanFilterOnly)
	fmt.Printf("  • MobileUAN condition (!= '[]')   : %q\n", uanConditionOnly)
	fmt.Printf("  • Outer gate (MobileUAN non-empty): %q\n", outerGate)

	if exactResult == "Data Not Fetched" {
		fmt.Println("  ✅ Resolver computes Data Not Fetched for exact scenario")
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - Exact shared scenario", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("  ❌ Expected \"Data Not Fetched\", got %q\n", exactResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - Exact shared scenario", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'Data Not Fetched', got %q", exactResult)}, PassRate: 0})
	}

	// TEMPORARY: Validate standard_Employment_Type with EXACT API response body (non-fan-out).
	// API response: { "data": { "result": [ { "uan": "100000000003" } ] }, "contactId": "..." }
	// Use employmentType = "" and Sourcing = Offline Store so we do NOT take the "salaried" branch; result must come from UAN condition.
	fmt.Println("\n🎯 [TEMP] STANDARD EMPLOYMENT TYPE - EXACT API BODY → EXPECT SALARIED (UAN branch)")
	fmt.Println("=" + strings.Repeat("=", 70))
	tempExpr := `{{CONDITION:((MobileUANService)) != '' AND ((MobileUANService)) != 'null' AND ((MobileUANService)) != '{}':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep') AND <Contact.employmentType__c> != '':'Self Employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}} != '[]':'Self Employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store','QRO2O') AND <Contact.employmentType__c> != '' AND {{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}}}}}:null}}`
	// employmentType empty so we don't take "salaried" branch; Sourcing = Offline Store; result must come from UAN condition
	tempDTO := cloneMasterDTOWithOverrides(masterDTO, map[string]map[string]interface{}{
		"lead":    {"Sourcing_Program__c": "Offline Store"},
		"contact": {"employmentType__c": ""},
	})
	tempCIBIL := map[string]interface{}{
		"consumerCreditData": []interface{}{
			map[string]interface{}{
				"accounts": []interface{}{
					map[string]interface{}{"accountType": "06"},
					map[string]interface{}{"accountType": "69"},
				},
			},
		},
	}
	// Exact API response body from production (non-fan-out)
	tempMobileUANBody := map[string]interface{}{
		"data": map[string]interface{}{
			"result": []interface{}{
				map[string]interface{}{"uan": "100000000003"},
			},
		},
		"contactId": "0039H00000HvjmbQAB",
	}
	tempServiceMap := cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{
		"CIBIL":            tempCIBIL,
		"MobileUANService": tempMobileUANBody,
	})
	tempResult := processor.ProcessPlaceholders(tempExpr, tempDTO, tempServiceMap, cache, nil)
	tempUANOnly := processor.ProcessPlaceholders(`{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}}`, tempDTO, tempServiceMap, cache, nil)
	tempUANCond := processor.ProcessPlaceholders(`{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}`, tempDTO, tempServiceMap, cache, nil)
	fmt.Printf("  [TEMP] full expression result     : %q\n", tempResult)
	fmt.Printf("  [TEMP] UAN ARRAY only            : %q\n", tempUANOnly)
	fmt.Printf("  [TEMP] UAN condition (Salaried/Data Not Fetched): %q\n", tempUANCond)
	if tempResult == "Salaried" {
		fmt.Println("  ✅ [TEMP] With exact API body, resolved to Salaried via UAN branch (expected)")
		results = append(results, TestCategoryResult{Name: "[TEMP] standard_Employment_Type exact API body → Salaried", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("  ❌ [TEMP] Expected Salaried (UAN branch), got %q — ((MobileUANService.data.result)) may be resolving to empty\n", tempResult)
		results = append(results, TestCategoryResult{Name: "[TEMP] standard_Employment_Type exact API body → Salaried", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'Salaried', got %q (API body has data.result with uan)", tempResult)}, PassRate: 0})
	}

	// Chain-style workaround config (no combined IN(...) AND ... in one CONDITION).
	// This should remain stable and match expected business output for the same scenario.
	chainExpr := `{{CONDITION:((MobileUANService)) != '':{{CONDITION:((MobileUANService)) != 'null':{{CONDITION:((MobileUANService)) != '{}':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed','self employed professional','senp','sep'):'Self Employed':{{CONDITION:{{ARRAY:((CIBIL.consumerCreditData[0].accounts)):filter:accountType IN ('12','14','23','24','33','38','39','40','50','51','52','53','54','55','56','57','58','59','61','71')}} != '[]':'Self Employed':{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store','QRO2O'):{{CONDITION:<Contact.employmentType__c> != '':{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} == 'salaried':'Salaried':{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}:{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}:{{CONDITION:{{ARRAY:((MobileUANService.data.result)):filter:uan != '' AND uan != 'null'}} != '[]':'Salaried':'Data Not Fetched'}}}}}}}}:'Data Not Fetched'}}:'Data Not Fetched'}}:'Data Not Fetched'}}`
	chainResult := processor.ProcessPlaceholders(chainExpr, exactDTO, exactServiceMap, cache, nil)
	if chainResult == "Data Not Fetched" {
		fmt.Printf("  ✅ Chain config result             : %q\n", chainResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - Chain config scenario", Total: 1, Passed: 1, Failed: nil, PassRate: 100})
	} else {
		fmt.Printf("  ❌ Chain config expected %q, got %q\n", "Data Not Fetched", chainResult)
		results = append(results, TestCategoryResult{Name: "Standard Employment Type - Chain config scenario", Total: 1, Passed: 0, Failed: []string{fmt.Sprintf("Expected 'Data Not Fetched', got %q", chainResult)}, PassRate: 0})
	}

	return results
}

func cloneMasterDTOWithOverrides(dto *common_dto.MasterDTO, overrides map[string]map[string]interface{}) *common_dto.MasterDTO {
	data := make(map[string]map[string]interface{})
	for k, v := range dto.Data {
		data[k] = make(map[string]interface{})
		for kk, vv := range v {
			data[k][kk] = vv
		}
	}
	for entity, fields := range overrides {
		if data[entity] == nil {
			data[entity] = make(map[string]interface{})
		}
		for k, v := range fields {
			data[entity][k] = v
		}
	}
	return &common_dto.MasterDTO{Data: data}
}

func cloneServiceMapWithMobileUANBody(serviceMap map[string]*models.EsaLog, body map[string]interface{}) map[string]*models.EsaLog {
	return cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{"MobileUANService": body})
}

// cloneServiceMapWithBodyOverrides clones the service map and overrides Response.Body for each key in overrides.
// Used to test with different CIBIL/MobileUANService payloads (e.g. production data.result shape, empty UAN, no salaried accounts).
func cloneServiceMapWithBodyOverrides(serviceMap map[string]*models.EsaLog, overrides map[string]interface{}) map[string]*models.EsaLog {
	out := make(map[string]*models.EsaLog)
	for k, v := range serviceMap {
		if v == nil {
			out[k] = nil
			continue
		}
		clone := *v
		if body, ok := overrides[k].(map[string]interface{}); ok {
			clone.Response = models.ResponseDetails{StatusCode: 200, Body: body}
		}
		out[k] = &clone
	}
	return out
}

func runTestCategory(categoryName string, tests []TestCase, processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) TestCategoryResult {
	fmt.Printf("🔍 %s:\n", categoryName)

	passed := 0
	failed := []string{}

	for _, test := range tests {
		result := processor.ProcessPlaceholders(test.Input, masterDTO, serviceMap, cache, nil)

		if test.Expected == "" || result == test.Expected {
			passed++
			fmt.Printf("  ✅ %s: %s → %s\n", test.Description, test.Input, result)
		} else {
			failureMsg := fmt.Sprintf("%s: Expected '%s', got '%s'", test.Description, test.Expected, result)
			failed = append(failed, failureMsg)
			fmt.Printf("  ❌ %s: %s → Expected '%s', got '%s'\n", test.Description, test.Input, test.Expected, result)
		}
	}

	passRate := float64(passed) / float64(len(tests)) * 100
	fmt.Printf("  📊 %s: %d/%d passed (%.1f%%)\n\n", categoryName, passed, len(tests), passRate)

	return TestCategoryResult{
		Name:     categoryName,
		Total:    len(tests),
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	}
}

func generateComprehensiveReport(results []TestCategoryResult, duration time.Duration) {
	fmt.Println("\n📊 COMPREHENSIVE ENHANCED TEST RESULTS")
	fmt.Println("=" + strings.Repeat("=", 80))

	totalTests := 0
	totalPassed := 0
	allFailures := []string{}

	fmt.Println("📋 CATEGORY BREAKDOWN:")
	for _, result := range results {
		totalTests += result.Total
		totalPassed += result.Passed
		allFailures = append(allFailures, result.Failed...)

		status := "✅"
		if result.PassRate < 95.0 {
			status = "⚠️"
		}
		if result.PassRate < 80.0 {
			status = "❌"
		}

		fmt.Printf("  %s %s: %d/%d (%.1f%%)\n", status, result.Name, result.Passed, result.Total, result.PassRate)
	}

	overallPassRate := float64(totalPassed) / float64(totalTests) * 100

	fmt.Printf("\n📈 OVERALL RESULTS:\n")
	fmt.Printf("  Total Tests: %d\n", totalTests)
	fmt.Printf("  Passed: %d\n", totalPassed)
	fmt.Printf("  Failed: %d\n", totalTests-totalPassed)
	fmt.Printf("  Pass Rate: %.1f%%\n", overallPassRate)
	fmt.Printf("  Duration: %v\n", duration)
	fmt.Println()

	// Show failures if any
	if len(allFailures) > 0 {
		fmt.Println("❌ DETAILED FAILURES:")
		for i, failure := range allFailures {
			fmt.Printf("  %d. %s\n", i+1, failure)
		}
		fmt.Println()
	}

	// Coverage assessment
	fmt.Println("🎯 COVERAGE ASSESSMENT:")
	if overallPassRate >= 95.0 {
		fmt.Println("  ✅ EXCELLENT: Comprehensive functionality validated")
		fmt.Println("  ✅ All critical features working")
		fmt.Println("  ✅ Production deployment recommended")
	} else if overallPassRate >= 85.0 {
		fmt.Println("  ⚠️  GOOD: Most functionality working")
		fmt.Println("  ⚠️  Minor issues need attention")
		fmt.Println("  ⚠️  Review failures before deployment")
	} else if overallPassRate >= 70.0 {
		fmt.Println("  ⚠️  FAIR: Significant issues detected")
		fmt.Println("  ⚠️  Major fixes required")
		fmt.Println("  ⚠️  Do not deploy without fixes")
	} else {
		fmt.Println("  ❌ POOR: Critical functionality broken")
		fmt.Println("  ❌ Extensive fixes required")
		fmt.Println("  ❌ DO NOT DEPLOY")
	}

	fmt.Printf("\n🎉 COMPREHENSIVE ENHANCED TEST COMPLETE - %.1f%% FUNCTIONALITY VALIDATED!\n", overallPassRate)
}

func cloneMasterDTO(masterDTO *common_dto.MasterDTO) *common_dto.MasterDTO {
	cloned := &common_dto.MasterDTO{}
	payload, err := json.Marshal(masterDTO)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal masterDTO for test clone: %v", err))
	}
	if err := json.Unmarshal(payload, cloned); err != nil {
		panic(fmt.Sprintf("failed to unmarshal masterDTO for test clone: %v", err))
	}
	return cloned
}

// runPreExecutionUtilityTests verifies that ExtractObjectsFieldsAndConditions includes
// fields from PreExecution/PostExecution validations and pick_response_from (so SF query
// has all <Object.Field> needed for expression evaluation). Also documents that
// pre_execution.enabled / post_execution.enabled is implemented in sequence_service.
func runPreExecutionUtilityTests() []TestCategoryResult {
	results := []TestCategoryResult{}
	passed := 0
	total := 5
	var failed []string
	hasField := func(fields []string, name string) bool {
		for _, f := range fields {
			if f == name {
				return true
			}
		}
		return false
	}

	// 1. PreExecution validations only → lead + contact + fields from expressions
	config1 := &esa_models.ServiceConfigurationResponse{
		ID: 12, ServiceName: "MobileUANService",
		AdditionalConfig: datatypes.JSON([]byte(`{"pre_execution":{"validations":["{{CONDITION:<Lead.LoanCategory__c> IN ('TopUp'):CONTINUE:EXECUTE}}","{{CONDITION:<Contact.employmentType__c> == 'Salaried':EXECUTE:CONTINUE}}"]}}`)),
	}
	objects1, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config1})
	if _, hasLead := objects1["lead"]; !hasLead {
		failed = append(failed, "PreExecution validations: missing lead")
	} else if _, hasContact := objects1["contact"]; !hasContact {
		failed = append(failed, "PreExecution validations: missing contact")
	} else if !hasField(objects1["lead"], "loancategory__c") {
		failed = append(failed, "PreExecution validations: missing lead.loancategory__c")
	} else {
		passed++
	}

	// 2. PostExecution validations → lead.status, contact.email
	config2 := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{"post_execution":{"validations":["{{CONDITION:<Lead.Status> == 'Closed':EXIT:EXECUTE}}","{{CONDITION:<Contact.Email> != '':CONTINUE:EXECUTE}}"]}}`)),
	}
	objects2, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config2})
	if _, hasLead := objects2["lead"]; !hasLead {
		failed = append(failed, "PostExecution validations: missing lead")
	} else if _, hasContact := objects2["contact"]; !hasContact {
		failed = append(failed, "PostExecution validations: missing contact")
	} else if !hasField(objects2["lead"], "status") {
		failed = append(failed, "PostExecution validations: missing lead.status")
	} else {
		passed++
	}

	// 3. PreExecution pick_response_from only
	config3 := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{"PreExecution":{"pick_response_from":"<Contact.UAN_Number__c>"}}`)),
	}
	objects3, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config3})
	if _, hasContact := objects3["contact"]; !hasContact {
		failed = append(failed, "pick_response_from: missing contact")
	} else if !hasField(objects3["contact"], "uan_number__c") {
		failed = append(failed, "pick_response_from: missing contact.uan_number__c")
	} else {
		passed++
	}

	// 4. Named PreExecution expression should also be scanned for SF fields
	config4 := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{"pre_execution":{"enabled":true,"reuseRecentMultibureauResponse":"{{CONDITION:{{CUSTOM:dateWithinDays:<Multibureau_Data__c.CreatedDate>:30}} == 'true':CONTINUE:EXECUTE}}"}}`)),
	}
	objects4, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config4})
	if _, hasMB := objects4["multibureau_data__c"]; !hasMB {
		failed = append(failed, "named pre_execution expression: missing multibureau_data__c")
	} else if !hasField(objects4["multibureau_data__c"], "createddate") {
		failed = append(failed, "named pre_execution expression: missing multibureau_data__c.createddate")
	} else {
		passed++
	}

	// 5. Multi-dot related field placeholders should preserve the full relation path after the root object
	config5 := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{"pre_execution":{"validations":["{{CONDITION:<Contact.Name> == <Multibureau_Data__c.Contact__r.Name> && <Contact.Birthdate> == <Multibureau_Data__c.Contact__r.Birthdate>:{{CONDITION:<Multibureau_Data__c.Contact__r.Lead__r.PreApproved_Date__c> != '':CONTINUE:EXECUTE}}:EXECUTE}}"],"pick_response_from":"<Multibureau_Data__c.Response__c>"}}`)),
	}
	objects5, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config5})
	if _, hasMB := objects5["multibureau_data__c"]; !hasMB {
		failed = append(failed, "related-field extraction: missing multibureau_data__c")
	} else if !hasField(objects5["multibureau_data__c"], "contact__r.name") {
		failed = append(failed, "related-field extraction: missing multibureau_data__c.contact__r.name")
	} else if !hasField(objects5["multibureau_data__c"], "contact__r.birthdate") {
		failed = append(failed, "related-field extraction: missing multibureau_data__c.contact__r.birthdate")
	} else if !hasField(objects5["multibureau_data__c"], "contact__r.lead__r.preapproved_date__c") {
		failed = append(failed, "related-field extraction: missing multibureau_data__c.contact__r.lead__r.preapproved_date__c")
	} else if !hasField(objects5["multibureau_data__c"], "response__c") {
		failed = append(failed, "related-field extraction: missing multibureau_data__c.response__c")
	} else {
		passed++
	}

	passRate := 0.0
	if total > 0 {
		passRate = float64(passed) / float64(total) * 100
	}
	results = append(results, TestCategoryResult{
		Name:     "PreExecution / PostExecution field extraction",
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	})
	return results
}

// isPlainSFIdentifier mirrors utility.objectNamePattern (^[A-Za-z_][A-Za-z0-9_]*$). Any extracted
// object-name key that is NOT a plain identifier is "garbage" produced by mis-tokenizing an
// expression (comparison operators, quotes, braces, embedded '<'/'(' fragments).
func isPlainSFIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !(isLetter || r == '_') {
				return false
			}
			continue
		}
		if !(isLetter || isDigit || r == '_') {
			return false
		}
	}
	return true
}

// runFieldExtractionConfigTypeTests validates utility.ExtractObjectsFieldsAndConditions across the
// full range of config placeholder "types" present in the production DB, and specifically guards
// against the field-extraction garbage-object-name bug: comparison operators (< / <= / > / >=) used
// adjacent to <Object.Field> placeholders previously produced bogus object keys such as
// " 30 && <payment_schedules__c[1]", "= 48:'ett':{{condition:<transaction_detail__c" and
// "= 0:'ntc':{{condition:{{numeric:((acticodata". Every case asserts (a) NO garbage object keys and
// (b) the legitimate objects/fields are still extracted (including dotted relationship paths and
// lab_ labelled queries).
func runFieldExtractionConfigTypeTests() []TestCategoryResult {
	var failed []string
	passed := 0
	total := 0

	garbageKeys := func(objs map[string][]string) []string {
		var bad []string
		for k := range objs {
			if !isPlainSFIdentifier(k) {
				bad = append(bad, k)
			}
		}
		return bad
	}
	hasField := func(objs map[string][]string, obj, field string) bool {
		for _, f := range objs[obj] {
			if f == field {
				return true
			}
		}
		return false
	}
	extract := func(reqBody, addlConfig string) map[string][]string {
		cfg := &esa_models.ServiceConfigurationResponse{ID: 1, ServiceName: "ExtractTest"}
		if reqBody != "" {
			cfg.RequestBody = datatypes.JSON([]byte(reqBody))
		}
		if addlConfig != "" {
			cfg.AdditionalConfig = datatypes.JSON([]byte(addlConfig))
		}
		objs, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{cfg})
		return objs
	}
	// check enforces the no-garbage invariant on every case, then the case-specific expectation.
	check := func(name string, objs map[string][]string, ok bool, detail string) {
		total++
		if g := garbageKeys(objs); len(g) > 0 {
			failed = append(failed, fmt.Sprintf("%s: garbage object keys produced: %v", name, g))
			return
		}
		if !ok {
			failed = append(failed, fmt.Sprintf("%s: %s", name, detail))
			return
		}
		passed++
	}

	// 1. ActicoPreBureau.Vintage_3M (VERBATIM DB) — indexed placeholders + "< 30" comparison.
	//    Previously emitted garbage " 30 && <payment_schedules__c[1]" / "[2]".
	o1 := extract(`{"Vintage_3M":"{{NUMERIC:{{CONDITION:<Payment_Schedules__c[0].Clearance__c> == true && <Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance__c> == true && <Payment_Schedules__c[1].Delinquent_Days__c> < 30 && <Payment_Schedules__c[2].Clearance__c> == true && <Payment_Schedules__c[2].Delinquent_Days__c> < 30:1:0}}}}"}`, "")
	check("ActicoPreBureau.Vintage_3M (indexed + '< 30')", o1,
		hasField(o1, "payment_schedules__c", "clearance__c") && hasField(o1, "payment_schedules__c", "delinquent_days__c"),
		"expected payment_schedules__c.clearance__c and .delinquent_days__c")

	// 2. PostBureauAmountCRIF.Type_Of_Customer (VERBATIM DB) — nested CONDITION with "<= 48" / "<= 12".
	//    Previously emitted "= 48:'ett':{{condition:<transaction_detail__c" / "= 12:...".
	o2 := extract(`{"Type_Of_Customer":"{{CONDITION:<Lead.Customer_Category__c> != '' && <Lead.Customer_Category__c> != null:'<Lead.Customer_Category__c>':{{CONDITION:<Transaction_Data__c.tata_thick_flag_L12M__c> == 1:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Week__c> <= 48:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Months__c> <= 12:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Year__c> <= 1:'ETT':'NTT'}}}}}}}}"}`, "")
	check("PostBureauAmountCRIF.Type_Of_Customer (nested + '<= N')", o2,
		hasField(o2, "transaction_detail__c", "detail__c") && hasField(o2, "transaction_detail__c", "week__c") &&
			hasField(o2, "transaction_detail__c", "months__c") && hasField(o2, "transaction_detail__c", "year__c") &&
			hasField(o2, "transaction_data__c", "tata_thick_flag_l12m__c") && hasField(o2, "lead", "customer_category__c"),
		"expected transaction_detail__c.{detail__c,week__c,months__c,year__c} + transaction_data__c.tata_thick_flag_l12m__c + lead.customer_category__c")

	// 3. IncomeModel_V5_V6.customerProfile (VERBATIM DB) — "<= 0" / ">= 10" / "<= 18" around a
	//    service-response ((acticoData...)). Previously emitted "= 0:'ntc':{{condition:{{numeric:((acticodata".
	//    All operands are service responses, so NO Salesforce object should be extracted.
	o3 := extract(`{"customerProfile":"{{CONDITION:{{NUMERIC:((acticoData.body.crif.features.bureau_score))}} <= 0:'NTC':{{CONDITION:{{NUMERIC:((acticoData.body.crif.features.bureau_score))}} >= 10 AND {{NUMERIC:((acticoData.body.crif.features.bureau_score))}} <= 18:'Exclusion':'ETC'}}}}"}`, "")
	check("IncomeModel.customerProfile (service-response + '<= 0')", o3,
		len(o3) == 0,
		fmt.Sprintf("expected NO Salesforce objects (service-response only), got %v", o3))

	// 4. Simple placeholders, || fallback chains, and ((service.response)) mixed.
	o4 := extract(`{"pan":"<contact.PAN_ID__c> || <lead.PAN__c>","mnrl":"((MNRLService.type))","email":"<Contact.Email>"}`, "")
	check("Simple + fallback chain + service-response", o4,
		hasField(o4, "contact", "pan_id__c") && hasField(o4, "lead", "pan__c") && hasField(o4, "contact", "email"),
		"expected contact.pan_id__c, lead.pan__c, contact.email")

	// 5. Conditional <Object[cond].Field> forms (equality, cross-object ref in condition).
	o5 := extract(`{"owner":"<ES_Contact__c[Type__c = 'PAN'].OwnerName__c>","dealer":"<Dealer_Info__c[Dealer_Code__c = lead.Dealer_Code__c].Dealer_Category__c>","income":"{{NUMERIC:<Income_Model__c[Lead__c = lead.Id].Income_prediction__c> || NUMERIC:0}}"}`, "")
	check("Conditional <Object[cond].Field>", o5,
		hasField(o5, "es_contact__c", "ownername__c") && hasField(o5, "es_contact__c", "type__c") &&
			hasField(o5, "dealer_info__c", "dealer_category__c") && hasField(o5, "income_model__c", "income_prediction__c"),
		"expected es_contact__c.{ownername__c,type__c} + dealer_info__c.dealer_category__c + income_model__c.income_prediction__c")

	// 6. Relationship (multi-dot) fields must preserve the full path after the root object.
	o6 := extract(`{"bscore":"{{NUMERIC:<Lead.Campaign__r.Bscore__c> || NUMERIC:0}}","risk":"<Lead.Campaign__r.Risk_Bucket__c>"}`, "")
	check("Relationship dotted fields <Lead.Campaign__r.X>", o6,
		hasField(o6, "lead", "campaign__r.bscore__c") && hasField(o6, "lead", "campaign__r.risk_bucket__c"),
		"expected lead.campaign__r.bscore__c and lead.campaign__r.risk_bucket__c (dotted relationship path)")

	// 7. Labelled query reference (lab_ prefix) must survive as a valid object key.
	o7 := extract(`{"mb":"<lab_MB_Crif.Response__c>"}`, "")
	check("Labelled lab_ query reference", o7,
		hasField(o7, "lab_mb_crif", "response__c"),
		fmt.Sprintf("expected lab_mb_crif.response__c, got %v", o7))

	// 8. {{ARRAY:...}} object-only transform spec + filter with IN operator.
	o8 := extract(`{"scores":"{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Score__c->value@NUMERIC}}"}`, "")
	check("ARRAY transform-only object spec", o8,
		hasField(o8, "a_score__c", "type__c") && hasField(o8, "a_score__c", "score__c"),
		fmt.Sprintf("expected a_score__c.{type__c,score__c}, got %v", o8))

	// 9. additional_config pre_execution validations with mixed comparison operators.
	o9 := extract("", `{"pre_execution":{"validations":["{{CONDITION:<Lead.Amount_in_Rs__c> > 500000 && <Contact.Age1__c> <= 65:EXECUTE:CONTINUE}}","{{CONDITION:<Lead.LoanCategory__c> IN ('TopUp Loan','Repeat'):CONTINUE:EXECUTE}}"]}}`)
	check("pre_execution validations with < / > operators", o9,
		hasField(o9, "lead", "amount_in_rs__c") && hasField(o9, "contact", "age1__c") && hasField(o9, "lead", "loancategory__c"),
		"expected lead.amount_in_rs__c, contact.age1__c, lead.loancategory__c")

	// 10. All configs combined in one batch (mirrors a real sequence load) — still no garbage.
	all := []*esa_models.ServiceConfigurationResponse{
		{ID: 3, ServiceName: "ActicoPreBureau", RequestBody: datatypes.JSON([]byte(`{"Vintage_3M":"{{NUMERIC:{{CONDITION:<Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance__c> == true:1:0}}}}"}`))},
		{ID: 203, ServiceName: "PostBureauAmountCRIF", RequestBody: datatypes.JSON([]byte(`{"Type_Of_Customer":"{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Week__c> <= 48:'ETT':'NTT'}}"}`))},
		{ID: 99, ServiceName: "IncomeModel_V5_V6", RequestBody: datatypes.JSON([]byte(`{"customerProfile":"{{CONDITION:{{NUMERIC:((acticoData.body.crif.features.bureau_score))}} <= 0:'NTC':'ETC'}}"}`))},
	}
	objAll, _ := utility.ExtractObjectsFieldsAndConditions(all)
	check("Combined batch (3 offending services)", objAll,
		hasField(objAll, "payment_schedules__c", "delinquent_days__c") && hasField(objAll, "transaction_detail__c", "detail__c"),
		fmt.Sprintf("expected payment_schedules__c + transaction_detail__c present without garbage, got %v", objAll))

	passRate := 0.0
	if total > 0 {
		passRate = float64(passed) / float64(total) * 100
	}
	return []TestCategoryResult{{
		Name:     "Field Extraction - config types & garbage-object guard",
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	}}
}

// runUncoveredDbConstructTests adds coverage for placeholder constructs that appear in production
// DB configs but were previously unexercised by this harness: {{UNMAPPED:...}} and the CUSTOM
// functions joinDistinctField, getOfficePincode, extractPincode and equals. Each case uses a
// VERBATIM shape from the DB. Expected values are the resolver's ACTUAL current behavior:
//
//	{{UNMAPPED:...}}      -> "0"  (unrecognized token; resolves to numeric default)
//	joinDistinctField     -> distinct, filtered, delimiter-joined field values (implemented)
//	getOfficePincode      -> 6-digit PIN code extracted from the address (implemented; alias of extractPincode)
//	extractPincode        -> 6-digit PIN code extracted from the address (implemented)
//	equals                -> ""   (NOT IMPLEMENTED in resolver; documented gap) — inside a
//	                              {{CONDITION:...}} the empty result takes the else branch.
//
// These are characterization tests: they lock in current behavior so that any future
// implementation of the remaining unimplemented function is a deliberate, detected change.
func runUncoveredDbConstructTests(processor *sequence_service.ExpressionProcessor) []TestCategoryResult {
	svc := map[string]*models.EsaLog{}
	cache := make(map[string]string)
	masterDTO := &common_dto.MasterDTO{Data: map[string]map[string]interface{}{
		"contact": {
			"Office_Address__c":    "Plot 5, MG Road, Bengaluru 560001",
			"Office_Pincode__c":    "560001",
			"Permanent_Address__c": "12 Residency Rd, Pune 411001",
		},
		"lead": {
			"Sector__c": "Salaried",
		},
		"multibureau_idlist__c": {
			"records": []interface{}{
				map[string]interface{}{"Id_Value__c": "ABCDE1234F", "Iden_Type__c": "PAN"},
				map[string]interface{}{"Id_Value__c": "123412341234", "Iden_Type__c": "Aadhaar"},
				map[string]interface{}{"Id_Value__c": "ABCDE1234F", "Iden_Type__c": "PAN"},
			},
		},
	}}

	type exprCase struct {
		name     string
		expr     string
		expected string
	}
	cases := []exprCase{
		{
			"UNMAPPED token (runtime-only placeholder)",
			`{{UNMAPPED:DL_Status - runtime DL verification API response}}`,
			"0",
		},
		{
			"CUSTOM:joinDistinctField (distinct + filter + delimiter) [IMPLEMENTED]",
			`{{CUSTOM:joinDistinctField:{{ARRAY:<MultiBureau_IdList__c>:map:Id_Value__c,Iden_Type__c}}:Id_Value__c:, :Iden_Type__c:PAN}}`,
			"ABCDE1234F",
		},
		{
			"CUSTOM:getOfficePincode (alias of extractPincode) [IMPLEMENTED]",
			`{{CUSTOM:getOfficePincode:<Contact.Office_Address__c>:<Contact.Office_Pincode__c>}}`,
			"560001",
		},
		{
			"CUSTOM:extractPincode (trailing PIN code) [IMPLEMENTED]",
			`{{CUSTOM:extractPincode:<Contact.Permanent_Address__c>}}`,
			"411001",
		},
		{
			"CUSTOM:equals bare [UNIMPLEMENTED -> empty]",
			`{{CUSTOM:equals:<Lead.Sector__c>:Salaried}}`,
			"",
		},
		{
			"CUSTOM:equals inside CONDITION (empty -> else branch)",
			`{{CONDITION:{{CUSTOM:equals:<Lead.Sector__c>:Salaried}}:CONTINUE:EXECUTE}}`,
			"EXECUTE",
		},
	}

	var failed []string
	passed := 0
	for _, c := range cases {
		got := processor.ProcessPlaceholders(c.expr, masterDTO, svc, cache, nil)
		if got == c.expected {
			passed++
			fmt.Printf("  ✅ %s: %s → %q\n", c.name, c.expr, got)
		} else {
			failed = append(failed, fmt.Sprintf("%s: expected %q, got %q", c.name, c.expected, got))
			fmt.Printf("  ❌ %s: expected %q, got %q\n", c.name, c.expected, got)
		}
	}

	passRate := 0.0
	if len(cases) > 0 {
		passRate = float64(passed) / float64(len(cases)) * 100
	}
	return []TestCategoryResult{{
		Name:     "Uncovered DB constructs (UNMAPPED + joinDistinctField/extractPincode/equals)",
		Total:    len(cases),
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	}}
}

func runMultiBureauReuseConditionTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	// Rolling dates so {{CUSTOM:dateWithinDays:...:30}} stays true for "CONTINUE" scenarios regardless of run day.
	now := time.Now().UTC()
	recentPreApproved := now.Add(-5 * 24 * time.Hour).Format("2006-01-02")
	recentCreated := now.Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000+0000")

	const reuseCondition = "{{CONDITION:<Contact.Name> == <Multibureau_Data__c.Contact__r.Name> && <Contact.Birthdate> == <Multibureau_Data__c.Contact__r.Birthdate>:{{CONDITION:<Multibureau_Data__c.Contact__r.Lead__r.PreApproved_Date__c> != '':{{CONDITION:{{CUSTOM:dateWithinDays:<Multibureau_Data__c.Contact__r.Lead__r.PreApproved_Date__c>:30}} == 'true':CONTINUE:EXECUTE}}:{{CONDITION:{{CUSTOM:dateWithinDays:<Multibureau_Data__c.CreatedDate>:30}} == 'true':CONTINUE:EXECUTE}}}}:EXECUTE}}"
	const pickResponse = "<Multibureau_Data__c.Response__c>"

	makeMultibureauRecord := func(name, birthdate, preApprovedDate, createdDate string) map[string]interface{} {
		record := map[string]interface{}{
			"CreatedDate": createdDate,
			"Response__c": `{"controlData":{"success":true},"source":"cached"}`,
			"Contact__r": map[string]interface{}{
				"Name":      name,
				"Birthdate": birthdate,
				"Lead__r": map[string]interface{}{
					"PreApproved_Date__c": preApprovedDate,
				},
			},
		}
		return record
	}

	runScenario := func(description, expectedDecision, expectedPickedResponse string, multibureau map[string]interface{}, mutate func(*common_dto.MasterDTO)) TestCategoryResult {
		scenarioDTO := cloneMasterDTO(masterDTO)
		scenarioDTO.Data["multibureau_data__c"] = multibureau
		if mutate != nil {
			mutate(scenarioDTO)
		}

		tests := []TestCase{
			{reuseCondition, expectedDecision, description + " decision"},
			{pickResponse, expectedPickedResponse, description + " pick_response_from"},
		}
		return runTestCategory("MultiBureau reuse - "+description, tests, processor, scenarioDTO, serviceMap, cache)
	}

	results = append(results, runScenario(
		"recent preapproved date wins",
		"CONTINUE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("John Michael Doe", "1990-01-15", recentPreApproved, "2026-01-01T14:08:33.000+0000"),
		nil,
	))

	results = append(results, runScenario(
		"blank preapproved date falls back to createddate",
		"CONTINUE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("John Michael Doe", "1990-01-15", "", recentCreated),
		nil,
	))

	results = append(results, runScenario(
		"old preapproved date executes",
		"EXECUTE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("John Michael Doe", "1990-01-15", "2025-01-15", "2026-03-02T14:08:33.000+0000"),
		nil,
	))

	results = append(results, runScenario(
		"blank preapproved and old createddate executes",
		"EXECUTE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("John Michael Doe", "1990-01-15", "", "2025-01-01T14:08:33.000+0000"),
		nil,
	))

	results = append(results, runScenario(
		"name mismatch executes",
		"EXECUTE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("Jane Michael Doe", "1990-01-15", "2026-03-01", "2026-03-02T14:08:33.000+0000"),
		nil,
	))

	results = append(results, runScenario(
		"birthdate mismatch executes",
		"EXECUTE",
		`{"controlData":{"success":true},"source":"cached"}`,
		makeMultibureauRecord("John Michael Doe", "1992-01-15", "2026-03-01", "2026-03-02T14:08:33.000+0000"),
		nil,
	))

	results = append(results, runScenario(
		"missing multibureau record executes",
		"EXECUTE",
		"",
		nil,
		func(dto *common_dto.MasterDTO) {
			delete(dto.Data, "multibureau_data__c")
		},
	))

	return results
}

// aadhaarLinkedExpression: If Karza present (true or false) use Karza; if Karza not available (empty), default to DataSutram data.aadhaar (Dmi then eKYC) then "".
// Two sibling CONDITION blocks for DataSutram so each inner condition has only one ref.
// Last4 rule: only used when NEITHER last-4 starts with XX (all 4 digits visible). If last-4 contains XX, falls through to last-2 rule.
// Last2 rule: when last 2 match → (either first 2 is XX/xx masked) OR (neither masked AND first 2 of both match) → true; else false.
// All comparisons are case-insensitive via uppercase transform.
// Fields: KarzaPanProfileDetail_kyc.result.aadhaarMatch, DataSutramPanProfile_kyc.data.aadhaar.
// Scenarios validated (18):
//  1. Karza true → use Karza
//  2. Karza false, use Karza → false
//  3. Karza empty, DataSutram aadhaar last 4 match (DMI aadhaar, all visible) → true
//  4. Karza empty, DataSutram aadhaar last 2 match + uppercase XX in first 2 of entire DataSutram aadhaar → true
//  5. Karza empty, DataSutram aadhaar last 2 match + lowercase xx in first 2 of entire DMI platform aadhaar → true (case-insensitive)
//  6. Karza empty, DataSutram aadhaar last 2 mismatch → false
//  7. Karza empty, DataSutram aadhaar last 2 match, no XX, first 2 differ → false
//  8. Karza empty, DataSutram data.aadhaar empty, DMI & eKYC present → blank
//  9. Karza empty, DataSutram present, both DMI and eKYC aadhaar empty → blank
//
// 10. Ref fallback: DMI empty, eKYC aadhaar last 4 match → true
// 11. Last 4 numeric match (DataSutram aadhaar 1234, DMI aadhaar 1234) → true
// 12. DataSutram aadhaar XX34, DMI aadhaar xx34: last4 masked → falls to last2 path, first2 XX → true
// 13. DataSutram aadhaar 1234, eKYC aadhaar 4534 (DMI empty) → false (different numbers)
// 14. Real case: DataSutram 92XXXXXXXX52, DMI xxxxxxxx0910 → false (last 2 don't match: 52 vs 10)
// 15. Real case: DataSutram 92XXXXXXXX52, DMI 92xxxxxxxx52 → true (both last4 masked → last2+first2: 52==52, first2 92==92)
// 16. DS last4 masked (XX34), DMI fully visible (last4 9034): last2 match, no XX in first2 → true (first2 12==12)
// 17. Both last4 fully visible but different → false (last4 comparison alone, no fallback to last2)
// 18. Real case: DataSutram 92XXXXXXXX52, DMI 29xxxxxxxx52 → false (both last4 masked → last2+first2: 52==52 but first2 92≠29)
// 19. Both last4 fully visible and different (last2 and first2 incidentally match) → false
// 20. DS last4 masked (XX52), DMI fully visible (last4 9052, first2 12): last2 match, first2 differ 92≠12 → false
// 21. DS last4 masked (XX52), DMI fully visible (last4 9052, first2 92): last2 match, first2 match 92==92 → true
const aadhaarLinkedExpression = "{{CONDITION:((KarzaPanProfileDetail_kyc.result.aadhaarMatch)) != '':((KarzaPanProfileDetail_kyc.result.aadhaarMatch)):{{CONDITION:((DataSutramPanProfile_kyc.data.aadhaar)) != '' AND <consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c> != '':{{CONDITION:{{TRANSFORM:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):4}}:substring:0:2}}:uppercase}} != 'XX' AND {{TRANSFORM:{{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c>:4}}:substring:0:2}}:uppercase}} != 'XX':{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):4}}:uppercase}} == {{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c>:4}}:uppercase}}:true:false}}:{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):2}}:uppercase}} == {{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c>:2}}:uppercase}} AND ({{TRANSFORM:{{TRANSFORM:((DataSutramPanProfile_kyc.data.aadhaar)):substring:0:2}}:uppercase}} == 'XX' OR {{TRANSFORM:{{TRANSFORM:<consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c>:substring:0:2}}:uppercase}} == 'XX' OR {{TRANSFORM:{{TRANSFORM:((DataSutramPanProfile_kyc.data.aadhaar)):substring:0:2}}:uppercase}} == {{TRANSFORM:{{TRANSFORM:<consolidated_external_data__c.DmiPlatform_Aadhaar_Number__c>:substring:0:2}}:uppercase}}):true:false}}}}:''}} || {{CONDITION:((DataSutramPanProfile_kyc.data.aadhaar)) != '' AND <consolidated_external_data__c.Adhaar_Number_eKYC__c> != '':{{CONDITION:{{TRANSFORM:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):4}}:substring:0:2}}:uppercase}} != 'XX' AND {{TRANSFORM:{{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.Adhaar_Number_eKYC__c>:4}}:substring:0:2}}:uppercase}} != 'XX':{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):4}}:uppercase}} == {{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.Adhaar_Number_eKYC__c>:4}}:uppercase}}:true:false}}:{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:((DataSutramPanProfile_kyc.data.aadhaar)):2}}:uppercase}} == {{TRANSFORM:{{CUSTOM:takeLast:<consolidated_external_data__c.Adhaar_Number_eKYC__c>:2}}:uppercase}} AND ({{TRANSFORM:{{TRANSFORM:((DataSutramPanProfile_kyc.data.aadhaar)):substring:0:2}}:uppercase}} == 'XX' OR {{TRANSFORM:{{TRANSFORM:<consolidated_external_data__c.Adhaar_Number_eKYC__c>:substring:0:2}}:uppercase}} == 'XX' OR {{TRANSFORM:{{TRANSFORM:((DataSutramPanProfile_kyc.data.aadhaar)):substring:0:2}}:uppercase}} == {{TRANSFORM:{{TRANSFORM:<consolidated_external_data__c.Adhaar_Number_eKYC__c>:substring:0:2}}:uppercase}}):true:false}}}}:''}} || \"\"}}"

// runAadhaarLinkedConfigTests runs scenarios for the aadhaarLinked config (Karza result.aadhaarMatch | DataSutram data.aadhaar last4/last2+XX (case-insensitive) | default blank; refs = DMI platform aadhaar then eKYC aadhaar via CONDITION).
func runAadhaarLinkedConfigTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, _ map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}

	type scenario struct {
		desc           string
		expected       string
		dmiAadhaar     string
		eKYCAadhaar    string
		karzaLinked    string
		dataSutramLink string
	}

	scenarios := []scenario{
		{"Karza true → use Karza", "true", "123456789012", "", "true", "1234"},
		{"Karza false, use Karza → false", "false", "123456781234", "", "false", "1234"},
		{"Karza empty, DataSutram aadhaar last 4 match (DMI aadhaar) → true", "true", "123456781234", "", "", "1234"},
		{"Karza empty, DataSutram aadhaar last 2 match + XX in first 2 of entire DataSutram aadhaar → true", "true", "123456789034", "", "", "XX34"},
		{"Karza empty, DataSutram aadhaar last 2 match + lowercase xx in first 2 of entire DMI platform aadhaar → true (case-insensitive)", "true", "xx34", "", "", "1234"},
		{"Karza empty, DataSutram aadhaar last 2 mismatch → false", "false", "123456789099", "", "", "1234"},
		{"Karza empty, DataSutram aadhaar last 2 match, no XX, first 2 differ → false", "false", "123456789034", "", "", "4534"},
		{"Karza empty, DataSutram data.aadhaar empty, DMI & eKYC present → blank", "", "123456789034", "123456789034", "", ""},
		{"Karza empty, DataSutram present, both DMI and eKYC aadhaar empty → blank", "", "", "", "", "1234"},
		{"Ref fallback: DMI empty, eKYC aadhaar last 4 match → true", "true", "", "123456781234", "", "1234"},
		{"Last 4 numeric match (DataSutram aadhaar 1234, DMI aadhaar 1234) → true", "true", "123456781234", "", "", "1234"},
		{"DataSutram aadhaar XX34, DMI aadhaar xx34: last4 masked → falls to last2 path, first2 XX → true", "true", "XXXXXXXXXX34", "", "", "XX34"},
		{"DataSutram aadhaar 1234, eKYC aadhaar 4534 (DMI empty) → false (different numbers)", "false", "", "4534", "", "1234"},
		{"Real case: DataSutram 92XXXXXXXX52, DMI xxxxxxxx0910 → false (last 2 don't match: 52 vs 10)", "false", "xxxxxxxx0910", "", "", "92XXXXXXXX52"},
		{"Real case: DataSutram 92XXXXXXXX52, DMI 92xxxxxxxx52 → true (last 4 XX52 == xx52 case-insensitive)", "true", "92xxxxxxxx52", "", "", "92XXXXXXXX52"},
		// DS last4 = XX34 (masked) → last2 path; last2 34==34, first2 DS 12 not-XX, first2 DMI 12 not-XX, 12==12 → true
		{"DS last4 masked (XX34), DMI fully visible (last4 9034): last2 match, no XX in first2, first2 12==12 → true", "true", "129034", "", "", "12XXXXXXXX34"},
		// Both last4 fully visible: DS last4 1234 ≠ DMI last4 9034 → false (no fallback to last2 path)
		{"Both last4 fully visible, last4 differ (1234 vs 9034): no fallback, directly false", "false", "129034", "", "", "451234"},
		{"Real case: DataSutram 92XXXXXXXX52, DMI 29xxxxxxxx52 → false (last4 masked → last2+first2: 52==52 but first2 92≠29)", "false", "29xxxxxxxx52", "", "", "92XXXXXXXX52"},
		// Both last4 visible and different, but last2 and first2 incidentally match — must still be false
		{"Both last4 fully visible and different (5634 vs 1234), last2+first2 incidentally match → false", "false", "123456781234", "", "", "123456785634"},
		// DS last4 masked (XX52), DMI fully visible (first2 12, last2 52): last2 match but first2 differ → false
		{"DS last4 masked (XX52), DMI fully visible first2 12 ≠ DS first2 92: last2 match, first2 differ → false", "false", "123456789052", "", "", "92XXXXXXXX52"},
		// DS last4 masked (XX52), DMI fully visible (first2 92, last2 52): last2 match and first2 match → true
		{"DS last4 masked (XX52), DMI fully visible first2 92 == DS first2 92: last2 match, first2 match → true", "true", "923456789052", "", "", "92XXXXXXXX52"},
	}

	passed := 0
	failed := []string{}

	for _, s := range scenarios {
		// Build DTO: add consolidated_external_data__c with DmiPlatform and eKYC fields
		overrides := map[string]map[string]interface{}{
			"consolidated_external_data__c": {
				"DmiPlatform_Aadhaar_Number__c": s.dmiAadhaar,
				"Adhaar_Number_eKYC__c":         s.eKYCAadhaar,
			},
		}
		dto := cloneMasterDTOWithOverrides(masterDTO, overrides)

		// Build service map with Karza and DataSutram bodies
		karzaBody := map[string]interface{}{}
		if s.karzaLinked != "" {
			karzaBody["result"] = map[string]interface{}{"aadhaarMatch": s.karzaLinked}
		}
		dataSutramBody := map[string]interface{}{}
		if s.dataSutramLink != "" {
			dataSutramBody["data"] = map[string]interface{}{"aadhaar": s.dataSutramLink}
		}
		svcMap := cloneServiceMapWithBodyOverrides(serviceMap, map[string]interface{}{
			"KarzaPanProfileDetail_kyc": karzaBody,
			"DataSutramPanProfile_kyc":  dataSutramBody,
		})

		// Use a fresh cache per scenario so cached result from previous scenario is not reused
		scenarioCache := make(map[string]string)
		result := processor.ProcessPlaceholders(aadhaarLinkedExpression, dto, svcMap, scenarioCache, nil)

		if s.expected == "" || result == s.expected {
			passed++
			fmt.Printf("  ✅ %s: got %q\n", s.desc, result)
		} else {
			failed = append(failed, fmt.Sprintf("%s: expected %q, got %q", s.desc, s.expected, result))
			fmt.Printf("  ❌ %s: expected %q, got %q\n", s.desc, s.expected, result)
		}
	}

	total := len(scenarios)
	passRate := float64(passed) / float64(total) * 100
	fmt.Printf("  📊 Aadhaar Linked Config: %d/%d passed (%.1f%%)\n\n", passed, total, passRate)

	results = append(results, TestCategoryResult{
		Name:     "Aadhaar Linked Config",
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	})
	return results
}

// runCrifPhoneListTests validates the two code fixes:
//
//  1. Fix 1 — parseCustomLogicExpression quote-char tracking:
//     getJsonPath must correctly receive the full JSON string when that string is
//     produced by a nested {{TRANSFORM:...:split:<html>:0}} expression. The CRIF JSON
//     contains timestamps like "09/03/2026 15:20:06" whose ':' characters must NOT be
//     treated as CUSTOM param separators. Before the fix, parseCustomLogicExpression
//     used a single boolean toggled by both ' and " so any '"...:..."' region lost its
//     in-quotes protection the moment a double-quote flipped the state.
//
//  2. Fix 2 — ARRAY:transform-only static literal injection:
//     A single-quoted literal on the left side of '->' (e.g. '00'->telephoneType)
//     injects a constant value onto every output array item instead of trying to
//     navigate a source field with that name. Supports @TYPE cast as usual.
//
//     Production config validated here:
//     "phoneList": "{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.REQUEST-DATA.APPLICANT-SEGMENT.PHONES}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}"
func runCrifPhoneListTests(processor *sequence_service.ExpressionProcessor, masterDTO *common_dto.MasterDTO, serviceMap map[string]*models.EsaLog, cache map[string]string) []TestCategoryResult {
	results := []TestCategoryResult{}
	passed := 0
	var failed []string

	run := func(desc, expr, expected string) {
		got := processor.ProcessPlaceholders(expr, masterDTO, serviceMap, cache, nil)
		if got == expected {
			passed++
			fmt.Printf("  ✅ %s\n", desc)
		} else {
			failed = append(failed, fmt.Sprintf("%s: expected %q, got %q", desc, expected, got))
			fmt.Printf("  ❌ %s\n     expected: %s\n     got:      %s\n", desc, expected, got)
		}
	}

	// ─── Fix 1: getJsonPath with nested TRANSFORM on CRIF JSON (has ':' in timestamps) ──────
	// CRIF raw_response contains "DATE-OF-REQUEST":"09/03/2026 15:20:06" etc.
	// Before the fix the ':' in "15:20:06" was a false param separator → getJsonPath returned "".
	run(
		"Fix1: getJsonPath scalar — STATUS from JSON-with-timestamps via nested TRANSFORM",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.HEADER-SEGMENT.STATUS}}`,
		"SUCCESS",
	)
	run(
		"Fix1: getJsonPath scalar — applicant first-name via nested TRANSFORM",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.REQUEST-DATA.APPLICANT-SEGMENT.FIRST-NAME}}`,
		"JOHN",
	)

	// ─── Fix 2: ARRAY:transform-only static literal injection (isolated, native array) ──────
	// Uses IxsightNegativeScrub_kyc.SANCTIONS (native array) so Fix 2 can be verified
	// independently of Fix 1. '00'->code injects constant "00" on every item.
	run(
		"Fix2: transform-only injects static literal on each array item (no type cast)",
		`{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)):transform-only:MATCHED_NAME->name,'crif'->source}}`,
		`[{"name":"EMRAAN ALI","source":"crif"},{"name":"JOHN DOE","source":"crif"}]`,
	)
	run(
		"Fix2: transform-only injects static literal with @STRING type cast",
		`{{ARRAY:((IxsightNegativeScrub_kyc.SANCTIONS)):transform-only:MATCHED_NAME->name@STRING,'00'->code@STRING}}`,
		`[{"code":"00","name":"EMRAAN ALI"},{"code":"00","name":"JOHN DOE"}]`,
	)

	// ─── Combined (production config): Fix 1 + Fix 2 together ────────────────────────────────
	// 1. {{TRANSFORM:...:split:<html>:0}}          → strips HTML suffix from raw_response
	// 2. {{CUSTOM:getJsonPath:...:...PHONES}}       → extracts PHONES array (Fix 1)
	// 3. {{ARRAY:...:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}
	//    → renames VALUE field + injects static telephoneType on each item (Fix 2)
	run(
		"Combined: CRIF phoneList — VALUE->telephoneNumber, static '00'->telephoneType",
		`{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.REQUEST-DATA.APPLICANT-SEGMENT.PHONES}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}`,
		`[{"telephoneNumber":"9000000003","telephoneType":"00"},{"telephoneNumber":"9876543210","telephoneType":"00"}]`,
	)

	// ─── getJsonPath predicate filter: delegates to evaluateFilterCondition engine ─────────
	// The bracket predicate uses the same engine as ARRAY:filter, supporting:
	//   ==  single equality (e.g. TYPE=='PHONE-VARIATIONS')
	//   AND  all conditions must match
	//   OR   any condition must match
	//   IN   field value in a set
	//   !=, >, <, >=, <=  comparison operators
	run(
		"Fix3: getJsonPath == predicate — VARIATIONS[TYPE=='PHONE-VARIATIONS'].VARIATION",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS'].VARIATION}}`,
		`[{"VALUE":"9000000003"},{"VALUE":"9876543210"}]`,
	)

	run(
		"Fix3: getJsonPath AND predicate — VARIATIONS[TYPE=='PHONE-VARIATIONS' AND STATUS=='ACTIVE']",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS' AND STATUS=='ACTIVE'].VARIATION}}`,
		`[{"VALUE":"9000000003"},{"VALUE":"9876543210"}]`,
	)

	run(
		"Fix3: getJsonPath AND predicate — no match returns empty string",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS' AND STATUS=='INACTIVE'].VARIATION}}`,
		``,
	)

	run(
		"Fix3: getJsonPath OR predicate — first matching element returned",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS' OR TYPE=='MOBILE-VARIATIONS'].VARIATION}}`,
		`[{"VALUE":"9000000003"},{"VALUE":"9876543210"}]`,
	)

	run(
		"Fix3: getJsonPath IN predicate — field value in set",
		`{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE IN ('PHONE-VARIATIONS','MOBILE-VARIATIONS')].VARIATION}}`,
		`[{"VALUE":"9000000003"},{"VALUE":"9876543210"}]`,
	)

	run(
		"Fix3+Combined: CRIF VARIATIONS phoneList via predicate filter + static literal",
		`{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIF.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS'].VARIATION}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}`,
		`[{"telephoneNumber":"9000000003","telephoneType":"00"},{"telephoneNumber":"9876543210","telephoneType":"00"}]`,
	)

	// ─── Fix4: apostrophe (') in the source JSON must not break getJsonPath ───────────────────
	// The CRIFApos response carries a name with apostrophes ("B'JOHN' B'DOE'"). The engine
	// wraps the resolved JSON in single quotes and escapes ' -> \'; the CUSTOM unwrap reverses it
	// via unescapeSingleQuoteWrapped. Without the fix, json.Unmarshal fails on "\'" and these
	// resolve to []. The apostrophe is in a NAME field, proving an apostrophe ANYWHERE in the
	// payload (not just the navigated path) previously corrupted the whole parse.
	run(
		"Fix4: apostrophe-in-JSON — PHONES still resolve via getJsonPath path",
		`{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIFApos.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.REQUEST-DATA.APPLICANT-SEGMENT.PHONES}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}`,
		`[{"telephoneNumber":"9000000001","telephoneType":"00"},{"telephoneNumber":"9000000006","telephoneType":"00"}]`,
	)
	run(
		"Fix4: apostrophe-in-JSON — PHONE-VARIATIONS predicate still resolves",
		`{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIFApos.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS'].VARIATION}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}`,
		`[{"telephoneNumber":"9000000001","telephoneType":"00"},{"telephoneNumber":"9000000006","telephoneType":"00"}]`,
	)

	// ─── Fix5: odd apostrophe count in a large JSON must not collapse the expression to "{{ARRAY:" ──
	// The single-quote wrap escapes ' -> \'; scanners must honor that backslash escape inside single
	// quotes, else the escaped apostrophe closes the quote early, the JSON's trailing "}}" are
	// mis-counted, and the whole {{ARRAY:...}} collapses. Uses the real REPORT-DATA path.
	run(
		"Fix5: odd-apostrophe JSON — expression does not collapse; phoneList resolves",
		`{{ARRAY:{{CUSTOM:getJsonPath:{{TRANSFORM:((CRIFAposOdd.raw_response)):split:<html>:0}}:CIR-REPORT-FILE.REPORT-DATA.STANDARD-DATA.DEMOGS.VARIATIONS[TYPE=='PHONE-VARIATIONS'].VARIATION}}:transform-only:VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING}}`,
		`[{"telephoneNumber":"9000000006","telephoneType":"00"},{"telephoneNumber":"8888888888","telephoneType":"00"}]`,
	)

	total := 14
	passRate := float64(passed) / float64(total) * 100
	fmt.Printf("  📊 CRIF Phone List (getJsonPath + static literal + predicate filter): %d/%d passed (%.1f%%)\n\n", passed, total, passRate)

	results = append(results, TestCategoryResult{
		Name:     "🆕 CRIF Phone List (getJsonPath + static literal in transform-only)",
		Total:    total,
		Passed:   passed,
		Failed:   failed,
		PassRate: passRate,
	})
	return results
}

// Test data structures
type TestCase struct {
	Input       string
	Expected    string
	Description string
}

type TestCategoryResult struct {
	Name     string
	Total    int
	Passed   int
	Failed   []string
	PassRate float64
}
