package sequence_service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"esa/internal/app/models"
	"esa/internal/app/utility"

	"github.com/dmi-infotech/common-modules/go/infrastructure/logger"
	"go.uber.org/zap"
)

// testCtx returns a context carrying a no-op request logger so applyPlugResponseInto's
// logging calls do not depend on a constructed SequenceService.logger.
func testCtx() context.Context {
	l := logger.NewZapLoggerFromSugared(zap.NewNop().Sugar())
	return utility.SetRequestLogger(context.Background(), l)
}

// newTestEsaLog builds an EsaLog with the given additional_config, response body, and source stamp.
func newTestEsaLog(additionalConfig string, body map[string]interface{}, source string) *models.EsaLog {
	esaLog := models.NewEsaLog()
	esaLog.ServiceId = 1
	esaLog.ServiceName = "TestService"
	if additionalConfig != "" {
		esaLog.SetField("additional_config", additionalConfig)
	}
	esaLog.Response.Body = body
	if source != "" {
		esaLog.Response.Source = source
	}
	return esaLog
}

func TestPlugWalk_ExactMarkerEmbedsObject(t *testing.T) {
	template := map[string]interface{}{"custom_response_key": plugResponseMarker}
	resp := map[string]interface{}{"score": float64(700)}

	got := plugWalk(template, resp)
	want := map[string]interface{}{"custom_response_key": map[string]interface{}{"score": float64(700)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugWalk exact marker => %#v, want %#v", got, want)
	}
}

func TestPlugWalk_EmbeddedMarkerStringifies(t *testing.T) {
	template := map[string]interface{}{"note": "raw=<Actual_response>!"}
	resp := map[string]interface{}{"a": float64(1)}

	got := plugWalk(template, resp).(map[string]interface{})
	want := `raw={"a":1}!`
	if got["note"] != want {
		t.Fatalf("embedded marker => %q, want %q", got["note"], want)
	}
}

func TestPlugWalk_NestedAndArray(t *testing.T) {
	template := map[string]interface{}{
		"outer": map[string]interface{}{
			"list": []interface{}{"static", plugResponseMarker},
		},
	}
	resp := map[string]interface{}{"x": true}

	got := plugWalk(template, resp)
	want := map[string]interface{}{
		"outer": map[string]interface{}{
			"list": []interface{}{"static", map[string]interface{}{"x": true}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested plugWalk => %#v, want %#v", got, want)
	}
}

func TestPlugWalk_NoMarkerUnchanged(t *testing.T) {
	template := map[string]interface{}{"k": "no marker here"}
	got := plugWalk(template, map[string]interface{}{"ignored": 1})
	if !reflect.DeepEqual(got, template) {
		t.Fatalf("no-marker template should be unchanged, got %#v", got)
	}
}

func TestParsePlugResponseInto_BareTemplate(t *testing.T) {
	ac := `{"plug_response_into": {"custom_response_key": "<Actual_response>"}}`
	esaLog := newTestEsaLog(ac, nil, "")
	s := &SequenceService{}

	tmpl, applyTo, ok := s.parsePlugResponseInto(esaLog)
	if !ok {
		t.Fatal("expected ok=true for bare template")
	}
	m, isMap := tmpl.(map[string]interface{})
	if !isMap || m["custom_response_key"] != plugResponseMarker {
		t.Fatalf("unexpected template: %#v", tmpl)
	}
	// Omitted apply_to => defaults (all four in-scope sources)
	for _, src := range []string{responseSourceAPICall, responseSourceStaticResponse, responseSourcePickResponseFrom, responseSourceFanOut} {
		if !applyTo[src] {
			t.Errorf("default apply_to missing source %q", src)
		}
	}
}

func TestParsePlugResponseInto_StructuredWithApplyTo(t *testing.T) {
	ac := `{"plug_response_into": {"template": {"wrap": "<Actual_response>"}, "apply_to": ["pick_response_from"]}}`
	esaLog := newTestEsaLog(ac, nil, "")
	s := &SequenceService{}

	tmpl, applyTo, ok := s.parsePlugResponseInto(esaLog)
	if !ok {
		t.Fatal("expected ok=true for structured form")
	}
	if m, isMap := tmpl.(map[string]interface{}); !isMap || m["wrap"] != plugResponseMarker {
		t.Fatalf("unexpected template: %#v", tmpl)
	}
	if !applyTo[responseSourcePickResponseFrom] {
		t.Error("apply_to should allow pick_response_from")
	}
	if applyTo[responseSourceAPICall] {
		t.Error("apply_to should NOT allow api_call")
	}
}

func TestParsePlugResponseInto_Absent(t *testing.T) {
	esaLog := newTestEsaLog(`{"PreExecution": {"enabled": true}}`, nil, "")
	s := &SequenceService{}
	if _, _, ok := s.parsePlugResponseInto(esaLog); ok {
		t.Fatal("expected ok=false when plug_response_into absent")
	}
}

func TestApplyPlugResponseInto_APICallWraps(t *testing.T) {
	ac := `{"plug_response_into": {"custom_response_key": "<Actual_response>"}}`
	body := map[string]interface{}{"score": float64(700)}
	esaLog := newTestEsaLog(ac, body, responseSourceAPICall)
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	want := map[string]interface{}{"custom_response_key": map[string]interface{}{"score": float64(700)}}
	if !reflect.DeepEqual(esaLog.Response.Body, want) {
		t.Fatalf("wrapped body => %#v, want %#v", esaLog.Response.Body, want)
	}
}

func TestApplyPlugResponseInto_SourceNotAllowed(t *testing.T) {
	ac := `{"plug_response_into": {"template": {"wrap": "<Actual_response>"}, "apply_to": ["pick_response_from"]}}`
	body := map[string]interface{}{"score": float64(700)}
	esaLog := newTestEsaLog(ac, body, responseSourceAPICall) // source api_call not in apply_to
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	if !reflect.DeepEqual(esaLog.Response.Body, body) {
		t.Fatalf("body should be unchanged when source not allowed, got %#v", esaLog.Response.Body)
	}
}

func TestApplyPlugResponseInto_NoSourceNoWrap(t *testing.T) {
	ac := `{"plug_response_into": {"custom_response_key": "<Actual_response>"}}`
	body := map[string]interface{}{"score": float64(700)}
	esaLog := newTestEsaLog(ac, body, "") // no source stamp (e.g. HTTP error path)
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	if !reflect.DeepEqual(esaLog.Response.Body, body) {
		t.Fatalf("body should be unchanged when no source stamped, got %#v", esaLog.Response.Body)
	}
}

func TestApplyPlugResponseInto_PickResponseFrom(t *testing.T) {
	ac := `{"plug_response_into": {"picked": "<Actual_response>"}}`
	body := map[string]interface{}{"Response__c": "cached"}
	esaLog := newTestEsaLog(ac, body, responseSourcePickResponseFrom)
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	want := map[string]interface{}{"picked": map[string]interface{}{"Response__c": "cached"}}
	if !reflect.DeepEqual(esaLog.Response.Body, want) {
		t.Fatalf("picked wrap => %#v, want %#v", esaLog.Response.Body, want)
	}
}

// TestParsePlugResponseInto_NestedInPreExecution ensures plug_response_into is found when
// nested inside pre_execution (co-located with pick_response_from), not just at the root.
func TestParsePlugResponseInto_NestedInPreExecution(t *testing.T) {
	ac := `{"pre_execution": {"enabled": true, "pick_response_from": "<X.Y>", "plug_response_into": {"apply_to": ["pick_response_from"], "template": {"raw_response": "<Actual_response_json>"}}}}`
	esaLog := newTestEsaLog(ac, nil, "")
	s := &SequenceService{}

	tmpl, applyTo, ok := s.parsePlugResponseInto(esaLog)
	if !ok {
		t.Fatal("expected ok=true for plug_response_into nested in pre_execution")
	}
	if m, isMap := tmpl.(map[string]interface{}); !isMap || m["raw_response"] != plugResponseMarkerJSON {
		t.Fatalf("unexpected template: %#v", tmpl)
	}
	if !applyTo[responseSourcePickResponseFrom] || applyTo[responseSourceAPICall] {
		t.Errorf("apply_to should allow only pick_response_from, got %#v", applyTo)
	}
}

// TestParsePlugResponseInto_RootTakesPrecedence ensures a root-level plug_response_into wins
// over a nested one when both are present.
func TestParsePlugResponseInto_RootTakesPrecedence(t *testing.T) {
	ac := `{"plug_response_into": {"root_key": "<Actual_response>"}, "pre_execution": {"plug_response_into": {"nested_key": "<Actual_response>"}}}`
	esaLog := newTestEsaLog(ac, nil, "")
	s := &SequenceService{}

	tmpl, _, ok := s.parsePlugResponseInto(esaLog)
	if !ok {
		t.Fatal("expected ok=true")
	}
	m, _ := tmpl.(map[string]interface{})
	if _, hasRoot := m["root_key"]; !hasRoot {
		t.Fatalf("root-level plug_response_into should take precedence, got %#v", tmpl)
	}
}

// TestPlugWalk_JSONTokenStringifies ensures <Actual_response_json> yields a JSON *string*,
// not an embedded object, so parse-first downstream consumers keep working.
func TestPlugWalk_JSONTokenStringifies(t *testing.T) {
	template := map[string]interface{}{"raw_response": plugResponseMarkerJSON}
	resp := map[string]interface{}{"CIR-REPORT-FILE": map[string]interface{}{"STATUS": "SUCCESS"}}

	got := plugWalk(template, resp).(map[string]interface{})
	str, isStr := got["raw_response"].(string)
	if !isStr {
		t.Fatalf("raw_response should be a JSON string, got %T (%#v)", got["raw_response"], got["raw_response"])
	}
	// Must be valid, re-parseable JSON.
	var back map[string]interface{}
	if err := json.Unmarshal([]byte(str), &back); err != nil {
		t.Fatalf("stringified raw_response is not valid JSON: %v (%q)", err, str)
	}
	if _, ok := back["CIR-REPORT-FILE"]; !ok {
		t.Fatalf("round-tripped JSON missing CIR-REPORT-FILE: %#v", back)
	}
}

// TestApplyPlugResponseInto_JSONTokenEndToEnd verifies the CRIF-style config: pick source +
// nested plug + stringify token produces a string raw_response.
func TestApplyPlugResponseInto_JSONTokenEndToEnd(t *testing.T) {
	ac := `{"pre_execution": {"pick_response_from": "<X.Y>", "plug_response_into": {"apply_to": ["pick_response_from"], "template": {"raw_response": "<Actual_response_json>"}}}}`
	body := map[string]interface{}{"CIR-REPORT-FILE": map[string]interface{}{"STATUS": "SUCCESS"}}
	esaLog := newTestEsaLog(ac, body, responseSourcePickResponseFrom)
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	str, isStr := esaLog.Response.Body["raw_response"].(string)
	if !isStr {
		t.Fatalf("raw_response should be a JSON string, got %T", esaLog.Response.Body["raw_response"])
	}
	var back map[string]interface{}
	if err := json.Unmarshal([]byte(str), &back); err != nil {
		t.Fatalf("raw_response not valid JSON: %v", err)
	}
}

// TestApplyPlugResponseInto_SourceReadFromResponse ensures the gating source is read from
// ResponseDetails.Source (not ExtraFields).
func TestApplyPlugResponseInto_SourceReadFromResponse(t *testing.T) {
	ac := `{"plug_response_into": {"wrap": "<Actual_response>"}}`
	body := map[string]interface{}{"score": float64(700)}
	esaLog := newTestEsaLog(ac, body, "")
	esaLog.Response.Source = responseSourceAPICall // set directly on the response
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	want := map[string]interface{}{"wrap": map[string]interface{}{"score": float64(700)}}
	if !reflect.DeepEqual(esaLog.Response.Body, want) {
		t.Fatalf("wrap => %#v, want %#v", esaLog.Response.Body, want)
	}
}

func TestApplyPlugResponseInto_NonObjectTemplateSkipped(t *testing.T) {
	// Template root is a JSON array => cannot become Response.Body (a map) => skip, body unchanged.
	ac := `{"plug_response_into": ["<Actual_response>"]}`
	body := map[string]interface{}{"score": float64(700)}
	esaLog := newTestEsaLog(ac, body, responseSourceAPICall)
	s := &SequenceService{}

	s.applyPlugResponseInto(testCtx(), esaLog, nil)

	if !reflect.DeepEqual(esaLog.Response.Body, body) {
		t.Fatalf("body should be unchanged for non-object template root, got %#v", esaLog.Response.Body)
	}
}
