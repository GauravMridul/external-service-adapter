package unit

import (
	"testing"

	"esa/internal/app/utility"

	"github.com/stretchr/testify/assert"
)

// The quote-aware scanning helpers are the single source of truth shared by the runtime transform
// parser (sequence_service.splitTransformPairs / indexUnquotedToken) and SOQL field extraction
// (utility.extractFieldsFromArrayTransformSpec). If these two ever tokenise a spec differently, the
// fetched columns stop matching the requested mappings, so the helpers are pinned here directly.

func TestSplitTransformSpecUnquoted_Comma(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain pairs", "A__c->a,B__c->b", []string{"A__c->a", "B__c->b"}},
		{"comma inside single quotes", "A__c->a??'x,y',B__c->b", []string{"A__c->a??'x,y'", "B__c->b"}},
		{"comma inside double quotes", `A__c->a??"x,y",B__c->b`, []string{`A__c->a??"x,y"`, "B__c->b"}},
		{"single-quoted static literal", "'00'->t,A__c->a", []string{"'00'->t", "A__c->a"}},
		{"double quote inside single quotes", `'say "hi", ok'->t,A__c->a`, []string{`'say "hi", ok'->t`, "A__c->a"}},
		{"escaped quote does not close", `'it\'s, fine'->t,A__c->a`, []string{`'it\'s, fine'->t`, "A__c->a"}},
		{"no delimiter", "A__c->a", []string{"A__c->a"}},
		{"empty input", "", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, utility.SplitTransformSpecUnquoted(c.in, ','))
		})
	}

	// Unbalanced quote falls back to the historical naive split so an existing config cannot change
	// behaviour because of a stray apostrophe.
	assert.Equal(t, []string{"A__c->a", "B__c->b'"},
		utility.SplitTransformSpecUnquoted("A__c->a,B__c->b'", ','))
}

func TestSplitTransformSpecUnquoted_Colon(t *testing.T) {
	// A colon inside a quoted default must not split the operation from its params.
	assert.Equal(t,
		[]string{"transform-only", "Status__c->status??'N/A: none',Type__c->key"},
		utility.SplitTransformSpecUnquoted("transform-only:Status__c->status??'N/A: none',Type__c->key", ':'))

	assert.Equal(t,
		[]string{"transform-only", "'00:00'->t,Type__c->key"},
		utility.SplitTransformSpecUnquoted("transform-only:'00:00'->t,Type__c->key", ':'))

	assert.Equal(t, []string{"map", "Type__c,Decile__c"},
		utility.SplitTransformSpecUnquoted("map:Type__c,Decile__c", ':'))
}

func TestIndexUnquotedToken(t *testing.T) {
	cases := []struct {
		in    string
		token string
		want  int
	}{
		{"Decile__c->value??-999", "??", 16},
		{"'??'->tag", "??", -1},
		{`"??"->tag`, "??", -1},
		{"Note__c->note??'??'", "??", 13},
		{"'a??b'->tag,X->y??1", "??", 16},
		{`'say "hi" ??'->tag`, "??", -1},
		{`'it\'s ??'->tag`, "??", -1},
		{"no operator here", "??", -1},
		{"", "??", -1},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, utility.IndexUnquotedToken(c.in, c.token),
			"IndexUnquotedToken(%q, %q)", c.in, c.token)
	}
}

func TestIsQuotedLiteralAndUnquote(t *testing.T) {
	for _, in := range []string{"'00'", `"00"`, "'a,b'", `"a:b"`, "''", `""`} {
		assert.Truef(t, utility.IsQuotedLiteral(in), "expected %q to be a quoted literal", in)
	}
	for _, in := range []string{"Decile__c", "", "'", `"`, "'mismatched\"", "no quotes"} {
		assert.Falsef(t, utility.IsQuotedLiteral(in), "expected %q NOT to be a quoted literal", in)
	}

	assert.Equal(t, "00", utility.UnquoteLiteral("'00'"))
	assert.Equal(t, "00", utility.UnquoteLiteral(`"00"`))
	assert.Equal(t, "a,b", utility.UnquoteLiteral("'a,b'"))
	assert.Equal(t, "", utility.UnquoteLiteral("''"))
	// Not a quoted literal: returned unchanged.
	assert.Equal(t, "Decile__c", utility.UnquoteLiteral("Decile__c"))
}
