package sequence_service

import "testing"

// Hardening tests for the ARRAY transform spec parser. Each case here corresponds to a defect found
// in review; they are separate from expression_processor_arraydefault_test.go so the intent stays
// obvious.

// A lowercase @type must be treated the same by BOTH the source-value path (applyTypeConversion)
// and the default path (convertDefaultForType). Previously convertDefaultForType uppercased the
// annotation while applyTypeConversion did not, so one field emitted a string for populated
// elements and a number for defaulted ones.
func TestParseTransformMap_TypeAnnotationCaseIsConsistent(t *testing.T) {
	ep := NewExpressionProcessor()

	// Uppercase: both paths convert.
	upper := ep.transformArray([]interface{}{
		map[string]interface{}{"D__c": "7"},
		map[string]interface{}{"D__c": nil},
	}, ep.parseTransformMap("D__c->v@NUMERIC??-999"), true)
	if got := mustItem(t, upper, 0)["v"]; got != 7 {
		t.Fatalf("@NUMERIC populated: expected number 7, got %v (%T)", got, got)
	}
	if got := mustItem(t, upper, 1)["v"]; got != -999 {
		t.Fatalf("@NUMERIC default: expected number -999, got %v (%T)", got, got)
	}

	// Lowercase: neither path converts. The point is that both elements share one JSON type.
	lower := ep.transformArray([]interface{}{
		map[string]interface{}{"D__c": "7"},
		map[string]interface{}{"D__c": nil},
	}, ep.parseTransformMap("D__c->v@numeric??-999"), true)
	if got := mustItem(t, lower, 0)["v"]; got != "7" {
		t.Fatalf("@numeric populated: expected string \"7\", got %v (%T)", got, got)
	}
	if got := mustItem(t, lower, 1)["v"]; got != "-999" {
		t.Fatalf("@numeric default: expected string \"-999\" (same type as the populated element), got %v (%T)", got, got)
	}
}

// `??` with nothing after it is a typo, not a default. It must not inject an empty string, must not
// leak the `??` into the target key, and must not corrupt the @TYPE annotation.
func TestSplitMappingDefault_EmptyDefaultIsNotADefault(t *testing.T) {
	ep := NewExpressionProcessor()

	for _, spec := range []string{"D__c->v@NUMERIC??", "D__c->v??", "D__c->v@NUMERIC??   "} {
		parsed := ep.parseTransformMap(spec)
		if _, hasDefault := parsed.defaults["v"]; hasDefault {
			t.Fatalf("%s: expected no default to be registered, got %+v", spec, parsed.defaults["v"])
		}
		if parsed.fields["D__c"] != "v" {
			t.Fatalf("%s: expected target key \"v\" (the ?? must not leak into it), got %v", spec, parsed.fields)
		}
	}

	// The @TYPE annotation must survive the stripped `??` and still convert.
	typed := ep.parseTransformMap("D__c->v@NUMERIC??")
	if typed.types["v"] != "NUMERIC" {
		t.Fatalf(`expected type "NUMERIC" to survive an empty ??, got %q`, typed.types["v"])
	}
	populated := ep.transformArray([]interface{}{map[string]interface{}{"D__c": "7"}}, typed, true)
	if got := mustItem(t, populated, 0)["v"]; got != 7 {
		t.Fatalf("expected @NUMERIC still applied after an empty ??, got %v (%T)", got, got)
	}

	// A blank source with an empty `??` keeps the no-default behaviour: null stays null.
	blank := ep.transformArray([]interface{}{map[string]interface{}{"D__c": nil}},
		ep.parseTransformMap("D__c->v@NUMERIC??"), true)
	if got := mustItem(t, blank, 0)["v"]; got != nil {
		t.Fatalf("expected null (no default) for an empty ??, got %v (%T)", got, got)
	}

	// ??'' is the explicit way to ask for an empty-string default.
	explicit := ep.transformArray([]interface{}{map[string]interface{}{"D__c": nil}},
		ep.parseTransformMap("D__c->v??''"), true)
	if got := mustItem(t, explicit, 0)["v"]; got != "" {
		t.Fatalf(`expected "" from ??'' , got %v (%T)`, got, got)
	}
}

// Static literal injection accepts both quote styles, matching splitTransformPairs and
// parseDefaultLiteral which already treat " as a quote. Previously a double-quoted literal was read
// as a field name and silently emitted null.
func TestParseTransformMap_StaticLiteralAcceptsBothQuoteStyles(t *testing.T) {
	ep := NewExpressionProcessor()

	for _, spec := range []string{`'00'->code,Type__c->key`, `"00"->code,Type__c->key`} {
		parsed := ep.parseTransformMap(spec)
		if parsed.statics["code"] != "00" {
			t.Fatalf("%s: expected static code=00, got statics=%v fields=%v", spec, parsed.statics, parsed.fields)
		}
		if _, isField := parsed.fields[`"00"`]; isField {
			t.Fatalf("%s: a quoted literal must not be treated as a field, got %v", spec, parsed.fields)
		}
	}

	// With @TYPE, and with a comma inside the double-quoted literal.
	typed := ep.parseTransformMap(`"00"->code@STRING,Type__c->key`)
	if typed.statics["code"] != "00" || typed.types["code"] != "STRING" {
		t.Fatalf("expected code=00 @STRING, got statics=%v types=%v", typed.statics, typed.types)
	}

	comma := ep.parseTransformMap(`"a,b"->tag,Type__c->key`)
	if comma.statics["tag"] != "a,b" {
		t.Fatalf(`expected static tag="a,b" to survive the comma split, got %v`, comma.statics)
	}
	if comma.fields["Type__c"] != "key" {
		t.Fatalf("expected the sibling mapping to survive, got %v", comma.fields)
	}
}
