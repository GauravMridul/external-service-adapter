package unit

import (
	"testing"

	"esa/internal/app/models/esa_models"
	"esa/internal/app/utility"

	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

// TestExtractObjectsFieldsAndConditions_FromPreExecutionValidations ensures that
// fields referenced in PreExecution validations (and pick_response_from) are included
// in the Salesforce query so they are available in masterDTO when evaluating expressions.
func TestExtractObjectsFieldsAndConditions_FromPreExecutionValidations(t *testing.T) {
	// Config with ONLY additional_config pre_execution validations (no request_body).
	// If we didn't scan validations, Lead and Contact would be missing.
	config := &esa_models.ServiceConfigurationResponse{
		ID:           12,
		ServiceName:  "MobileUANService",
		AdditionalConfig: datatypes.JSON([]byte(`{
			"pre_execution": {
				"validations": [
					"{{CONDITION:<Lead.LoanCategory__c> IN ('TopUp Loan', 'Repeat'):CONTINUE:EXECUTE}}",
					"{{CONDITION:<Contact.Id> == '' || <Contact.Id> == null:CONTINUE:EXECUTE}}",
					"{{CONDITION:{{FORMAT:text:<Contact.employmentType__c>:lowercase}} IN ('self employed'):CONTINUE:EXECUTE}}",
					"{{CONDITION:<Lead.Sourcing_Program__c> IN ('Offline Store', 'QRO2O'):CONTINUE:EXECUTE}}",
					"{{CONDITION:{{TRANSFORM:{{CUSTOM:takeLast:<Contact.MobilePhone>}}:remove_spaces}}:length}} == 10:EXECUTE:CONTINUE}}"
				]
			}
		}`)),
	}

	objectsWithFields, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	// All objects referenced in validations must be present
	assert.Contains(t, objectsWithFields, "lead", "Lead should be extracted from PreExecution validations")
	assert.Contains(t, objectsWithFields, "contact", "Contact should be extracted from PreExecution validations")

	leadFields := objectsWithFields["lead"]
	contactFields := objectsWithFields["contact"]

	// id is always added by ExtractObjectsFieldsAndConditions
	assert.Contains(t, leadFields, "id")
	assert.Contains(t, contactFields, "id")
	// Fields from validation expressions
	assert.Contains(t, leadFields, "loancategory__c", "LoanCategory__c from validations")
	assert.Contains(t, leadFields, "sourcing_program__c", "Sourcing_Program__c from validations")
	assert.Contains(t, contactFields, "employmenttype__c", "employmentType__c from validations")
	assert.Contains(t, contactFields, "mobilephone", "MobilePhone from validations")
}

// TestExtractObjectsFieldsAndConditions_FromPostExecutionValidations ensures
// PostExecution validations are also scanned for <Object.Field> placeholders.
func TestExtractObjectsFieldsAndConditions_FromPostExecutionValidations(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID:          1,
		ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{
			"post_execution": {
				"validations": [
					"{{CONDITION:<Lead.Status> == 'Closed':EXIT:EXECUTE}}",
					"{{CONDITION:<Contact.Email> != '':CONTINUE:EXECUTE}}"
				]
			}
		}`)),
	}

	objectsWithFields, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assert.Contains(t, objectsWithFields, "lead")
	assert.Contains(t, objectsWithFields, "contact")
	assert.Contains(t, objectsWithFields["lead"], "status")
	assert.Contains(t, objectsWithFields["contact"], "email")
}

// TestExtractObjectsFieldsAndConditions_PreExecutionPickResponseFrom ensures
// pick_response_from is still extracted (regression test).
func TestExtractObjectsFieldsAndConditions_PreExecutionPickResponseFrom(t *testing.T) {
	config := &esa_models.ServiceConfigurationResponse{
		ID:          1,
		ServiceName: "SomeService",
		AdditionalConfig: datatypes.JSON([]byte(`{
			"PreExecution": {
				"pick_response_from": "<Contact.UAN_Number__c>"
			}
		}`)),
	}

	objectsWithFields, _ := utility.ExtractObjectsFieldsAndConditions([]*esa_models.ServiceConfigurationResponse{config})

	assert.Contains(t, objectsWithFields, "contact")
	assert.Contains(t, objectsWithFields["contact"], "uan_number__c")
}
