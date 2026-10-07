package sequence_service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
)

// ============================================================================
// VARIABLES FEATURE
//
// A service's additional_config may declare a single "variables" map:
//
//	"variables": {
//	  "mobile1":             "<Lead.MobilePhone> || <Contact.MobilePhone>",
//	  "clean_mobile":        "{{CUSTOM:formatPhone(((var.mobile1)))}}",
//	  "mobile_match_result": "((self))"
//	}
//
// Every value is an ordinary config expression (same grammar as request_body). Definitions from
// all services are aggregated into one sequence-level registry (masterDTO.Variables) and resolved
// in dependency order at group boundaries. They are referenced anywhere placeholders resolve as
// ((var.NAME)) / ((var.NAME.path)), and ((self)) inside a definition means the declaring service's
// own response body. Resolved values are type-preserved and returned to the caller.
// ============================================================================

const (
	// reservedServiceNameVar / reservedServiceNameSelf cannot be used as service names because they
	// are the placeholder namespaces for the variable registry and self-reference.
	reservedServiceNameVar  = "var"
	reservedServiceNameSelf = "self"
)

var (
	// variableNameRegex validates a variable name (registry key).
	variableNameRegex = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	// varRefFirstSegmentRegex captures the first identifier right after "((" (handles "(((var" too,
	// where the outer paren belongs to a function call like formatPhone(((var.x)))).
	varRefFirstSegmentRegex = regexp.MustCompile(`\(\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	// varRefVarNameRegex captures the variable name in a ((var.NAME...)) reference.
	varRefVarNameRegex = regexp.MustCompile(`\(\(\s*var\.([A-Za-z0-9_]+)`)
)

// isReservedServiceName reports whether name collides with a variable namespace (var/self).
func isReservedServiceName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == reservedServiceNameVar || n == reservedServiceNameSelf
}

// isValidVariableName reports whether name is a legal registry key.
func isValidVariableName(name string) bool {
	return variableNameRegex.MatchString(name)
}

// isEmptyVar reports whether a resolved value should be treated as empty for the merge rule
// (empty never overwrites a non-empty value).
func isEmptyVar(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	default:
		return false
	}
}

// singleDoubleParenPlaceholder reports whether expr is exactly one ((...)) placeholder with no other
// content (no nested placeholders, no || fallback). Such definitions are resolved raw so full-body
// object/array references preserve their type instead of collapsing to the string pipeline's "true".
func singleDoubleParenPlaceholder(expr string) (string, bool) {
	if len(expr) > 4 && strings.HasPrefix(expr, "((") && strings.HasSuffix(expr, "))") {
		inner := expr[2 : len(expr)-2]
		if !strings.Contains(inner, "((") && !strings.Contains(inner, "))") && !strings.Contains(inner, "||") {
			return strings.TrimSpace(inner), true
		}
	}
	return "", false
}

// scanVariableRefs extracts the service names, variable names, and self-reference flag that a
// definition expression depends on. It is intentionally an over-scan of ((...)) tokens (including
// those embedded in {{...}} arguments), which is exactly what dependency staging needs.
func scanVariableRefs(expr string) (refServices []string, refVars []string, refSelf bool) {
	seenSvc := make(map[string]bool)
	seenVar := make(map[string]bool)

	for _, m := range varRefFirstSegmentRegex.FindAllStringSubmatch(expr, -1) {
		seg := m[1]
		switch seg {
		case reservedServiceNameSelf:
			refSelf = true
		case reservedServiceNameVar, "item":
			// var handled below; item is fan-out-only and never a variable dependency
		default:
			if !seenSvc[seg] {
				seenSvc[seg] = true
				refServices = append(refServices, seg)
			}
		}
	}

	for _, m := range varRefVarNameRegex.FindAllStringSubmatch(expr, -1) {
		name := m[1]
		if !seenVar[name] {
			seenVar[name] = true
			refVars = append(refVars, name)
		}
	}
	return refServices, refVars, refSelf
}

// collectVariableDefinitions parses the "variables" block from a service's additional_config and
// registers each entry in the sequence registry. Invalid entries are logged and skipped.
func (s *SequenceService) collectVariableDefinitions(ctx context.Context, masterDTO *common_dto.MasterDTO, serviceID int, serviceName string, additionalConfig []byte) {
	if masterDTO == nil || masterDTO.Variables == nil || len(additionalConfig) == 0 {
		return
	}
	log := s.getLog(ctx)

	var ac map[string]interface{}
	if err := json.Unmarshal(additionalConfig, &ac); err != nil {
		return
	}
	raw, ok := ac["variables"]
	if !ok || raw == nil {
		raw, ok = ac["Variables"]
	}
	if !ok || raw == nil {
		return
	}
	varsMap, ok := raw.(map[string]interface{})
	if !ok {
		log.Warnw("variables block is not a JSON object; ignored", "serviceID", serviceID, "serviceName", serviceName)
		return
	}

	for name, v := range varsMap {
		expr, ok := v.(string)
		if !ok {
			log.Warnw("variable value is not a string expression; ignored", "serviceID", serviceID, "variable", name)
			continue
		}
		if !isValidVariableName(name) {
			log.Warnw("invalid variable name; ignored (allowed characters: A-Z a-z 0-9 _)", "serviceID", serviceID, "variable", name)
			continue
		}
		refServices, refVars, refSelf := scanVariableRefs(expr)
		masterDTO.Variables.AddDefinition(&common_dto.VariableDefinition{
			Name:           name,
			Expression:     expr,
			OwnerServiceID: serviceID,
			RefServices:    refServices,
			RefVars:        refVars,
			RefSelf:        refSelf,
			ReadyGroup:     -1,
		})
		log.Debugw("Registered variable definition",
			"serviceID", serviceID, "variable", name,
			"refServices", refServices, "refVars", refVars, "refSelf", refSelf)
	}
}

// computeVariableReadyGroups sets, for each definition, the earliest group index at which every
// service it references is terminal (-1 means it depends on no service and is eligible before group 0).
// Variable-to-variable dependencies are handled by iterating within a stage, not here.
func (s *SequenceService) computeVariableReadyGroups(masterDTO *common_dto.MasterDTO) {
	if masterDTO == nil || masterDTO.Variables == nil || !masterDTO.Variables.HasDefinitions() {
		return
	}

	nameToGroup := make(map[string]int)
	idToGroup := make(map[int]int)
	for groupIndex, group := range masterDTO.SequenceArray {
		for _, serviceID := range group {
			idToGroup[serviceID] = groupIndex
			if svc, ok := masterDTO.ServiceMap[serviceID]; ok && svc.ServiceName != "" {
				nameToGroup[svc.ServiceName] = groupIndex
			}
		}
	}

	for _, d := range masterDTO.Variables.Definitions() {
		ready := -1
		if d.RefSelf {
			if g, ok := idToGroup[d.OwnerServiceID]; ok && g > ready {
				ready = g
			}
		}
		for _, svcName := range d.RefServices {
			if g, ok := nameToGroup[svcName]; ok && g > ready {
				ready = g
			}
			// Unknown service name contributes nothing: the reference resolves empty, so we do not
			// block the variable on a service that is not part of this sequence.
		}
		d.ReadyGroup = ready
	}
}

// buildVariableResolutionScope builds the serviceNameMap used while resolving a definition:
// all terminal services by name, "self" bound to the declaring service (regardless of its status),
// and "var" bound to the current registry snapshot so a variable can reference earlier variables.
func (s *SequenceService) buildVariableResolutionScope(masterDTO *common_dto.MasterDTO, owner *models.EsaLog) map[string]*models.EsaLog {
	m := make(map[string]*models.EsaLog)
	for _, svc := range masterDTO.EsaServices {
		if shouldIncludeForServicePlaceholderResolution(svc) {
			m[svc.ServiceName] = svc
		}
	}
	if owner != nil {
		m[reservedServiceNameSelf] = owner
	}
	if masterDTO.Variables != nil {
		if snap := masterDTO.Variables.Snapshot(); len(snap) > 0 {
			m[reservedServiceNameVar] = &models.EsaLog{
				Response: models.ResponseDetails{Body: snap, StatusCode: 200},
			}
		}
	}
	return m
}

// resolveVariableValue evaluates a single definition and returns its typed value. A definition that
// is exactly one ((...)) placeholder is resolved raw (type-preserving, so full-body objects survive);
// everything else runs through the standard typed placeholder pipeline used by request bodies.
func (s *SequenceService) resolveVariableValue(ctx context.Context, masterDTO *common_dto.MasterDTO, d *common_dto.VariableDefinition) interface{} {
	owner := masterDTO.ServiceMap[d.OwnerServiceID]
	serviceNameMap := s.buildVariableResolutionScope(masterDTO, owner)

	expr := strings.TrimSpace(d.Expression)
	if inner, ok := singleDoubleParenPlaceholder(expr); ok {
		return s.expressionProcessor.getServiceResponseValueRaw(inner, serviceNameMap)
	}

	placeholderCache := make(map[string]string)
	return s.expressionProcessor.ProcessPlaceholdersWithTypeCtx(ctx, expr, TypeModeTyped, masterDTO, serviceNameMap, placeholderCache, nil)
}

// refVarsSettled reports whether all variables referenced by d have been evaluated. An undefined
// referenced variable is treated as settled (it resolves empty and must not block dependents).
func (s *SequenceService) refVarsSettled(reg *common_dto.VariableRegistry, d *common_dto.VariableDefinition) bool {
	if len(d.RefVars) == 0 {
		return true
	}
	defs := reg.Definitions()
	for _, rv := range d.RefVars {
		anyDefined := false
		allResolved := true
		for _, dd := range defs {
			if dd.Name == rv {
				anyDefined = true
				if !dd.Resolved {
					allResolved = false
				}
			}
		}
		if anyDefined && !allResolved {
			return false
		}
	}
	return true
}

// resolveVariablesStage resolves every not-yet-resolved definition whose referenced services are
// terminal (ReadyGroup <= uptoGroup) and whose referenced variables are settled. It iterates until
// no further progress so a chain of variable-to-variable dependencies settles within one stage.
//
// Merge policy (deterministic): definitions are processed in ascending owner-service-id order so a
// same-stage collision has a stable "last writer". An empty resolution never overwrites a non-empty
// value; a non-empty overwrite of a non-empty value is last-writer-wins and is warned; empty writes
// are debug-logged.
func (s *SequenceService) resolveVariablesStage(ctx context.Context, masterDTO *common_dto.MasterDTO, uptoGroup int) {
	if masterDTO == nil || masterDTO.Variables == nil || !masterDTO.Variables.HasDefinitions() {
		return
	}
	reg := masterDTO.Variables
	log := s.getLog(ctx)

	defs := reg.Definitions()
	ordered := make([]*common_dto.VariableDefinition, len(defs))
	copy(ordered, defs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].OwnerServiceID < ordered[j].OwnerServiceID
	})

	for {
		progress := false
		for _, d := range ordered {
			if d.Resolved || d.ReadyGroup > uptoGroup {
				continue
			}
			if !s.refVarsSettled(reg, d) {
				continue
			}

			value := s.resolveVariableValue(ctx, masterDTO, d)
			d.Resolved = true
			progress = true

			if isEmptyVar(value) {
				log.Debugw("Variable resolved empty; not written",
					"variable", d.Name, "ownerServiceID", d.OwnerServiceID)
				continue
			}
			if prev, ok := reg.Get(d.Name); ok && !isEmptyVar(prev) {
				log.Warnw("Variable overwritten by a later definition (last-writer-wins)",
					"variable", d.Name, "ownerServiceID", d.OwnerServiceID,
					"previousValue", truncateForLog(formatVarForLog(prev), maxLogExpressionLen),
					"newValue", truncateForLog(formatVarForLog(value), maxLogExpressionLen))
			}
			reg.Set(d.Name, value)
			log.Infow("Variable resolved",
				"variable", d.Name, "ownerServiceID", d.OwnerServiceID, "stageUptoGroup", uptoGroup)
		}
		if !progress {
			break
		}
	}
}

// logUnresolvedVariables warns about definitions that were never evaluated (missing dependency,
// referenced service that never ran, or a reference cycle). Called once after execution completes.
func (s *SequenceService) logUnresolvedVariables(ctx context.Context, masterDTO *common_dto.MasterDTO) {
	if masterDTO == nil || masterDTO.Variables == nil || !masterDTO.Variables.HasDefinitions() {
		return
	}
	log := s.getLog(ctx)
	for _, d := range masterDTO.Variables.Definitions() {
		if !d.Resolved {
			log.Warnw("Variable definition unresolved and dropped (missing dependency, referenced service did not run, or dependency cycle)",
				"variable", d.Name, "ownerServiceID", d.OwnerServiceID,
				"refServices", d.RefServices, "refVars", d.RefVars, "refSelf", d.RefSelf)
		}
	}
}

// addVarPseudoService injects the variable registry as a synthetic "var" service into a consumer's
// serviceNameMap, so ((var.NAME)) / ((var.NAME.path)) resolve through the existing service-placeholder
// machinery (type-preserving, nested paths, etc.). No-op when no variables are resolved.
func addVarPseudoService(serviceNameMap map[string]*models.EsaLog, masterDTO *common_dto.MasterDTO) {
	if serviceNameMap == nil || masterDTO == nil || masterDTO.Variables == nil {
		return
	}
	snap := masterDTO.Variables.Snapshot()
	if len(snap) == 0 {
		return
	}
	serviceNameMap[reservedServiceNameVar] = &models.EsaLog{
		Response: models.ResponseDetails{Body: snap, StatusCode: 200},
	}
}

// resolveVarsInPlugTemplate returns a deep copy of a plug_response_into template with every ((...))
// placeholder resolved from serviceNameMap (which includes the "var" pseudo-service). It walks maps,
// slices, and strings; it deliberately touches only the (( )) delimiter so the <Actual_response> /
// <Actual_response_json> plug markers (a different delimiter) are left intact for plugWalk.
func (s *SequenceService) resolveVarsInPlugTemplate(node interface{}, serviceNameMap map[string]*models.EsaLog) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = s.resolveVarsInPlugTemplate(val, serviceNameMap)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = s.resolveVarsInPlugTemplate(val, serviceNameMap)
		}
		return out
	case string:
		return s.resolveDoubleParenPlaceholders(v, serviceNameMap)
	default:
		return v
	}
}

// resolveDoubleParenPlaceholders resolves ((...)) placeholders in str. If str is exactly one ((...))
// placeholder, the raw typed value is returned (type preserved, e.g. objects/arrays/numbers); otherwise
// each ((...)) occurrence is replaced by its string form. Other delimiters (<...>, {{...}}) are left
// untouched, so plug markers such as <Actual_response> are never affected.
func (s *SequenceService) resolveDoubleParenPlaceholders(str string, serviceNameMap map[string]*models.EsaLog) interface{} {
	if !strings.Contains(str, "((") {
		return str
	}
	if inner, ok := singleDoubleParenPlaceholder(str); ok {
		return s.expressionProcessor.getServiceResponseValueRaw(inner, serviceNameMap)
	}
	return s.expressionProcessor.doubleParenthesesRegex.ReplaceAllStringFunc(str, func(match string) string {
		inner := strings.TrimSpace(match[2 : len(match)-2])
		return s.expressionProcessor.getServiceResponseValue(inner, serviceNameMap, nil)
	})
}

// formatVarForLog renders a resolved variable value compactly for log lines.
func formatVarForLog(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}
