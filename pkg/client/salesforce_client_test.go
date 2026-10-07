package client

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"esa/internal/app/models/esa_models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalClient returns a SalesforceClient with only the fields needed for SOQL building and value replacement.
func minimalClient() *SalesforceClient {
	c := &SalesforceClient{
		dependencyRegex: regexp.MustCompile(`<([^.>]+)\.([^>]+)>`),
	}
	return c
}

func TestReplaceCompositeReferencesWithValues_ReplacesPlaceholder(t *testing.T) {
	c := minimalClient()

	// Simulate additional_conditions after processAdditionalConditionsReferences: @{lead_query.records[0].pulled_leadsource__c}
	conditions := "LeadSource__c = @{lead_query.records[0].pulled_leadsource__c}"

	successfulResults := map[string]interface{}{
		"lead": map[string]interface{}{
			"Id":                  "lead123",
			"Pulled_Leadsource__c": "Web",
		},
	}

	out := c.replaceCompositeReferencesWithValues(conditions, successfulResults)

	assert.NotContains(t, out, "@{", "SOQL must not contain unresolved placeholder")
	assert.Contains(t, out, "'Web'", "SOQL must contain the replaced value")
	assert.Contains(t, out, "LeadSource__c = 'Web'", "full clause should be replaced")
}

func TestReplaceCompositeReferencesWithValues_CaseInsensitiveObjectKey(t *testing.T) {
	c := minimalClient()

	conditions := "LeadSource__c = @{lead_query.records[0].pulled_leadsource__c}"

	// Results keyed by "Lead" (capital L) as might come from ReferenceID trim
	successfulResults := map[string]interface{}{
		"Lead": map[string]interface{}{
			"Pulled_Leadsource__c": "Partner",
		},
	}

	out := c.replaceCompositeReferencesWithValues(conditions, successfulResults)

	assert.NotContains(t, out, "@{")
	assert.Contains(t, out, "'Partner'")
}

func TestReplaceCompositeReferencesWithValues_KeepsPlaceholderWhenValueMissing(t *testing.T) {
	c := minimalClient()

	conditions := "LeadSource__c = @{lead_query.records[0].pulled_leadsource__c}"
	successfulResults := map[string]interface{}{} // no lead

	out := c.replaceCompositeReferencesWithValues(conditions, successfulResults)

	assert.Contains(t, out, "@{lead_query.records[0].pulled_leadsource__c}", "when value missing, placeholder is left as-is")
}

func TestBuildSOQLQueryWithValueReplacement_NoPlaceholderInOutput(t *testing.T) {
	c := minimalClient()

	objectName := "policy_parameter__c"
	fields := []string{"Id", "Name", "LeadSource__c"}
	queryObject := &esa_models.QueryObjectRelationshipMap{
		QueryObject:   "policy_parameter__c",
		QueryRelation: "Some_Ref__c",
		AdditionalConditions: sql.NullString{
			Valid:  true,
			String: "AND LeadSource__c = <Lead.Pulled_Leadsource__c>",
		},
	}
	refID := "ref123"
	successfulResults := map[string]interface{}{
		"lead": map[string]interface{}{
			"Pulled_Leadsource__c": "Web",
		},
	}

	query, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(), objectName, fields, queryObject, refID, successfulResults)
	require.NoError(t, err)
	assert.True(t, allReplaced, "all placeholders should be replaced when dependency has the field")

	assert.NotContains(t, query, "@{", "SOQL must not contain any @{} placeholder")
	assert.NotContains(t, query, "lead_query.records[0].pulled_leadsource__c", "composite reference must be replaced")
	assert.Contains(t, query, "'Web'", "actual value must appear in SOQL")
	assert.Contains(t, query, "LeadSource__c = 'Web'", "additional condition must use replaced value")
}

// TestBuildSOQLQueryWithValueReplacement_AllReplacedFalseWhenFieldMissing verifies that when the dependency
// (e.g. Lead) was queried successfully but the referenced field (e.g. dealer_code__c) is missing from the
// result (SF omits null fields from JSON), allReplaced is false so the caller can skip the query and bind {}.
func TestBuildSOQLQueryWithValueReplacement_AllReplacedFalseWhenFieldMissing(t *testing.T) {
	c := minimalClient()

	objectName := "dealer_info__c"
	fields := []string{"Id", "Name"}
	queryObject := &esa_models.QueryObjectRelationshipMap{
		QueryObject:   "dealer_info__c",
		QueryRelation: "Ref__c",
		AdditionalConditions: sql.NullString{
			Valid:  true,
			String: "AND Dealer_Code__c = <Lead.dealer_code__c>",
		},
	}
	refID := "ref123"
	// Lead record present but dealer_code__c missing (e.g. null and omitted by SF)
	successfulResults := map[string]interface{}{
		"lead": map[string]interface{}{
			"Id":   "lead123",
			"Name": "Test",
			// dealer_code__c not present - SF behavior for null
		},
	}

	query, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(), objectName, fields, queryObject, refID, successfulResults)
	require.NoError(t, err)
	assert.False(t, allReplaced, "allReplaced should be false when dependency field is missing so caller skips and binds {}")
	assert.Contains(t, query, "@{", "query still contains unresolved placeholder")
}

func TestEnsureQueryObjectsForDependencies_AddsMissingLead(t *testing.T) {
	c := minimalClient()

	enhanced := map[string][]string{
		"policy_parameter__c": {"Id", "Name"},
		"Lead":                {"Id", "Pulled_Leadsource__c"},
	}
	queryObjects := map[string]*esa_models.QueryObjectRelationshipMap{
		"policy_parameter__c": {
			QueryObject:   "policy_parameter__c",
			QueryRelation: "Ref__c",
			AdditionalConditions: sql.NullString{Valid: true, String: "AND LeadSource__c = <Lead.Pulled_Leadsource__c>"},
		},
	}

	// Before: no Lead in queryObjects
	_, ok := queryObjects["lead"]
	assert.False(t, ok)
	_, ok = queryObjects["Lead"]
	assert.False(t, ok)

	// ensureQueryObjectsForDependencies needs repo; without repo it returns queryObjects as-is
	out := c.ensureQueryObjectsForDependencies(context.Background(), enhanced, queryObjects)

	// With no repo/cache, missing names are not fetched, so we get back the same map
	assert.Equal(t, queryObjects, out)

	// When we have a repo that returns Lead, combined should have Lead. Test that when we pass
	// queryObjects that already has "lead" (from a fetch), we don't duplicate.
	queryObjectsWithLead := map[string]*esa_models.QueryObjectRelationshipMap{
		"policy_parameter__c": queryObjects["policy_parameter__c"],
		"lead":                 {QueryObject: "lead", QueryRelation: "Id"},
	}
	out2 := c.ensureQueryObjectsForDependencies(context.Background(), enhanced, queryObjectsWithLead)
	assert.Equal(t, queryObjectsWithLead, out2, "when no missing, return same map")
}
