package sequence_service

import "testing"

// Verifies the __rownum__ / __index__ transform tokens are parsed into the index map.
func TestParseTransformMap_IndexTokens(t *testing.T) {
	ep := NewExpressionProcessor()

	spec := ep.parseTransformMap("__rownum__->key,Product_Type__c->value")
	if base, ok := spec.indexes["key"]; !ok || base != 1 {
		t.Fatalf("expected __rownum__ to set 1-based index for key, got base=%d ok=%v", base, ok)
	}
	if spec.fields["Product_Type__c"] != "value" {
		t.Fatalf("expected Product_Type__c->value in transform map, got %v", spec.fields)
	}

	spec0 := ep.parseTransformMap("__index__->pos,Name->n")
	if base, ok := spec0.indexes["pos"]; !ok || base != 0 {
		t.Fatalf("expected __index__ to set 0-based index for pos, got base=%d ok=%v", base, ok)
	}
}

// Verifies transformArray injects the row number (1-based) alongside a mapped field,
// producing the analytic_super_category shape: [{key:1,value:...},{key:2,value:...}].
func TestTransformArray_InjectsRowNumber(t *testing.T) {
	ep := NewExpressionProcessor()
	src := []interface{}{
		map[string]interface{}{"Product_Type__c": "Mobile"},
		map[string]interface{}{"Product_Type__c": "Washing machine"},
	}
	out := ep.transformArray(src, ep.parseTransformMap("__rownum__->key,Product_Type__c->value"), true)

	if len(out) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(out))
	}
	first := out[0].(map[string]interface{})
	if first["key"] != 1 || first["value"] != "Mobile" {
		t.Fatalf("expected {key:1,value:Mobile}, got %v", first)
	}
	second := out[1].(map[string]interface{})
	if second["key"] != 2 || second["value"] != "Washing machine" {
		t.Fatalf("expected {key:2,value:Washing machine}, got %v", second)
	}
}

// Verifies the count operation returns the element count.
func TestApplyArrayOperation_Count(t *testing.T) {
	ep := NewExpressionProcessor()
	src := []interface{}{
		map[string]interface{}{},
		map[string]interface{}{},
		map[string]interface{}{},
	}
	res := ep.applyArrayOperation(src, "count", nil)
	if len(res) != 1 {
		t.Fatalf("expected single-element result, got %d", len(res))
	}
	if n, ok := res[0].(int); !ok || n != 3 {
		t.Fatalf("expected count 3, got %v", res[0])
	}

	empty := ep.applyArrayOperation([]interface{}{}, "count", nil)
	if len(empty) != 1 || empty[0].(int) != 0 {
		t.Fatalf("expected count 0 for empty array, got %v", empty)
	}
}

// Verifies __rownum__/__index__ default to a JSON number, and @STRING casts to a string.
// (Documents the caveat that these tokens differ from ordinary transformed fields, which default to string.)
func TestTransformArray_IndexTypeDefaultAndStringCast(t *testing.T) {
	ep := NewExpressionProcessor()
	src := []interface{}{map[string]interface{}{"Name": "A"}}

	// Default: number
	out := ep.transformArray(src, ep.parseTransformMap("__rownum__->key,Name->value"), true)
	if _, ok := out[0].(map[string]interface{})["key"].(int); !ok {
		t.Fatalf("expected __rownum__ default to be int (JSON number), got %T", out[0].(map[string]interface{})["key"])
	}

	// @STRING: string
	out2 := ep.transformArray(src, ep.parseTransformMap("__rownum__->key@STRING,Name->value"), true)
	if v, ok := out2[0].(map[string]interface{})["key"].(string); !ok || v != "1" {
		t.Fatalf("expected __rownum__@STRING to be string \"1\", got %v (%T)", out2[0].(map[string]interface{})["key"], out2[0].(map[string]interface{})["key"])
	}
}

// Verifies that in `transform` mode (onlyTransformed=false) an injected index key is NOT
// overwritten by a same-named original field on the element (the documented caveat, fixed in code).
func TestTransformArray_InjectedIndexKeyWinsOverOriginal(t *testing.T) {
	ep := NewExpressionProcessor()
	src := []interface{}{
		map[string]interface{}{"id_index": "ORIGINAL", "Name": "A"},
	}
	// transform (not transform-only): copies through other keys, but must not clobber id_index.
	out := ep.transformArray(src, ep.parseTransformMap("__rownum__->id_index,Name->value"), false)

	item := out[0].(map[string]interface{})
	if item["id_index"] != 1 {
		t.Fatalf("expected injected id_index=1 to win over original 'ORIGINAL', got %v", item["id_index"])
	}
	if item["value"] != "A" {
		t.Fatalf("expected value=A, got %v", item["value"])
	}
}

// Verifies chained "merge:count" renders the count as a scalar (not __ARRAY_PARSE__:[N]).
// This is the pre-existing limitation fixed by keying scalar rendering on the effective operation.
func TestEvaluateArrayExpression_MergeThenCountScalar(t *testing.T) {
	ep := NewExpressionProcessor()

	// Two literal arrays (2 + 1 = 3 elements) merged, then counted.
	got := ep.evaluateArrayExpression(`[{},{}],[{}]:merge:count`, nil, nil)
	if got != "3" {
		t.Fatalf("expected merge:count scalar \"3\", got %q", got)
	}

	// Plain count still works.
	if g := ep.evaluateArrayExpression(`[{},{},{}]:count`, nil, nil); g != "3" {
		t.Fatalf("expected count scalar \"3\", got %q", g)
	}
}
