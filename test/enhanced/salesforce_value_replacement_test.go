// Package main_test runs tests for Salesforce SOQL value-replacement behavior.
// It validates that policy_parameter__c (and similar objects) get additional_conditions
// placeholders like @{lead_query.records[0].pulled_leadsource__c} replaced with actual
// values so that SOQL never contains the literal "@{" (which causes MALFORMED_QUERY).
package main_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSalesforceValueReplacement runs the client package tests that validate
// replaceCompositeReferencesWithValues and buildSOQLQueryWithValueReplacement
// so that SOQL for dependent objects (e.g. policy_parameter__c) does not contain
// unresolved @{} placeholders.
func TestSalesforceValueReplacement(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cmd := exec.Command("go", "test", "./pkg/client", "-run", "TestReplaceCompositeReferencesWithValues|TestBuildSOQLQueryWithValueReplacement|TestEnsureQueryObjectsForDependencies", "-v", "-count=1")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("client test output:\n%s", string(out))
		t.Fatalf("Salesforce value-replacement tests failed: %v", err)
	}
	t.Logf("Salesforce value-replacement tests passed:\n%s", string(out))
}
