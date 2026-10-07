package sequence_service

import (
	"encoding/json"
	"strconv"
	"strings"

	"esa/internal/app/dto/common_dto"
	"esa/internal/app/models"
)

const fanOutCallLogsKey = "fan_out_call_logs"

// ExtraFields keys for fan-out Mongo docs (serviceName unchanged; these distinguish aggregate vs per-call).
const (
	fanOutRoleKey       = "fanOutRole"       // "aggregate" | "per_call"
	fanOutCallIndexKey  = "fanOutCallIndex"  // 0-based for per-call; omit for aggregate
	fanOutLogLabelKey   = "fanOutLogLabel"   // e.g. "ServiceName (merged)" or "ServiceName (1 of 3)"
)

// getFanOutConfigFromEsaLog parses additional_config from esaLog and returns FanOutFromArrayConfig if enabled.
func getFanOutConfigFromEsaLog(esaLog *models.EsaLog) *FanOutFromArrayConfig {
	if esaLog == nil || esaLog.ExtraFields == nil {
		return nil
	}
	raw, ok := esaLog.ExtraFields["additional_config"]
	if !ok {
		return nil
	}
	var jsonData []byte
	switch v := raw.(type) {
	case string:
		jsonData = []byte(v)
	case []byte:
		jsonData = v
	default:
		return nil
	}
	var ac AdditionalConfig
	if err := json.Unmarshal(jsonData, &ac); err != nil {
		return nil
	}
	if ac.FanOutFromArray == nil || !ac.FanOutFromArray.Enabled {
		return nil
	}
	return ac.FanOutFromArray
}

// getSourceEsaLog returns the source service's EsaLog from masterDTO (by service_id or service_name).
func getSourceEsaLog(masterDTO *common_dto.MasterDTO, src FanOutSource) *models.EsaLog {
	if masterDTO == nil {
		return nil
	}
	if src.ServiceID > 0 {
		if log, ok := masterDTO.ServiceMap[src.ServiceID]; ok {
			return log
		}
	}
	if src.ServiceName != "" {
		for _, log := range masterDTO.EsaServices {
			if log.ServiceName == src.ServiceName {
				return log
			}
		}
	}
	return nil
}

// getArrayFromSourceResponse extracts the array at arrayPath from the source service's response body.
func getArrayFromSourceResponse(sourceLog *models.EsaLog, arrayPath string) []interface{} {
	if sourceLog == nil || sourceLog.Response.Body == nil {
		return nil
	}
	if arrayPath == "" {
		arrayPath = "result"
	}
	pathParts := strings.Split(arrayPath, ".")
	current := interface{}(sourceLog.Response.Body)
	for _, part := range pathParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		var found bool
		for k, v := range m {
			if strings.EqualFold(strings.TrimSpace(k), part) {
				current = v
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	arr, ok := current.([]interface{})
	if !ok {
		return nil
	}
	return arr
}

func valueToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		if val {
			return "true"
		}
		return "false"
	}
	if b, err := json.Marshal(v); err == nil {
		s := string(b)
		if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
			return s[1 : len(s)-1]
		}
		return s
	}
	return ""
}

// itemMatchesCondition returns true if the item satisfies the condition (field op value).
func itemMatchesCondition(item map[string]interface{}, c FanOutFilterCondition) bool {
	val, exists := item[c.Field]
	if !exists {
		fieldLower := strings.ToLower(c.Field)
		for k, v := range item {
			if strings.ToLower(k) == fieldLower {
				val = v
				exists = true
				break
			}
		}
	}
	if !exists {
		return false
	}
	itemStr := valueToString(val)
	expectStr := valueToString(c.Value)
	switch strings.ToLower(strings.TrimSpace(c.Op)) {
	case "eq", "==":
		return itemStr == expectStr
	case "ne", "!=":
		return itemStr != expectStr
	case "in":
		if arr, ok := c.Value.([]interface{}); ok {
			for _, v := range arr {
				if valueToString(v) == itemStr {
					return true
				}
			}
			return false
		}
		return itemStr == expectStr
	case "not_in":
		if arr, ok := c.Value.([]interface{}); ok {
			for _, v := range arr {
				if valueToString(v) == itemStr {
					return false
				}
			}
			return true
		}
		return itemStr != expectStr
	default:
		return itemStr == expectStr
	}
}

// itemMatchesConditionTree evaluates a condition tree node (and/or group or leaf) against an item.
// Empty "and" is treated as true; empty "or" as false.
func itemMatchesConditionTree(item map[string]interface{}, node *FanOutConditionNode) bool {
	if node == nil {
		return true
	}
	if len(node.And) > 0 {
		for i := range node.And {
			if !itemMatchesConditionTree(item, &node.And[i]) {
				return false
			}
		}
		return true
	}
	if len(node.Or) > 0 {
		for i := range node.Or {
			if itemMatchesConditionTree(item, &node.Or[i]) {
				return true
			}
		}
		return false
	}
	// Leaf: same semantics as include/exclude conditions
	return itemMatchesCondition(item, FanOutFilterCondition{Field: node.Field, Op: node.Op, Value: node.Value})
}

// applyFanOutFilter returns items that pass include (AND) or are not excluded by exclude (OR),
// or that match the condition tree when filter.condition is set.
func applyFanOutFilter(items []interface{}, filter *FanOutFilter) []interface{} {
	if filter == nil {
		return items
	}
	var out []interface{}
	for _, it := range items {
		item, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		if filter.Condition != nil {
			if !itemMatchesConditionTree(item, filter.Condition) {
				continue
			}
			out = append(out, it)
			continue
		}
		if len(filter.Include) > 0 {
			allMatch := true
			for _, c := range filter.Include {
				if !itemMatchesCondition(item, c) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				continue
			}
		}
		if len(filter.Exclude) > 0 {
			excluded := false
			for _, c := range filter.Exclude {
				if itemMatchesCondition(item, c) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}
