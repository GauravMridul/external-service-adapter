package sequence_service

import "testing"

// TestDocParity_ArrayDefaultTables asserts that every row of the two example tables in
// ESA_Guide.md §12.3.4 ("Cross-type examples" and "Using ?? as a literal value") matches the
// implementation. If a doc row and the code ever disagree, this test fails rather than the
// documentation quietly going stale.
func TestDocParity_ArrayDefaultTables(t *testing.T) {
	ep := NewExpressionProcessor()

	// ── §12.3.4 "Default typing across data types" + "Cross-type examples" ──────────────
	// Each spec is evaluated against an element whose source field is null, so the default fires.
	defaultRows := []struct {
		spec string
		key  string
		want interface{}
	}{
		// Typing rules table
		{"D__c->v@NUMERIC??null", "v", nil},
		{"D__c->v@NUMERIC??-999", "v", -999},
		{"D__c->v@NUMERIC??'N/A'", "v", "N/A"},
		{"D__c->v??-999", "v", "-999"},

		// Cross-type examples table
		{"Decile__c->value@NUMERIC??-999", "value", -999},
		{"Decile__c->value@NUMERIC??'N/A'", "value", "N/A"},
		{"Decile__c->value@NUMERIC??UNKNOWN", "value", "UNKNOWN"},
		{"Decile__c->value@NUMERIC??'-999'", "value", -999},
		{"Verified__c->flag@BOOLEAN??true", "flag", true},
		{"Verified__c->flag@BOOLEAN??-999", "flag", -999},
		{"Verified__c->flag@BOOLEAN??'N/A'", "flag", "N/A"},
		{"Verified__c->flag@BOOLEAN??'-999'", "flag", "-999"},
		{"Name__c->name@STRING??-999", "name", "-999"},

		// "Default literal forms" prose: quoting does not force a string under @NUMERIC.
		{"D__c->v@NUMERIC??\"-999\"", "v", -999},
		// @BOOLEAN accepted tokens listed in the guide.
		{"D__c->v@BOOLEAN??yes", "v", true},
		{"D__c->v@BOOLEAN??no", "v", false},
		{"D__c->v@BOOLEAN??on", "v", true},
		{"D__c->v@BOOLEAN??off", "v", false},
		{"D__c->v@BOOLEAN??enabled", "v", true},
		{"D__c->v@BOOLEAN??disabled", "v", false},
		{"D__c->v@BOOLEAN??1", "v", true},
		{"D__c->v@BOOLEAN??0", "v", false},
		// ??'null' is the string, not JSON null.
		{"D__c->v@STRING??'null'", "v", "null"},
	}

	for _, row := range defaultRows {
		t.Run("default/"+row.spec, func(t *testing.T) {
			// A single element whose every candidate source field is null.
			element := map[string]interface{}{
				"D__c": nil, "Decile__c": nil, "Verified__c": nil, "Name__c": nil,
			}
			out := ep.transformArray([]interface{}{element}, ep.parseTransformMap(row.spec), true)
			got := mustItem(t, out, 0)[row.key]
			if got != row.want {
				t.Fatalf("guide says %s -> %v (%T), code gives %v (%T)",
					row.spec, row.want, row.want, got, got)
			}
		})
	}

	// ── §12.3.4 "Using ?? as a literal value" ──────────────────────────────────────────
	literalRows := []struct {
		name string
		spec string
		// exactly one of these is checked
		wantStatic  string
		wantDefault string
		key         string
	}{
		{"operator", "Decile__c->value??-999", "", "-999", "value"},
		{"quoted ?? is a static literal", "'??'->tag", "??", "", "tag"},
		{"static literal containing ??", "'N/A??'->status", "N/A??", "", "status"},
		{"default is the literal ??", "Note__c->note??'??'", "", "??", "note"},
		{"quoted comma and ?? stay one pair", "'a,b??c'->tag", "a,b??c", "", "tag"},
	}

	for _, row := range literalRows {
		t.Run("literal/"+row.name, func(t *testing.T) {
			spec := ep.parseTransformMap(row.spec)
			if row.wantStatic != "" {
				if got := spec.statics[row.key]; got != row.wantStatic {
					t.Fatalf("guide says %s injects static %q, code gives %q (statics=%v)",
						row.spec, row.wantStatic, got, spec.statics)
				}
				if _, isDefault := spec.defaults[row.key]; isDefault {
					t.Fatalf("guide says %s is NOT a default, code registered one: %+v",
						row.spec, spec.defaults[row.key])
				}
				return
			}
			got, ok := spec.defaults[row.key]
			if !ok || got.text != row.wantDefault {
				t.Fatalf("guide says %s defaults to %q, code gives %+v (present=%v)",
					row.spec, row.wantDefault, got, ok)
			}
		})
	}

	// ── §12.3.4 caveat: ?? on a static literal or index token is ignored ────────────────
	ignored := ep.parseTransformMap("'00'->code??X,__rownum__->key??Y")
	if len(ignored.defaults) != 0 {
		t.Fatalf("guide says ?? on a static literal / index token is ignored, code registered %v",
			ignored.defaults)
	}
	if ignored.statics["code"] != "00" {
		t.Fatalf("expected static literal code=00 to survive, got %v", ignored.statics)
	}
	if base, ok := ignored.indexes["key"]; !ok || base != 1 {
		t.Fatalf("expected __rownum__ injection to survive, got indexes=%v", ignored.indexes)
	}

	// ── §12.3.4 behaviour table: what triggers the default ──────────────────────────────
	triggers := []struct {
		name    string
		element map[string]interface{}
		want    interface{}
	}{
		{"null", map[string]interface{}{"D__c": nil}, -999},
		{"absent key", map[string]interface{}{}, -999},
		{"empty string", map[string]interface{}{"D__c": ""}, -999},
		{"whitespace only", map[string]interface{}{"D__c": "   "}, -999},
		{"populated", map[string]interface{}{"D__c": "7"}, 7},
	}
	for _, tr := range triggers {
		t.Run("trigger/"+tr.name, func(t *testing.T) {
			out := ep.transformArray([]interface{}{tr.element},
				ep.parseTransformMap("D__c->v@NUMERIC??-999"), true)
			if got := mustItem(t, out, 0)["v"]; got != tr.want {
				t.Fatalf("source %v: guide says %v, code gives %v", tr.element, tr.want, got)
			}
		})
	}

	// ── §12.7 note: default conversion is STRICTER than source-value conversion ─────────
	// Same literal, same @TYPE: as a source value it is cast away, as a default it survives.
	sourceOut := ep.transformArray(
		[]interface{}{map[string]interface{}{"D__c": "N/A"}},
		ep.parseTransformMap("D__c->v@NUMERIC"), true)
	if got := mustItem(t, sourceOut, 0)["v"]; got != nil {
		t.Fatalf("guide says a non-numeric SOURCE value becomes null under @NUMERIC, got %v", got)
	}
	defaultOut := ep.transformArray(
		[]interface{}{map[string]interface{}{"D__c": nil}},
		ep.parseTransformMap("D__c->v@NUMERIC??'N/A'"), true)
	if got := mustItem(t, defaultOut, 0)["v"]; got != "N/A" {
		t.Fatalf(`guide says a non-numeric DEFAULT survives as "N/A" under @NUMERIC, got %v`, got)
	}
}
