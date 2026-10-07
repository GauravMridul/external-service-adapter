package client

import (
	"context"
	"database/sql"
	"testing"

	"esa/internal/app/models/esa_models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptySet returns a classifier that marks the given placeholder strings as empty; all others
// are non-empty and always classifiable (ok=true).
func emptySet(refs ...string) emptyClassifier {
	set := make(map[string]bool, len(refs))
	for _, r := range refs {
		set[r] = true
	}
	return func(ph string) (bool, bool) {
		return set[ph], true
	}
}

// classifierUnknownFor returns a classifier that cannot classify `unknown` (ok=false), marks the
// listed refs empty, and treats everything else as non-empty.
func classifierUnknownFor(unknown string, empty ...string) emptyClassifier {
	set := make(map[string]bool, len(empty))
	for _, r := range empty {
		set[r] = true
	}
	return func(ph string) (bool, bool) {
		if ph == unknown {
			return false, false
		}
		return set[ph], true
	}
}

func TestPruneWhereConditions(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		classify  emptyClassifier
		wantText  string
		wantDecis pruneDecision
	}{
		{
			name:      "OR group: prune one empty branch, keep survivors",
			input:     "WHERE (A = @{a} OR B = @{b} OR C = @{c})",
			classify:  emptySet("@{c}"),
			wantText:  "WHERE A = @{a} OR B = @{b}",
			wantDecis: prunedOK,
		},
		{
			name:      "OR group: prune two empty branches, single survivor",
			input:     "WHERE (A = @{a} OR B = @{b} OR C = @{c})",
			classify:  emptySet("@{a}", "@{c}"),
			wantText:  "WHERE B = @{b}",
			wantDecis: prunedOK,
		},
		{
			name:      "OR group: all empty -> skip",
			input:     "WHERE (A = @{a} OR B = @{b})",
			classify:  emptySet("@{a}", "@{b}"),
			wantDecis: prunedSkip,
		},
		{
			name:      "static AND (OR group): partial prune keeps static + survivors + trailing",
			input:     "WHERE CreatedDate >= LAST_N_DAYS:30 AND (A = @{a} OR B = @{b} OR C = @{c}) ORDER BY CreatedDate DESC",
			classify:  emptySet("@{a}"),
			wantText:  "WHERE CreatedDate >= LAST_N_DAYS:30 AND (B = @{b} OR C = @{c}) ORDER BY CreatedDate DESC",
			wantDecis: prunedOK,
		},
		{
			name:      "static AND (OR group): all OR branches empty -> skip (never keep static-only)",
			input:     "WHERE CreatedDate >= LAST_N_DAYS:30 AND (A = @{a} OR B = @{b} OR C = @{c}) ORDER BY CreatedDate DESC",
			classify:  emptySet("@{a}", "@{b}", "@{c}"),
			wantDecis: prunedSkip,
		},
		{
			name:      "AND with one empty operand -> skip",
			input:     "WHERE A = @{a} AND B = @{b}",
			classify:  emptySet("@{a}"),
			wantDecis: prunedSkip,
		},
		{
			name:      "single empty comparison -> skip",
			input:     "WHERE A = @{a}",
			classify:  emptySet("@{a}"),
			wantDecis: prunedSkip,
		},
		{
			name:      "nothing empty -> all retained",
			input:     "WHERE (A = @{a} OR B = @{b})",
			classify:  emptySet(),
			wantText:  "WHERE A = @{a} OR B = @{b}",
			wantDecis: prunedOK,
		},
		{
			name:      "quote-aware: OR inside a string literal is not a boolean operator",
			input:     "WHERE (A = 'X OR Y' OR B = @{b})",
			classify:  emptySet("@{b}"),
			wantText:  "WHERE A = 'X OR Y'",
			wantDecis: prunedOK,
		},
		{
			name:      "non-wholly-wrapped: (A) OR (B) splits at top level",
			input:     "WHERE (A = @{a}) OR (B = @{b})",
			classify:  emptySet("@{a}"),
			wantText:  "WHERE B = @{b}",
			wantDecis: prunedOK,
		},
		{
			name:      "NOT (...) treated as opaque leaf and pruned when its placeholder is empty",
			input:     "WHERE NOT (A = @{a}) OR B = @{b}",
			classify:  emptySet("@{a}"),
			wantText:  "WHERE B = @{b}",
			wantDecis: prunedOK,
		},
		{
			name:      "NOT (...) opaque leaf retained when its placeholder resolves",
			input:     "WHERE NOT (A = @{a}) OR B = @{b}",
			classify:  emptySet("@{b}"),
			wantText:  "WHERE NOT (A = @{a})",
			wantDecis: prunedOK,
		},
		{
			name:      "nested groups: prune inner OR branch, preserve grouping",
			input:     "WHERE ((A = @{a} OR B = @{b}) AND (C = @{c} OR D = @{d}))",
			classify:  emptySet("@{b}"),
			wantText:  "WHERE (A = @{a}) AND (C = @{c} OR D = @{d})",
			wantDecis: prunedOK,
		},
		{
			name:     "IN-list parentheses are not boolean grouping (treated as part of the leaf)",
			input:    "WHERE Type__c IN ('A','B') AND (X = @{x} OR Y = @{y})",
			classify: emptySet("@{x}"),
			// A reduced OR-group nested under AND stays parenthesized (safe, precedence-preserving).
			wantText:  "WHERE Type__c IN ('A','B') AND (Y = @{y})",
			wantDecis: prunedOK,
		},
		{
			name:      "unbalanced parentheses -> fallback",
			input:     "WHERE (A = @{a} OR B = @{b}",
			classify:  emptySet("@{a}"),
			wantDecis: prunedFallback,
		},
		{
			name:      "missing WHERE prefix -> fallback",
			input:     "AND A = @{a}",
			classify:  emptySet("@{a}"),
			wantDecis: prunedFallback,
		},
		{
			name:      "unclassifiable placeholder -> fallback",
			input:     "WHERE (A = @{a} OR B = @{x})",
			classify:  classifierUnknownFor("@{x}", "@{a}"),
			wantDecis: prunedFallback,
		},
		{
			name:     "case-insensitive and lowercase connectors",
			input:    "WHERE (A = @{a} or B = @{b}) and C = @{c}",
			classify: emptySet("@{a}"),
			// The reduced OR-group (single survivor) remains parenthesized under the AND.
			wantText:  "WHERE (B = @{b}) AND C = @{c}",
			wantDecis: prunedOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, decision := pruneWhereConditions(tc.input, tc.classify)
			assert.Equal(t, tc.wantDecis, decision, "decision mismatch")
			if tc.wantDecis == prunedOK {
				assert.Equal(t, tc.wantText, got, "pruned clause mismatch")
			}
		})
	}
}

func TestIsBlankSOQLValue(t *testing.T) {
	assert.True(t, isBlankSOQLValue(nil), "nil is blank")
	assert.True(t, isBlankSOQLValue(""), "empty string is blank")
	assert.True(t, isBlankSOQLValue("   "), "whitespace string is blank")
	assert.False(t, isBlankSOQLValue("x"), "non-empty string is not blank")
	assert.False(t, isBlankSOQLValue(0), "zero int is not blank")
	assert.False(t, isBlankSOQLValue(false), "false bool is not blank")
	assert.False(t, isBlankSOQLValue([]interface{}{}), "empty array is not blank")
	assert.False(t, isBlankSOQLValue(map[string]interface{}{}), "empty object is not blank")
}

// --- Integration tests through buildSOQLQueryWithValueReplacement (real config shapes) ---

func condQO(objectName, queryRelation, additionalConditions, additionalFields string) *esa_models.QueryObjectRelationshipMap {
	return &esa_models.QueryObjectRelationshipMap{
		QueryObject:          objectName,
		QueryRelation:        queryRelation,
		AdditionalFields:     sql.NullString{Valid: additionalFields != "", String: additionalFields},
		AdditionalConditions: sql.NullString{Valid: additionalConditions != "", String: additionalConditions},
	}
}

// id 33: WHERE CreatedDate >= LAST_N_DAYS:30 AND (PAN OR Mobile OR Email) — Email blank -> pruned.
func TestBuildSOQL_Id33_PartialPrune(t *testing.T) {
	c := minimalClient()
	qo := condQO("Multibureau_Consolidate_Data__c", "",
		"WHERE CreatedDate >= LAST_N_DAYS:30 AND (PAN__c = <contact.PAN_ID__c> OR Mobile_Number__c = <contact.Phone> OR Email__c = <contact.Email>) ORDER BY CreatedDate DESC",
		"PAN__c")
	results := map[string]interface{}{
		"contact": map[string]interface{}{
			"PAN_ID__c": "EGGGP7777G",
			"Phone":     "9000000005",
			"Email":     "", // blank -> pruned
		},
	}

	q, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Multibureau_Consolidate_Data__c", []string{"PAN__c"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.True(t, allReplaced, "query should run with surviving branches")
	assert.Contains(t, q, "CreatedDate >= LAST_N_DAYS:30")
	assert.Contains(t, q, "PAN__c = 'EGGGP7777G'")
	assert.Contains(t, q, "Mobile_Number__c = '9000000005'")
	assert.NotContains(t, q, "Email__c", "blank Email branch must be pruned")
	assert.NotContains(t, q, "''", "no empty-string equality should be emitted")
	assert.NotContains(t, q, "@{", "no unresolved placeholder")
	assert.Contains(t, q, "ORDER BY CreatedDate DESC", "trailing clause preserved")
}

// id 33 with all identity fields blank -> whole query skipped.
func TestBuildSOQL_Id33_AllEmpty_Skips(t *testing.T) {
	c := minimalClient()
	qo := condQO("Multibureau_Consolidate_Data__c", "",
		"WHERE CreatedDate >= LAST_N_DAYS:30 AND (PAN__c = <contact.PAN_ID__c> OR Mobile_Number__c = <contact.Phone> OR Email__c = <contact.Email>) ORDER BY CreatedDate DESC",
		"PAN__c")
	results := map[string]interface{}{
		"contact": map[string]interface{}{"PAN_ID__c": "", "Phone": "", "Email": ""},
	}

	_, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Multibureau_Consolidate_Data__c", []string{"PAN__c"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.False(t, allReplaced, "all identity fields blank -> skip the query")
}

// id 33 with every field present -> not engaged, byte-identical substitution (all branches kept).
func TestBuildSOQL_Id33_NoneEmpty_RunsUnchanged(t *testing.T) {
	c := minimalClient()
	qo := condQO("Multibureau_Consolidate_Data__c", "",
		"WHERE CreatedDate >= LAST_N_DAYS:30 AND (PAN__c = <contact.PAN_ID__c> OR Mobile_Number__c = <contact.Phone> OR Email__c = <contact.Email>) ORDER BY CreatedDate DESC",
		"PAN__c")
	results := map[string]interface{}{
		"contact": map[string]interface{}{
			"PAN_ID__c": "EGGGP7777G",
			"Phone":     "9000000005",
			"Email":     "a@b.com",
		},
	}

	q, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Multibureau_Consolidate_Data__c", []string{"PAN__c"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.True(t, allReplaced)
	assert.Contains(t, q, "PAN__c = 'EGGGP7777G'")
	assert.Contains(t, q, "Mobile_Number__c = '9000000005'")
	assert.Contains(t, q, "Email__c = 'a@b.com'")
	assert.NotContains(t, q, "@{")
}

// id 93-style: WHERE with two AND-ed placeholders; one blank -> skip (AND cannot drop a branch).
func TestBuildSOQL_Id93_AndBlank_Skips(t *testing.T) {
	c := minimalClient()
	qo := condQO("Policy_Parameter__c", "",
		"WHERE LeadSource__r.Name = <lead.Pulled_Leadsource__c> and Business_type__c = <lead.Business_Type__c>",
		"")
	results := map[string]interface{}{
		"lead": map[string]interface{}{
			"Pulled_Leadsource__c": "", // blank
			"Business_Type__c":     "SENP",
		},
	}

	_, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Policy_Parameter__c", []string{"Id"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.False(t, allReplaced, "AND clause with a blank operand -> skip")
}

// id 10-style: single placeholder present -> runs unchanged (not engaged).
func TestBuildSOQL_Id10_SinglePresent_Runs(t *testing.T) {
	c := minimalClient()
	qo := condQO("Dealer_Info__c", "", "WHERE Dealer_Code__c = <lead.Dealer_Code__c>", "")
	results := map[string]interface{}{
		"lead": map[string]interface{}{"Dealer_Code__c": "DLR123"},
	}

	q, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Dealer_Info__c", []string{"Id"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.True(t, allReplaced)
	assert.Contains(t, q, "Dealer_Code__c = 'DLR123'")
	assert.NotContains(t, q, "@{")
}

// id 20-style: AND-appended condition on a query_relation object is never engaged by the pruner.
func TestBuildSOQL_Id20_AndAppended_NotEngaged(t *testing.T) {
	c := minimalClient()
	qo := condQO("Transaction_Data__c", "Contact_Id__r.Lead__c",
		"AND Contact_Id__r.Applicant_Type__c = 'Individual'", "Transaction_data_ID__c")
	results := map[string]interface{}{}

	q, allReplaced, err := c.buildSOQLQueryWithValueReplacement(context.Background(),
		"Transaction_Data__c", []string{"Id"}, qo, "00Q9H00000GplZxUAJ", results)

	require.NoError(t, err)
	assert.True(t, allReplaced)
	assert.Contains(t, q, "WHERE Contact_Id__r.Lead__c = '00Q9H00000GplZxUAJ'")
	assert.Contains(t, q, "AND Contact_Id__r.Applicant_Type__c = 'Individual'")
}
