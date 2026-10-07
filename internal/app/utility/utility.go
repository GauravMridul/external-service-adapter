package utility

import (
	"encoding/json"
	"esa/internal/app/constants"
	"esa/internal/app/models/esa_models"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func UUIDFromString(input string) (u uuid.UUID, err error) {
	err = u.UnmarshalText([]byte(input))
	return
}

func ConvertFloatToString(amount float64) string {
	stringVal := strconv.FormatFloat(amount, 'f', 2, 64)
	return stringVal
}

func FormatDateFromCurrentToNewFormat(date string, currentFormat string, newFormat string) string {
	currentDate, _ := time.Parse(currentFormat, date)
	formattedDate := currentDate.Format(newFormat)
	return formattedDate
}

func GetRequestClientIpAddress(headers map[string]string) string {
	var ipAddress = headers[constants.ForwardedForHeaderKey]
	ipAddress = strings.TrimSpace(ipAddress)
	if len(ipAddress) > 0 {
		ipAddresses := strings.Split(ipAddress, ",")
		if len(ipAddresses) >= 2 {
			return strings.TrimSpace(ipAddresses[1])
		}
		return strings.TrimSpace(ipAddress)
	}
	ipAddress = headers[constants.RealIpHeaderKey]
	ipAddress = strings.TrimSpace(ipAddress)
	if len(ipAddress) > 0 {
		return ipAddress
	}
	return strings.TrimSpace(headers[constants.RemoteAddressHeaderKey])
}

func ConcatinateList(list []string, differentiator string) string {
	var concatinatedString string
	for _, value := range list {
		if len(concatinatedString) > 0 {
			concatinatedString = concatinatedString + differentiator
		}
		concatinatedString = concatinatedString + value
	}
	return concatinatedString
}

// ParseNestedSequenceString converts a sequence string like "{1,2,3;5,6;7}" into a nested array
// where semicolons separate the sub-arrays: [[1,2,3], [5,6], [7]]
func ParseNestedSequenceString(sequenceStr string) [][]int {
	sequenceStr = strings.Trim(sequenceStr, "{}")
	if sequenceStr == "" {
		return nil
	}

	groups := strings.Split(sequenceStr, ";")
	result := make([][]int, 0, len(groups))

	for _, group := range groups {
		nums := make([]int, 0, 4)
		start := 0
		for i := 0; i <= len(group); i++ {
			if i == len(group) || group[i] == ',' {
				if start < i {
					numStr := group[start:i]
					if num, err := strconv.Atoi(strings.TrimSpace(numStr)); err == nil {
						nums = append(nums, num)
					}
				}
				start = i + 1
			}
		}
		if len(nums) > 0 {
			result = append(result, nums)
		}
	}

	return result
}

// Pre-compiled regex patterns for better performance
var (
	// ObjectFieldPattern matches <Object.FieldPath> placeholders, preserving the entire related-field path
	// after the first dot (e.g. <Multibureau_Data__c.Contact__r.Lead__r.PreApproved_Date__c>).
	ObjectFieldPattern = regexp.MustCompile(`<([A-Za-z0-9_]+)\.([A-Za-z0-9_]+(?:\[[^\]]*\])?(?:\.[A-Za-z0-9_]+(?:\[[^\]]*\])?)*)>`)

	// Pattern to match fallback chains (|| separated).
	// The angle-bracket alternative requires the placeholder body to START with an identifier
	// character ([A-Za-z_]). This prevents a comparison operator ('<' / '<=') used inside an
	// expression (e.g. "<Payment_Schedules__c[1].Delinquent_Days__c> < 30 && <...>") from opening
	// a spurious placeholder span that greedily swallows the following genuine <Object.Field>
	// placeholder up to its closing '>'. Such spurious spans previously produced garbage object
	// names like " 30 && <payment_schedules__c[1]". Object names are always identifiers, so a real
	// placeholder never begins with a space, digit, or operator.
	fallbackPattern = regexp.MustCompile(`(<[A-Za-z_][^>]*>|\(\([^)]*\)\))(\s*\|\|\s*(<[A-Za-z_][^>]*>|\(\([^)]*\)\)))*`)

	// Pattern for service response placeholders
	serviceResponsePattern = regexp.MustCompile(`\(\(([A-Za-z0-9_]+)\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)\)\)`)

	// Pattern for conditional placeholders: Object[condition].fieldPath
	conditionalPlaceholderRegex = regexp.MustCompile(`^([A-Za-z0-9_]+)\[([^\]]+)\]\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)$`)

	// Pattern to detect ARRAY expressions with optional operations
	arrayExpressionPattern = regexp.MustCompile(`\{\{ARRAY:<([^>]+)>(?::([^}]+))?\}\}`)

	// Pattern to validate Salesforce object names (letters/numbers/underscore, starting with letter/underscore)
	objectNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseConditionalPlaceholder parses placeholder content and extracts object name, condition, and field name.
// This is a shared utility used by both field extraction and placeholder resolution to ensure consistency.
//
// Examples:
//   - "Bank_Facilities__c[Vendor_Type__c == 'FinBox Insights Data'].fis_v4__c" returns:
//     objectName: "Bank_Facilities__c", condition: "Vendor_Type__c == 'FinBox Insights Data'", fieldName: "fis_v4__c", isConditional: true
//   - "Contact.Email" returns:
//     objectName: "Contact", condition: "", fieldName: "Email", isConditional: false
func ParseConditionalPlaceholder(content string) (objectName, condition, fieldName string, isConditional bool) {
	// First check for conditional syntax: Object[condition].field
	if matches := conditionalPlaceholderRegex.FindStringSubmatch(content); len(matches) == 4 {
		return matches[1], matches[2], matches[3], true
	}

	// Handle simple case: Object.field
	if dotIndex := strings.Index(content, "."); dotIndex > 0 {
		return content[:dotIndex], "", content[dotIndex+1:], false
	}

	// Invalid format
	return "", "", "", false
}

// ExtractObjectsFromSingleConfig extracts object fields from a single service config
// Enhanced to support conditional filtering and fallback options
func ExtractObjectsFromSingleConfig(config *esa_models.ServiceConfigurationResponse, result map[string]map[string]struct{}) {
	if config.RequestBody == nil {
		return
	}

	var data interface{}
	if err := json.Unmarshal(config.RequestBody, &data); err != nil {
		return
	}

	extractFromInterface(data, result)

	// Also extract from URL and headers for completeness
	extractFromString(config.APIURL, result)

	// Extract from headers if they exist
	var headers map[string]interface{}
	if err := json.Unmarshal(config.Headers, &headers); err == nil {
		extractFromInterface(headers, result)
	}
}

// Enhanced version that also detects conditional patterns
func ExtractObjectsFieldsAndConditions(serviceConfigs []*esa_models.ServiceConfigurationResponse) (map[string][]string, map[string]bool) {
	// Store object -> set of fields
	objectsWithFields := make(map[string]map[string]struct{})
	// Store object -> has conditions flag
	objectsWithConditions := make(map[string]bool)

	for _, config := range serviceConfigs {
		ExtractObjectsFromSingleConfigWithConditions(config, objectsWithFields, objectsWithConditions)
	}

	// Convert to map[string][]string with pre-allocated capacity and ensure 'id' field
	result := make(map[string][]string, len(objectsWithFields))
	for obj, fieldSet := range objectsWithFields {
		// Ensure 'id' field is always included
		fieldSet["id"] = struct{}{}

		fields := make([]string, 0, len(fieldSet))
		for field := range fieldSet {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		result[obj] = fields
	}

	return result, objectsWithConditions
}

// Enhanced version that tracks conditional patterns
func ExtractObjectsFromSingleConfigWithConditions(config *esa_models.ServiceConfigurationResponse, result map[string]map[string]struct{}, _ map[string]bool) {
	if config.RequestBody != nil {
		var data interface{}
		if err := json.Unmarshal(config.RequestBody, &data); err == nil {
			extractFromInterfaceWithConditions(data, result, nil)
		}
	}

	// Also extract from URL and headers for completeness
	extractFromStringWithConditions(config.APIURL, result, nil)

	if len(config.Headers) > 0 {
		var headers map[string]interface{}
		if err := json.Unmarshal(config.Headers, &headers); err == nil {
			extractFromInterfaceWithConditions(headers, result, nil)
		}
	}

	// Extract from PreExecution/PostExecution: pick_response_from and validations so referenced <Object.Field> are included in SF query
	if len(config.AdditionalConfig) > 0 {
		var ac map[string]interface{}
		if err := json.Unmarshal(config.AdditionalConfig, &ac); err == nil {
			for _, key := range []string{"PreExecution", "pre_execution", "PostExecution", "post_execution"} {
				execMap, _ := ac[key].(map[string]interface{})
				if execMap == nil {
					continue
				}
				if s, ok := execMap["pick_response_from"].(string); ok && strings.TrimSpace(s) != "" {
					extractFromStringWithConditions(s, result, nil)
				}
				for _, valKey := range []string{"validations", "Validations"} {
					arr, _ := execMap[valKey].([]interface{})
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							extractFromStringWithConditions(s, result, nil)
						}
					}
				}
				for execKey, execVal := range execMap {
					execKeyLower := strings.ToLower(strings.TrimSpace(execKey))
					if execKeyLower == "enabled" || execKeyLower == "validations" || execKeyLower == "pick_response_from" {
						continue
					}
					if s, ok := execVal.(string); ok && strings.TrimSpace(s) != "" {
						extractFromStringWithConditions(s, result, nil)
					}
				}
			}
		}
	}
}

// Enhanced recursive function to traverse the JSON structure and track conditions
func extractFromInterfaceWithConditions(data interface{}, result map[string]map[string]struct{}, _ map[string]bool) {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, val := range v {
			// Extract from both key and value
			extractFromStringWithConditions(key, result, nil)
			extractFromInterfaceWithConditions(val, result, nil)
		}
	case []interface{}:
		for _, item := range v {
			extractFromInterfaceWithConditions(item, result, nil)
		}
	case string:
		extractFromStringWithConditions(v, result, nil)
	}
}

// Enhanced string extraction to detect conditional patterns
func extractFromStringWithConditions(text string, result map[string]map[string]struct{}, _ map[string]bool) {
	// Find all fallback chains first
	fallbackMatches := fallbackPattern.FindAllString(text, -1)
	for _, chain := range fallbackMatches {
		extractPlaceholdersFromChainWithConditions(chain, result, nil)
	}

	// Detect ARRAY expressions with potential object-only references
	extractFromArrayExpressions(text, result)

	// Find standalone placeholders not part of fallback chains
	extractStandalonePlaceholdersWithConditions(text, result, nil)
}

// extractFromArrayExpressions processes ARRAY expressions and extracts relevant object fields.
func extractFromArrayExpressions(text string, result map[string]map[string]struct{}) {
	matches := arrayExpressionPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		objectRef := strings.TrimSpace(match[1])
		if objectRef == "" || strings.Contains(objectRef, ".") {
			continue
		}

		if objectNamePattern.MatchString(objectRef) {
			// Attempt to parse transform spec (match[2]) if provided
			transformSpec := ""
			if len(match) >= 3 {
				transformSpec = strings.TrimSpace(match[2])
			}

			extractFieldsFromArrayTransformSpec(objectRef, transformSpec, result)
		}
	}
}

// extractFieldsFromArrayTransformSpec adds fields referenced within ARRAY transform specifications.
func extractFieldsFromArrayTransformSpec(objectName, transformSpec string, result map[string]map[string]struct{}) {
	if transformSpec == "" {
		return
	}

	// Split on colons that sit OUTSIDE quotes. A quoted literal may legitimately contain a colon
	// (a `??'N/A: none'` default, a `'00:00'->t` static literal); a naive split truncated the spec
	// there and silently dropped every mapping that followed, so those fields never reached the
	// SELECT list and emitted null at runtime.
	parts := SplitTransformSpecUnquoted(transformSpec, ':')
	if len(parts) == 0 {
		return
	}

	op := strings.TrimSpace(parts[0])

	switch op {
	case "transform", "transform-only":
		if len(parts) < 2 {
			break
		}

		// Quote-aware on commas for the same reason, and to match the evaluator's
		// splitTransformPairs exactly. A naive split cut a quoted default such as
		// ??'A,Other__c->B' in half, and the trailing fragment looked like a mapping pair, which
		// injected a non-existent column and failed the whole query.
		fieldMappings := SplitTransformSpecUnquoted(strings.TrimSpace(parts[1]), ',')
		for _, mapping := range fieldMappings {
			mapping = strings.TrimSpace(mapping)
			if mapping == "" {
				continue
			}

			// Remove a ??default suffix (field->alias@TYPE??-999) FIRST. The default is a
			// literal, never a field, and it must not reach the SOQL SELECT list. Stripping it
			// before the @TYPE split also keeps a default that contains '@' from confusing it.
			// Only an UNQUOTED ?? is the operator, so the static literal '??'->tag is left intact
			// (mirrors splitMappingDefault in the evaluator).
			if separatorIndex := IndexUnquotedToken(mapping, "??"); separatorIndex >= 0 {
				mapping = mapping[:separatorIndex]
			}

			// Remove the type annotation (field->alias@TYPE). Quote-aware so a quoted literal
			// containing '@' is not truncated into something that looks like a field.
			mapping = SplitTransformSpecUnquoted(mapping, '@')[0]

			if !strings.Contains(mapping, "->") {
				continue
			}

			sourceField := strings.TrimSpace(strings.Split(mapping, "->")[0])

			// A quoted left-hand side injects a constant on every element; it is a literal, not a
			// field, so it must never be queried.
			if IsQuotedLiteral(sourceField) {
				continue
			}

			if sourceField != "" && isValidFieldName(sourceField) {
				addObjectField(objectName, sourceField, result)
			}
		}
	case "map":
		if len(parts) < 2 {
			break
		}

		fields := SplitTransformSpecUnquoted(strings.TrimSpace(parts[1]), ',')
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if IsQuotedLiteral(field) {
				continue
			}
			if field != "" && isValidFieldName(field) {
				addObjectField(objectName, field, result)
			}
		}
	}

	// Ensure Id is always requested for object records
	addObjectField(objectName, "Id", result)
}

// Extract placeholders from a fallback chain with condition detection
func extractPlaceholdersFromChainWithConditions(chain string, result map[string]map[string]struct{}, _ map[string]bool) {
	// Split by || to get individual placeholders
	parts := strings.Split(chain, "||")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
			extractFromAngleBracketPlaceholderWithConditions(part, result, nil)
		}
	}
}

// Extract standalone placeholders with condition detection
func extractStandalonePlaceholdersWithConditions(text string, result map[string]map[string]struct{}, _ map[string]bool) {
	// Remove fallback chains from text to avoid double processing
	textWithoutChains := fallbackPattern.ReplaceAllString(text, "")

	// Find angle bracket placeholders
	matches := ObjectFieldPattern.FindAllStringSubmatch(textWithoutChains, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			objectName := match[1]
			fieldName := match[2]

			// Add the main field
			addObjectField(objectName, fieldName, result)
		}
	}

}

// Extract placeholders from angle bracket notation with condition detection
func extractFromAngleBracketPlaceholderWithConditions(placeholder string, result map[string]map[string]struct{}, _ map[string]bool) {
	// Remove < and >
	content := placeholder[1 : len(placeholder)-1]

	// Use shared parsing utility to handle both conditional and simple syntax
	objectName, condition, fieldName, isConditional := ParseConditionalPlaceholder(content)
	if objectName != "" && fieldName != "" {
		// Add the target field
		addObjectField(objectName, fieldName, result)

		// If this is a conditional placeholder, extract fields from the condition
		if isConditional && condition != "" {
			extractFieldsFromCondition(objectName, condition, result)
		}
	}
}

// Add object field to result map with normalization.
//
// Object names are always plain Salesforce identifiers (letters/digits/underscore, e.g. "lead",
// "payment_schedules__c", "lab_mb_crif"). If the caller supplies a value that is not a valid
// identifier, it is a fragment mis-parsed from an expression (comparison operators, quotes,
// braces, embedded '<'/'(' etc.) rather than a real object; storing it would pollute the
// Salesforce query and trigger "No query object relationship map found" lookups. We therefore
// drop it here — the single choke point shared by every extraction path.
//
// NOTE: only the OBJECT NAME is validated. Field names are intentionally NOT identifier-checked
// because relationship paths are legitimate (e.g. "campaign__r.bscore__c",
// "contact__r.lead__r.preapproved_date__c").
func addObjectField(objectName, fieldName string, result map[string]map[string]struct{}) {
	trimmedObjectName := strings.TrimSpace(objectName)
	if !objectNamePattern.MatchString(trimmedObjectName) {
		return
	}

	// Normalize both object name and field name to lowercase for consistent processing
	normalizedObjectName := strings.ToLower(trimmedObjectName)
	normalizedFieldName := strings.ToLower(strings.TrimSpace(fieldName))
	if normalizedFieldName == "" {
		return
	}

	// Exclude synthetic ARRAY transform position tokens (__rownum__ / __index__). These are NOT
	// Salesforce fields — they inject the element's array position (row number / index) into the
	// transform output — so they must never be added to the SOQL field set, or the generated query
	// fails with "No such column '__rownum__'". This is the single choke point shared by every
	// extraction path (including extractFieldsFromArrayTransformSpec), so guarding here covers all
	// ARRAY transform / transform-only specs that reference these tokens as a mapping source.
	if normalizedFieldName == "__rownum__" || normalizedFieldName == "__index__" {
		return
	}

	if result[normalizedObjectName] == nil {
		result[normalizedObjectName] = make(map[string]struct{})
	}
	result[normalizedObjectName][normalizedFieldName] = struct{}{}
}

// removed unused helper isReservedWord

// Legacy functions for backward compatibility - these delegate to the new specialized utilities

// extractFromInterface recursively traverses the JSON structure
func extractFromInterface(data interface{}, result map[string]map[string]struct{}) {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, val := range v {
			// Extract from both key and value
			extractFromString(key, result)
			extractFromInterface(val, result)
		}
	case []interface{}:
		for _, item := range v {
			extractFromInterface(item, result)
		}
	case string:
		extractFromString(v, result)
	}
}

// extractFromString extracts placeholders from a string
func extractFromString(text string, result map[string]map[string]struct{}) {
	// Find all fallback chains first
	fallbackMatches := fallbackPattern.FindAllString(text, -1)
	for _, chain := range fallbackMatches {
		extractPlaceholdersFromChain(chain, result)
	}

	// Find standalone placeholders not part of fallback chains
	extractStandalonePlaceholders(text, result)
}

// Extract placeholders from a fallback chain
func extractPlaceholdersFromChain(chain string, result map[string]map[string]struct{}) {
	// Split by || to get individual placeholders
	parts := strings.Split(chain, "||")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
			extractFromAngleBracketPlaceholder(part, result)
		} else if strings.HasPrefix(part, "((") && strings.HasSuffix(part, "))") {
			extractFromServiceResponsePlaceholder(part, result)
		}
	}
}

// Extract standalone placeholders
func extractStandalonePlaceholders(text string, result map[string]map[string]struct{}) {
	// Remove fallback chains from text to avoid double processing
	textWithoutChains := fallbackPattern.ReplaceAllString(text, "")

	// Find angle bracket placeholders
	matches := ObjectFieldPattern.FindAllStringSubmatch(textWithoutChains, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			objectName := match[1]
			fieldName := match[2]
			addObjectField(objectName, fieldName, result)
		}
	}

}

// Extract placeholders from angle bracket notation
func extractFromAngleBracketPlaceholder(placeholder string, result map[string]map[string]struct{}) {
	// Remove < and >
	content := placeholder[1 : len(placeholder)-1]

	// Use shared parsing utility to handle both conditional and simple syntax
	objectName, condition, fieldName, isConditional := ParseConditionalPlaceholder(content)
	if objectName != "" && fieldName != "" {
		// Add the target field
		addObjectField(objectName, fieldName, result)

		// If this is a conditional placeholder, extract fields from the condition
		if isConditional && condition != "" {
			extractFieldsFromCondition(objectName, condition, result)
		}
	}
}

// extractFieldsFromCondition extracts field names from conditional expressions
// Examples:
//   - "Vendor_Type__c == 'FinBox Insights Data'" -> extracts "Vendor_Type__c"
//   - "Status__c != 'Inactive' AND Type__c == 'Premium'" -> extracts "Status__c", "Type__c"
func extractFieldsFromCondition(objectName, condition string, result map[string]map[string]struct{}) {
	// Common comparison operators to look for
	// Note: Check longer operators first (>=, <=, ==, !=) before single char operators (=, >, <)
	operators := []string{">=", "<=", "==", "!=", ">", "<", " = ", " != ", "=", " IN ", " NOT IN "}

	// Split condition by AND/OR to handle multiple conditions
	conditionParts := []string{condition}
	for _, separator := range []string{" AND ", " OR ", " && ", " || "} {
		var newParts []string
		for _, part := range conditionParts {
			newParts = append(newParts, strings.Split(part, separator)...)
		}
		conditionParts = newParts
	}

	// Extract field names from each condition part
	for _, part := range conditionParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Look for field names before comparison operators
		for _, op := range operators {
			if idx := strings.Index(part, op); idx > 0 {
				fieldName := strings.TrimSpace(part[:idx])
				// Remove any parentheses or quotes around field name
				fieldName = strings.Trim(fieldName, "()\"'")
				if fieldName != "" && isValidFieldName(fieldName) {
					addObjectField(objectName, fieldName, result)
				}
				break
			}
		}
	}
}

// isValidFieldName checks if a string looks like a valid Salesforce field name
func isValidFieldName(fieldName string) bool {
	// Basic validation: should contain letters/numbers/underscores, no spaces or special chars
	if len(fieldName) == 0 {
		return false
	}
	for _, char := range fieldName {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return true
}

// Extract placeholders from service response notation
func extractFromServiceResponsePlaceholder(placeholder string, result map[string]map[string]struct{}) {
	// Service responses are not queried from Salesforce, so skip extraction
}
