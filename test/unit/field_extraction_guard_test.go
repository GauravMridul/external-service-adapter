package unit

import (
	"regexp"
	"testing"

	"esa/internal/app/models/esa_models"
	"esa/internal/app/utility"

	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

// plainSFIdentifier mirrors utility.objectNamePattern. Any extracted object-name key that is not a
// plain identifier is garbage produced by mis-tokenizing an expression.
var plainSFIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func assertNoGarbageObjectKeys(t *testing.T, objects map[string][]string) {
	t.Helper()
	for k := range objects {
		assert.Truef(t, plainSFIdentifier.MatchString(k),
			"garbage object-name key extracted: %q (keys must be plain Salesforce identifiers)", k)
	}
}

// TestExtraction_ActicoPreBureau_Vintage3M reproduces the exact production expression that produced
// the garbage keys " 30 && <payment_schedules__c[1]" and "[2]" because "< 30" opened a spurious
// placeholder span. After the fix the comparison operator cannot open a span, so only the real
// payment_schedules__c fields are extracted and no garbage key survives.
func TestExtraction_ActicoPreBureau_Vintage3M(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 3, ServiceName: "ActicoPreBureau",
		RequestBody: datatypes.JSON([]byte(`{"Vintage_3M":"{{NUMERIC:{{CONDITION:<Payment_Schedules__c[0].Clearance__c> == true && <Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance__c> == true && <Payment_Schedules__c[1].Delinquent_Days__c> < 30 && <Payment_Schedules__c[2].Clearance__c> == true && <Payment_Schedules__c[2].Delinquent_Days__c> < 30:1:0}}}}"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	assert.Contains(t, objects, "payment_schedules__c")
	assert.Contains(t, objects["payment_schedules__c"], "clearance__c")
	assert.Contains(t, objects["payment_schedules__c"], "delinquent_days__c")
	// The specific historical garbage keys must be absent.
	assert.NotContains(t, objects, " 30 && <payment_schedules__c[1]")
	assert.NotContains(t, objects, " 30 && <payment_schedules__c[2]")
}

// TestExtraction_PostBureauAmountCRIF_TypeOfCustomer reproduces the nested CONDITION with "<= 48" /
// "<= 12" that produced "= 48:'ett':{{condition:<transaction_detail__c" and "= 12:...".
func TestExtraction_PostBureauAmountCRIF_TypeOfCustomer(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 203, ServiceName: "PostBureauAmountCRIF",
		RequestBody: datatypes.JSON([]byte(`{"Type_Of_Customer":"{{CONDITION:<Lead.Customer_Category__c> != '' && <Lead.Customer_Category__c> != null:'<Lead.Customer_Category__c>':{{CONDITION:<Transaction_Data__c.tata_thick_flag_L12M__c> == 1:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Week__c> <= 48:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Months__c> <= 12:'ETT':{{CONDITION:<Transaction_Detail__c.Detail__c> > 0 && <Transaction_Detail__c.Year__c> <= 1:'ETT':'NTT'}}}}}}}}"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	assert.Contains(t, objects, "transaction_detail__c")
	for _, f := range []string{"detail__c", "week__c", "months__c", "year__c"} {
		assert.Containsf(t, objects["transaction_detail__c"], f, "transaction_detail__c.%s", f)
	}
	assert.Contains(t, objects["transaction_data__c"], "tata_thick_flag_l12m__c")
	assert.Contains(t, objects["lead"], "customer_category__c")
}

// TestExtraction_IncomeModel_CustomerProfile reproduces "<= 0:'NTC':...>= 10...<= 18" wrapped around
// a service-response ((acticoData...)). Previously produced "= 0:'ntc':{{condition:{{numeric:((acticodata".
// All operands are service responses, so NO Salesforce object should be extracted.
func TestExtraction_IncomeModel_CustomerProfile(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 99, ServiceName: "IncomeModel_V5_V6",
		RequestBody: datatypes.JSON([]byte(`{"customerProfile":"{{CONDITION:{{NUMERIC:((acticoData.body.crif.features.bureau_score))}} <= 0:'NTC':{{CONDITION:{{NUMERIC:((acticoData.body.crif.features.bureau_score))}} >= 10 AND {{NUMERIC:((acticoData.body.crif.features.bureau_score))}} <= 18:'Exclusion':'ETC'}}}}"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	assert.Empty(t, objects, "service-response-only expression should extract no Salesforce objects")
}

// TestExtraction_ComparisonOperatorAdjacency is a focused regression: a comparison operator directly
// before a genuine placeholder must not consume that placeholder. Both fields must be extracted.
func TestExtraction_ComparisonOperatorAdjacency(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{"x":"{{CONDITION:<Payment_Schedules__c[0].Delinquent_Days__c> < 30 && <Payment_Schedules__c[1].Clearance__c> == true:1:0}}"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	assert.Contains(t, objects["payment_schedules__c"], "delinquent_days__c")
	assert.Contains(t, objects["payment_schedules__c"], "clearance__c")
}

// TestExtraction_LegitConfigTypesUnaffected guards that the fix did not regress ordinary extraction:
// simple, fallback chain, conditional, relationship-dotted, and labelled references still resolve.
func TestExtraction_LegitConfigTypesUnaffected(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{"pan":"<contact.PAN_ID__c> || <lead.PAN__c>","owner":"<ES_Contact__c[Type__c = 'PAN'].OwnerName__c>","bscore":"{{NUMERIC:<Lead.Campaign__r.Bscore__c> || NUMERIC:0}}","mb":"<lab_MB_Crif.Response__c>","svc":"((MNRLService.type))"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	assert.Contains(t, objects["contact"], "pan_id__c")
	assert.Contains(t, objects["lead"], "pan__c")
	assert.Contains(t, objects["es_contact__c"], "ownername__c")
	assert.Contains(t, objects["es_contact__c"], "type__c")
	assert.Contains(t, objects["lead"], "campaign__r.bscore__c")
	assert.Contains(t, objects["lab_mb_crif"], "response__c")
}

// TestExtraction_ExcludesRownumIndexTokens guards that the synthetic ARRAY transform position
// tokens __rownum__ / __index__ are NOT extracted as Salesforce fields. If they were, they'd be
// added to the SOQL field set and break the query with "No such column '__rownum__'".
func TestExtraction_ExcludesRownumIndexTokens(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{"cat":"{{ARRAY:<Product__c>:transform-only:__rownum__->key,Name__c->value}}","cat2":"{{ARRAY:<Product__c>:transform-only:__index__->pos,Family__c->fam}}"}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)
	// Real fields are still extracted.
	assert.Contains(t, objects["product__c"], "name__c")
	assert.Contains(t, objects["product__c"], "family__c")
	// The synthetic position tokens must never appear as fields.
	assert.NotContains(t, objects["product__c"], "__rownum__")
	assert.NotContains(t, objects["product__c"], "__index__")
}

// TestExtraction_ExcludesArrayDefaultLiterals guards that the v2.4 `??` per-element default is
// treated as a literal and never enters the SOQL SELECT list. A naive parser would put -999 /
// UNKNOWN / N/A into the field set and the query would fail with "No such column".
func TestExtraction_ExcludesArrayDefaultLiterals(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{
			"App_Deciles": "{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Decile__c->value@NUMERIC??-999}}",
			"App_Scores": "{{ARRAY:<A_Score__c>:transform-only:Type__c->key,Underwriting_score__c->value??-999}}",
			"Statuses": "{{ARRAY:<A_Score__c>:transform-only:Status__c->status??'N/A, unknown'}}",
			"Constitution": "{{ARRAY:<A_Score__c>:transform-only:Risk_Bucket__c->bucket@STRING??UNKNOWN}}",
			"Nulls": "{{ARRAY:<A_Score__c>:transform-only:Segment__c->segment??null}}"
		}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)

	fields := objects["a_score__c"]
	// The real source fields on the LEFT of -> are still extracted.
	for _, want := range []string{"type__c", "decile__c", "underwriting_score__c", "status__c", "risk_bucket__c", "segment__c", "id"} {
		assert.Containsf(t, fields, want, "expected source field %q in the SOQL field set, got %v", want, fields)
	}
	// No part of a ?? default may become a field.
	for _, unwanted := range []string{"-999", "999", "n/a", "unknown", "null", "value", "key", "status", "bucket", "segment"} {
		assert.NotContainsf(t, fields, unwanted, "default literal / target key %q must not be queried, got %v", unwanted, fields)
	}
}

// TestExtraction_QuotedDoubleQuestionMarkLiterals guards that a "??" used as DATA inside a quoted
// static literal cannot inject a bogus column into the SOQL SELECT list, and that the real source
// fields sharing the same transform spec are still extracted.
func TestExtraction_QuotedDoubleQuestionMarkLiterals(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{
			"a": "{{ARRAY:<A_Score__c>:transform-only:'??'->tag,Decile__c->value@NUMERIC??-999}}",
			"b": "{{ARRAY:<A_Score__c>:transform-only:'N/A??'->status,Type__c->key}}",
			"c": "{{ARRAY:<A_Score__c>:transform-only:Note__c->note??'??'}}"
		}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assertNoGarbageObjectKeys(t, objects)

	fields := objects["a_score__c"]
	for _, want := range []string{"decile__c", "type__c", "note__c", "id"} {
		assert.Containsf(t, fields, want, "expected source field %q to still be extracted, got %v", want, fields)
	}
	for _, unwanted := range []string{"??", "'??'", "n/a??", "'n/a??'", "tag", "status", "note", "-999"} {
		assert.NotContainsf(t, fields, unwanted, "literal %q must not be queried, got %v", unwanted, fields)
	}
}

// TestExtraction_QuoteAwareTransformSpecSplitting guards the quote-aware `:` and `,` splitting in
// extractFieldsFromArrayTransformSpec. Before it, the splits were naive and produced two distinct
// failures, both of which had been live since v2.3 for quoted static literals:
//
//   - a quoted value containing a comma AND an arrow was cut in half, and the trailing fragment
//     looked like a mapping pair, injecting a non-existent column that failed the whole SOQL query
//   - a quoted value containing a colon truncated the spec, so every mapping after it was never
//     scanned and its fields silently never reached the SELECT list (emitting null at runtime)
func TestExtraction_QuoteAwareTransformSpecSplitting(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID: 1, ServiceName: "T",
		RequestBody: datatypes.JSON([]byte(`{
			"leakDefault":   "{{ARRAY:<A_Score__c>:transform-only:Status__c->status??'A,Bogus__c->C'}}",
			"leakStatic":    "{{ARRAY:<B_Score__c>:transform-only:'A,Bogus__c->C'->t,Type__c->key}}",
			"dropDefault":   "{{ARRAY:<C_Score__c>:transform-only:Status__c->status??'N/A: none',Type__c->key,Decile__c->value}}",
			"dropStatic":    "{{ARRAY:<D_Score__c>:transform-only:'00:00'->t,Type__c->key,Decile__c->value}}",
			"atInLiteral":   "{{ARRAY:<E_Score__c>:transform-only:'a@b'->t,Type__c->key}}",
			"mapWithLiteral":"{{ARRAY:<F_Score__c>:map:Type__c,'lit'}}"
		}`)),
	}

	objects, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})
	assertNoGarbageObjectKeys(t, objects)

	// No quoted literal may ever contribute a column.
	for object, fields := range objects {
		assert.NotContainsf(t, fields, "bogus__c",
			"a quoted literal must not inject a column into %s, got %v", object, fields)
	}

	// A colon inside a quoted value must not hide the mappings that follow it.
	assert.Contains(t, objects["c_score__c"], "type__c", "mapping after a quoted colon was dropped")
	assert.Contains(t, objects["c_score__c"], "decile__c", "mapping after a quoted colon was dropped")
	assert.Contains(t, objects["c_score__c"], "status__c")

	assert.Contains(t, objects["d_score__c"], "type__c", "mapping after a quoted colon was dropped")
	assert.Contains(t, objects["d_score__c"], "decile__c", "mapping after a quoted colon was dropped")

	// A quoted literal containing '@' must not be truncated into something field-shaped.
	assert.Contains(t, objects["e_score__c"], "type__c")
	assert.NotContains(t, objects["e_score__c"], "a")

	// map: a quoted literal is not a field.
	assert.Contains(t, objects["f_score__c"], "type__c")
	assert.NotContains(t, objects["f_score__c"], "lit")
	assert.NotContains(t, objects["f_score__c"], "'lit'")
}
