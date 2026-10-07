package sequence_service

import (
	"encoding/json"
	"testing"
)

// These tests cover the v2.4.0 per-element default suffix `??` on ARRAY mapping pairs
// (ESA_Guide §12.3.2). They exercise parseTransformMap / transformArray directly so the
// substitution and the @TYPE interaction can be asserted without a DB fixture.

func mustItem(t *testing.T, out []interface{}, index int) map[string]interface{} {
	t.Helper()
	if index >= len(out) {
		t.Fatalf("expected at least %d elements, got %d", index+1, len(out))
	}
	item, ok := out[index].(map[string]interface{})
	if !ok {
		t.Fatalf("expected element %d to be an object, got %T", index, out[index])
	}
	return item
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// Verifies the default literal is peeled off the mapping pair and normalised, and that the
// left-hand spec (source path, target key, @TYPE) still parses exactly as before.
func TestParseTransformMap_ParsesDefaults(t *testing.T) {
	ep := NewExpressionProcessor()

	spec := ep.parseTransformMap("Type__c->key,Decile__c->value@NUMERIC??-999")
	if spec.fields["Type__c"] != "key" {
		t.Fatalf("expected Type__c->key, got %v", spec.fields)
	}
	if spec.fields["Decile__c"] != "value" {
		t.Fatalf("expected Decile__c->value, got %v", spec.fields)
	}
	if spec.types["value"] != "NUMERIC" {
		t.Fatalf("expected value@NUMERIC to survive the ?? split, got %q", spec.types["value"])
	}
	if got, ok := spec.defaults["value"]; !ok || got.text != "-999" || got.quoted || got.isNull {
		t.Fatalf("expected unquoted default \"-999\" for value, got %+v (present=%v)", got, ok)
	}
	// A pair without ?? must not register a default at all.
	if _, ok := spec.defaults["key"]; ok {
		t.Fatalf("expected no default for key, got %v", spec.defaults["key"])
	}
}

// Verifies the default literal forms from ESA_Guide §12.3.4, including that quoting is recorded
// (it is what pins a cross-type default to a string).
func TestParseDefaultLiteral_Forms(t *testing.T) {
	cases := []struct {
		raw  string
		want transformDefault
	}{
		{"-999", transformDefault{text: "-999"}},
		{"UNKNOWN", transformDefault{text: "UNKNOWN"}},
		{"'N/A, unknown'", transformDefault{text: "N/A, unknown", quoted: true}},
		{`"N/A, unknown"`, transformDefault{text: "N/A, unknown", quoted: true}},
		{"null", transformDefault{isNull: true}},
		{"NULL", transformDefault{isNull: true}},
		{"'null'", transformDefault{text: "null", quoted: true}}, // quoted null is the string
		{"  -999  ", transformDefault{text: "-999"}},
		{"'  padded  '", transformDefault{text: "  padded  ", quoted: true}}, // quotes keep inner space
		{"true", transformDefault{text: "true"}},
	}
	for _, c := range cases {
		if got := parseDefaultLiteral(c.raw); got != c.want {
			t.Fatalf("parseDefaultLiteral(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-type defaults: the declared @TYPE must not throw the sentinel away
// ---------------------------------------------------------------------------

// A default whose literal does not fit the mapping's @TYPE is emitted in its own natural JSON type
// instead of being cast away. Before this, @NUMERIC??'N/A' produced null and @BOOLEAN??'N/A'
// produced false, silently discarding the configured sentinel.
func TestTransformArray_CrossTypeDefaults(t *testing.T) {
	ep := NewExpressionProcessor()

	cases := []struct {
		name string
		spec string
		want interface{}
	}{
		// @NUMERIC with a non-numeric default -> string, not null.
		{"numeric type, quoted string default", "D__c->v@NUMERIC??'N/A'", "N/A"},
		{"numeric type, bare string default", "D__c->v@NUMERIC??UNKNOWN", "UNKNOWN"},
		{"numeric type, string with comma", "D__c->v@NUMERIC??'N/A, unknown'", "N/A, unknown"},
		{"numeric type, boolean default", "D__c->v@NUMERIC??false", false},

		// @BOOLEAN with a non-boolean default -> the literal, not false.
		{"boolean type, quoted string default", "D__c->v@BOOLEAN??'N/A'", "N/A"},
		{"boolean type, bare string default", "D__c->v@BOOLEAN??UNKNOWN", "UNKNOWN"},
		{"boolean type, numeric sentinel", "D__c->v@BOOLEAN??-999", -999},

		// Quoting pins a cross-type default to a string even when it looks convertible.
		{"boolean type, quoted numeric stays string", "D__c->v@BOOLEAN??'-999'", "-999"},

		// @STRING accepts anything, so nothing is cross-type.
		{"string type, numeric default", "D__c->v@STRING??-999", "-999"},
		{"string type, quoted default", "D__c->v@STRING??'N/A'", "N/A"},

		// Compatible combinations are unchanged.
		{"numeric type, numeric default", "D__c->v@NUMERIC??-999", -999},
		{"numeric type, float default", "D__c->v@NUMERIC??-9.5", -9.5},
		{"numeric type, quoted numeric default", "D__c->v@NUMERIC??'-999'", -999},
		{"boolean type, true default", "D__c->v@BOOLEAN??true", true},
		{"boolean type, false default", "D__c->v@BOOLEAN??false", false},
		{"boolean type, 1 default", "D__c->v@BOOLEAN??1", true},
		{"boolean type, 0 default", "D__c->v@BOOLEAN??0", false},

		// null wins over every declared type.
		{"numeric type, null default", "D__c->v@NUMERIC??null", nil},
		{"boolean type, null default", "D__c->v@BOOLEAN??null", nil},
		{"string type, null default", "D__c->v@STRING??null", nil},

		// Untyped mappings keep emitting the literal as a string.
		{"no type, numeric default", "D__c->v??-999", "-999"},
		{"no type, boolean default", "D__c->v??true", "true"},
		{"no type, string default", "D__c->v??UNKNOWN", "UNKNOWN"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := ep.transformArray(
				[]interface{}{map[string]interface{}{"D__c": nil}},
				ep.parseTransformMap(c.spec), true)
			got := mustItem(t, out, 0)["v"]
			if got != c.want {
				t.Fatalf("%s: got %v (%T), want %v (%T)", c.spec, got, got, c.want, c.want)
			}
		})
	}
}

// A cross-type default must not disturb the SOURCE value's own @TYPE conversion: a populated
// numeric field still becomes a JSON number even when the default is a string sentinel.
func TestTransformArray_CrossTypeDefaultLeavesSourceTypingAlone(t *testing.T) {
	ep := NewExpressionProcessor()

	source := []interface{}{
		map[string]interface{}{"Type__c": "MSME_APP", "Decile__c": "7"},
		map[string]interface{}{"Type__c": "RETAIL_APP", "Decile__c": nil},
	}
	out := ep.transformArray(source, ep.parseTransformMap("Type__c->key,Decile__c->value@NUMERIC??'N/A'"), true)

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	const want = `[{"key":"MSME_APP","value":7},{"key":"RETAIL_APP","value":"N/A"}]`
	if string(encoded) != want {
		t.Fatalf("expected %s, got %s", want, encoded)
	}
}

func TestConvertDefaultForType(t *testing.T) {
	cases := []struct {
		literal    string
		targetType string
		want       interface{}
		wantOK     bool
	}{
		{"-999", "NUMERIC", -999, true},
		{"-9.5", "NUMERIC", -9.5, true},
		{" 42 ", "NUMERIC", 42, true},
		{"N/A", "NUMERIC", nil, false},
		{"", "NUMERIC", nil, false},
		{"true", "BOOLEAN", true, true},
		{"FALSE", "BOOLEAN", false, true},
		{"1", "BOOLEAN", true, true},
		{"0", "BOOLEAN", false, true},
		{"enabled", "BOOLEAN", true, true},
		{"disabled", "BOOLEAN", false, true},
		{"N/A", "BOOLEAN", nil, false},
		{"-999", "BOOLEAN", nil, false},
		{"N/A", "STRING", "N/A", true},
		{"-999", "STRING", "-999", true},
		{"N/A", "WEIRD", "N/A", true},
	}
	for _, c := range cases {
		got, ok := convertDefaultForType(c.literal, c.targetType)
		if ok != c.wantOK || got != c.want {
			t.Fatalf("convertDefaultForType(%q, %q) = %v/%v, want %v/%v",
				c.literal, c.targetType, got, ok, c.want, c.wantOK)
		}
	}
}

// applyTypeConversion (used for SOURCE values) must keep its lossy behaviour — only the default
// path is strict. This guards against someone "unifying" the two.
func TestApplyTypeConversion_StillLossyForSourceValues(t *testing.T) {
	ep := NewExpressionProcessor()

	if got := ep.applyTypeConversion("N/A", "NUMERIC"); got != nil {
		t.Fatalf("expected @NUMERIC on a non-numeric SOURCE value to stay null, got %v (%T)", got, got)
	}
	if got := ep.applyTypeConversion("N/A", "BOOLEAN"); got != false {
		t.Fatalf("expected @BOOLEAN on an unknown SOURCE value to stay false, got %v (%T)", got, got)
	}
}

// Verifies the mapping-pair split is quote-aware so a quoted default containing a comma
// survives, while unquoted commas still delimit pairs.
func TestSplitTransformPairs_QuoteAware(t *testing.T) {
	got := splitTransformPairs("Type__c->key,Status__c->status??'N/A, unknown'")
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs, got %d: %q", len(got), got)
	}
	if got[1] != "Status__c->status??'N/A, unknown'" {
		t.Fatalf("quoted default was split, got %q", got[1])
	}

	// Existing quoted-literal source configs split identically to the naive comma split.
	plain := splitTransformPairs("VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING")
	if len(plain) != 2 || plain[0] != "VALUE->telephoneNumber@STRING" || plain[1] != "'00'->telephoneType@STRING" {
		t.Fatalf("unexpected split of static-literal spec: %q", plain)
	}

	// An unbalanced quote falls back to the historical naive split rather than swallowing commas.
	unbalanced := splitTransformPairs("A->a,B->b'")
	if len(unbalanced) != 2 {
		t.Fatalf("expected naive fallback to yield 2 pairs, got %d: %q", len(unbalanced), unbalanced)
	}
}

// ---------------------------------------------------------------------------
// Evaluation — new behaviour (spec §4 "New-behaviour cases")
// ---------------------------------------------------------------------------

func TestTransformArray_DefaultCases(t *testing.T) {
	ep := NewExpressionProcessor()

	cases := []struct {
		name    string
		spec    string
		element map[string]interface{}
		key     string
		want    interface{}
	}{
		{
			name:    "numeric default typed emits a JSON number",
			spec:    "Decile__c->value@NUMERIC??-999",
			element: map[string]interface{}{"Decile__c": nil},
			key:     "value",
			want:    -999,
		},
		{
			name:    "numeric default untyped stays a string",
			spec:    "Decile__c->value??-999",
			element: map[string]interface{}{"Decile__c": nil},
			key:     "value",
			want:    "-999",
		},
		{
			name:    "explicit JSON null",
			spec:    "Decile__c->value??null",
			element: map[string]interface{}{"Decile__c": nil},
			key:     "value",
			want:    nil,
		},
		{
			name:    "quoted default containing a comma",
			spec:    "Status__c->status??'N/A, unknown'",
			element: map[string]interface{}{"Status__c": nil},
			key:     "status",
			want:    "N/A, unknown",
		},
		{
			name:    "missing nested path",
			spec:    "result.ctb->td_constitution??'UNKNOWN'",
			element: map[string]interface{}{"other": "x"},
			key:     "td_constitution",
			want:    "UNKNOWN",
		},
		{
			name:    "absent key",
			spec:    "Decile__c->value@NUMERIC??-999",
			element: map[string]interface{}{"Type__c": "MSME_APP"},
			key:     "value",
			want:    -999,
		},
		{
			name:    "empty string source",
			spec:    "Name__c->name??'BLANK'",
			element: map[string]interface{}{"Name__c": ""},
			key:     "name",
			want:    "BLANK",
		},
		{
			name:    "non-null source ignores the default",
			spec:    "Decile__c->value@NUMERIC??-999",
			element: map[string]interface{}{"Decile__c": "7"},
			key:     "value",
			want:    7,
		},
		{
			name:    "?? in the data but not in the spec passes through untouched",
			spec:    "Note__c->note",
			element: map[string]interface{}{"Note__c": "a??b"},
			key:     "note",
			want:    "a??b",
		},
		{
			name:    "@STRING applies to the default",
			spec:    "Decile__c->value@STRING??-999",
			element: map[string]interface{}{"Decile__c": nil},
			key:     "value",
			want:    "-999",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := ep.transformArray([]interface{}{c.element}, ep.parseTransformMap(c.spec), true)
			item := mustItem(t, out, 0)
			got, present := item[c.key]
			if !present {
				t.Fatalf("expected key %q in output, got %v", c.key, item)
			}
			if got != c.want {
				t.Fatalf("expected %q = %v (%T), got %v (%T)", c.key, c.want, c.want, got, got)
			}
		})
	}
}

// Verifies the App_Deciles driver payload: given the record set the Apex loop would keep,
// ?? fills the null Decile__c so Actico receives -999 instead of null.
func TestTransformArray_AppDecilesDriverScenario(t *testing.T) {
	ep := NewExpressionProcessor()

	source := []interface{}{
		map[string]interface{}{"Type__c": "MSME_APP", "Decile__c": "7"},
		map[string]interface{}{"Type__c": "RETAIL_APP", "Decile__c": nil},
	}

	out := ep.transformArray(source, ep.parseTransformMap("Type__c->key,Decile__c->value@NUMERIC??-999"), true)

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	const want = `[{"key":"MSME_APP","value":7},{"key":"RETAIL_APP","value":-999}]`
	if string(encoded) != want {
		t.Fatalf("expected %s, got %s", want, encoded)
	}
}

// Documents a pre-existing gap that ?? does NOT close and that the change spec assumes away:
// `filter:Type__c != ''` does not drop a record whose Type__c is JSON null. Salesforce returns
// a selected-but-null field as a present key with a nil value, and evaluateFilterCondition
// stringifies nil via fmt.Sprintf("%v", ...) to "<nil>", which is != "".
//
// So the Apex guard `if (a_score.Type__c != null)` is not reproduced by that filter. Use a
// positive filter instead (e.g. `Type__c IN ('MSME_APP','RETAIL_APP')`), which does exclude
// nulls because "<nil>" is not in the allowed set.
func TestFilterArray_NullFieldIsNotExcludedByNotEqualEmpty(t *testing.T) {
	ep := NewExpressionProcessor()

	source := []interface{}{
		map[string]interface{}{"Type__c": "MSME_APP", "Decile__c": "7"},
		map[string]interface{}{"Type__c": nil, "Decile__c": "3"},
		map[string]interface{}{"Decile__c": "5"}, // key absent entirely
	}

	// nil survives; the absent key is excluded.
	kept := ep.filterArray(source, "Type__c != ''")
	if len(kept) != 2 {
		t.Fatalf("expected the nil-Type__c record to survive `!= ''` (2 kept), got %d: %v", len(kept), kept)
	}
	if mustItem(t, kept, 1)["Type__c"] != nil {
		t.Fatalf("expected the second kept record to be the nil-Type__c one, got %v", kept[1])
	}

	// A positive membership filter does exclude both the nil and the absent record.
	positive := ep.filterArray(source, "Type__c IN ('MSME_APP','RETAIL_APP')")
	if len(positive) != 1 || mustItem(t, positive, 0)["Type__c"] != "MSME_APP" {
		t.Fatalf("expected only MSME_APP from the IN filter, got %v", positive)
	}
}

// Verifies ?? works on the transform (not transform-only) variant, and that the default does
// not leak into the pass-through copy of the original fields.
func TestTransformArray_DefaultInTransformMode(t *testing.T) {
	ep := NewExpressionProcessor()

	src := []interface{}{map[string]interface{}{"Decile__c": nil, "Extra__c": "keep"}}
	out := ep.transformArray(src, ep.parseTransformMap("Decile__c->value@NUMERIC??-999"), false)

	item := mustItem(t, out, 0)
	if item["value"] != -999 {
		t.Fatalf("expected value=-999, got %v (%T)", item["value"], item["value"])
	}
	if item["Extra__c"] != "keep" {
		t.Fatalf("expected untransformed Extra__c to pass through, got %v", item["Extra__c"])
	}
	if _, leaked := item["Decile__c"]; leaked {
		t.Fatalf("transform source field must not be copied through, got %v", item)
	}
}

// ---------------------------------------------------------------------------
// Evaluation — backward compatibility (spec §4 "Backward-compatibility cases")
// ---------------------------------------------------------------------------

// Without ??, a null source still yields JSON null and an absent field still yields null —
// exactly the v2.3 behaviour. This is the guard against the new branch firing unasked.
func TestTransformArray_NoDefaultKeepsV23Behaviour(t *testing.T) {
	ep := NewExpressionProcessor()

	src := []interface{}{
		map[string]interface{}{"Type__c": "MSME_APP", "Score__c": nil},
		map[string]interface{}{"Type__c": "RETAIL_APP"},
		map[string]interface{}{"Type__c": "SME_APP", "Score__c": ""},
	}
	out := ep.transformArray(src, ep.parseTransformMap("Type__c->key,Score__c->value@NUMERIC"), true)

	for i := range src {
		item := mustItem(t, out, i)
		if item["value"] != nil {
			t.Fatalf("element %d: expected value=null without ??, got %v (%T)", i, item["value"], item["value"])
		}
	}

	// Untyped mapping: null stays null and "" stays "".
	untyped := ep.transformArray(src, ep.parseTransformMap("Score__c->value"), true)
	if v := mustItem(t, untyped, 0)["value"]; v != nil {
		t.Fatalf("expected null source to stay null, got %v", v)
	}
	if v := mustItem(t, untyped, 2)["value"]; v != "" {
		t.Fatalf("expected empty source to stay \"\", got %v (%T)", v, v)
	}
}

// Nested source paths and the empty-LHS scalar form must parse identically to v2.3.
func TestParseTransformMap_UnchangedFormsWithoutDefault(t *testing.T) {
	ep := NewExpressionProcessor()

	nested := ep.parseTransformMap("result.pradr.adr->td_principal_place_of_business")
	if nested.fields["result.pradr.adr"] != "td_principal_place_of_business" {
		t.Fatalf("nested source path changed: %v", nested.fields)
	}
	if len(nested.defaults) != 0 {
		t.Fatalf("expected no defaults, got %v", nested.defaults)
	}

	scalar := ep.parseTransformMap("->tag@STRING")
	if scalar.fields[""] != "tag" || scalar.types["tag"] != "STRING" {
		t.Fatalf("empty-LHS scalar form changed: fields=%v types=%v", scalar.fields, scalar.types)
	}

	static := ep.parseTransformMap("VALUE->telephoneNumber@STRING,'00'->telephoneType@STRING")
	if static.statics["telephoneType"] != "00" {
		t.Fatalf("static literal form changed: %v", static.statics)
	}
	if len(static.defaults) != 0 {
		t.Fatalf("static literals must not register defaults, got %v", static.defaults)
	}
}

// A default declared after `merge` still applies, since merge re-enters the same
// transform-only path.
func TestEvaluateArrayExpression_MergeThenTransformOnlyWithDefault(t *testing.T) {
	ep := NewExpressionProcessor()

	expression := `[{"MATCHED_SOURCE":"A","MATCHED_RULENAME":"r1"}],[{"MATCHED_SOURCE":"B"}]` +
		`:merge:transform-only:MATCHED_SOURCE->key,MATCHED_RULENAME->value??'NONE'`
	got := ep.evaluateArrayExpression(expression, nil, nil)

	const want = `__ARRAY_PARSE__:[{"key":"A","value":"r1"},{"key":"B","value":"NONE"}]`
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// filter and map have no `->` mappings, so a literal `??` in their params must be left alone.
func TestApplyArrayOperation_FilterAndMapIgnoreDefaults(t *testing.T) {
	ep := NewExpressionProcessor()

	src := []interface{}{
		map[string]interface{}{"Score__c": "750", "Type__c": "A"},
		map[string]interface{}{"Score__c": "650", "Type__c": "B"},
	}

	filtered := ep.applyArrayOperation(src, "filter", []string{"Score__c@NUMERIC > 700"})
	if len(filtered) != 1 || mustItem(t, filtered, 0)["Type__c"] != "A" {
		t.Fatalf("expected only the 750 record, got %v", filtered)
	}

	mapped := ep.applyArrayOperation(src, "map", []string{"Type__c"})
	item := mustItem(t, mapped, 0)
	if len(item) != 1 || item["Type__c"] != "A" {
		t.Fatalf("expected map to select Type__c only, got %v", item)
	}
}

// ---------------------------------------------------------------------------
// `??` as data vs `??` as the operator
// ---------------------------------------------------------------------------

// Response DATA containing "??" can never affect the mapping: only the config spec is parsed for
// the `??` operator, and the resolved element value is never re-scanned. This test covers "??" in
// a value, in a key, at the start/end of a value, and a value that is nothing but "??".
func TestTransformArray_DoubleQuestionMarkInDataIsInert(t *testing.T) {
	ep := NewExpressionProcessor()

	source := []interface{}{
		map[string]interface{}{"Note__c": "a??b", "Type__c": "??"},
		map[string]interface{}{"Note__c": "??leading", "Type__c": "trailing??"},
		map[string]interface{}{"Note__c": "??", "Type__c": "x"},
	}

	// No `??` in the spec at all.
	out := ep.transformArray(source, ep.parseTransformMap("Note__c->note,Type__c->kind"), true)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	const want = `[{"kind":"??","note":"a??b"},{"kind":"trailing??","note":"??leading"},` +
		`{"kind":"x","note":"??"}]`
	if string(encoded) != want {
		t.Fatalf("data containing ?? was altered.\n want %s\n  got %s", want, encoded)
	}

	// Same data, but now the spec declares a default. A non-blank value containing "??" must still
	// win over the default.
	withDefault := ep.transformArray(source, ep.parseTransformMap("Note__c->note??FALLBACK"), true)
	for i, wantNote := range []string{"a??b", "??leading", "??"} {
		if got := mustItem(t, withDefault, i)["note"]; got != wantNote {
			t.Fatalf("element %d: expected note %q to beat the default, got %v", i, wantNote, got)
		}
	}
}

// A "??" inside a quoted region of the SPEC is data, not the operator. Before quote awareness the
// pair `'??'->tag` split into `'` + `'->tag`, the left half had no `->`, and the whole mapping was
// silently dropped from the output.
func TestParseTransformMap_QuotedDoubleQuestionMarkIsNotTheOperator(t *testing.T) {
	ep := NewExpressionProcessor()

	// Static literal that IS "??" — must inject the literal, not register a default.
	staticSpec := ep.parseTransformMap("'??'->tag,Decile__c->value")
	if staticSpec.statics["tag"] != "??" {
		t.Fatalf(`expected static literal tag="??", got statics=%v defaults=%v`, staticSpec.statics, staticSpec.defaults)
	}
	if len(staticSpec.defaults) != 0 {
		t.Fatalf("expected no defaults for a quoted ??, got %v", staticSpec.defaults)
	}
	// The sibling pair must survive; that is the mapping that used to disappear.
	if staticSpec.fields["Decile__c"] != "value" {
		t.Fatalf("sibling mapping was dropped: fields=%v", staticSpec.fields)
	}

	// Static literal that merely CONTAINS "??".
	containing := ep.parseTransformMap("'N/A??'->status,Type__c->key")
	if containing.statics["status"] != "N/A??" {
		t.Fatalf(`expected static literal status="N/A??", got %v`, containing.statics)
	}
	if containing.fields["Type__c"] != "key" {
		t.Fatalf("sibling mapping was dropped: fields=%v", containing.fields)
	}

	// A double-quoted literal is stripped and marked quoted, same as a single-quoted one.
	doubleQuoted := ep.parseTransformMap(`Note__c->note??"??"`)
	if got, ok := doubleQuoted.defaults["note"]; !ok || got.text != "??" || !got.quoted {
		t.Fatalf(`expected quoted default "??" from a double-quoted literal, got %+v (present=%v)`, got, ok)
	}

	// A default whose VALUE is the literal "??" (single-quoted, so the quotes are stripped).
	literalDefault := ep.parseTransformMap("Note__c->note??'??'")
	if got, ok := literalDefault.defaults["note"]; !ok || got.text != "??" || !got.quoted {
		t.Fatalf(`expected quoted default "??", got %+v (present=%v)`, got, ok)
	}

	// A quoted static literal containing a comma AND a ?? still stays one pair.
	commaLiteral := ep.parseTransformMap("'a,b??c'->tag,Type__c->key")
	if commaLiteral.statics["tag"] != "a,b??c" {
		t.Fatalf(`expected static literal tag="a,b??c", got %v`, commaLiteral.statics)
	}
	if commaLiteral.fields["Type__c"] != "key" {
		t.Fatalf("sibling mapping was dropped: fields=%v", commaLiteral.fields)
	}
}

// End-to-end proof that the quoted static literal survives all the way into the payload.
func TestTransformArray_QuotedDoubleQuestionMarkStaticLiteral(t *testing.T) {
	ep := NewExpressionProcessor()

	source := []interface{}{map[string]interface{}{"Decile__c": "7"}}
	out := ep.transformArray(source, ep.parseTransformMap("'??'->tag,Decile__c->value@NUMERIC??-999"), true)

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	const want = `[{"tag":"??","value":7}]`
	if string(encoded) != want {
		t.Fatalf("expected %s, got %s", want, encoded)
	}
}

func TestIndexUnquotedToken(t *testing.T) {
	cases := []struct {
		text  string
		token string
		want  int
	}{
		{"Decile__c->value??-999", "??", 16},
		{"'??'->tag", "??", -1},
		{`"??"->tag`, "??", -1},
		{"Note__c->note??'??'", "??", 13},
		{"'a??b'->tag,X->y??1", "??", 16},
		{"no operator here", "??", -1},
		{"", "??", -1},
		// A double quote inside a single-quoted region does not close it, so the ?? stays quoted.
		{`'say "hi" ??'->tag`, "??", -1},
		// An escaped quote does not close the region either.
		{`'it\'s ??'->tag`, "??", -1},
		// Unbalanced quote: everything after it counts as quoted, so the ?? is not found.
		{"X->y??1'unclosed", "??", 4},
	}
	for _, c := range cases {
		if got := indexUnquotedToken(c.text, c.token); got != c.want {
			t.Fatalf("indexUnquotedToken(%q, %q) = %d, want %d", c.text, c.token, got, c.want)
		}
	}
}
