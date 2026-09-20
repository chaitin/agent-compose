package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOmitDeclaredScriptsPreservesUnrelatedConfiguration(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{"name":"agent","system_prompt":"script body","scheduler":{"script":"","enabled":false},"env":[{"name":"script","value":"keep"}]}`,
		`{"agents":[{"name":"without-scheduler"},{"name":"empty","scheduler":{"script":""}}],"variables":[]}`,
	} {
		output, omitted, err := omitDeclaredScripts(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(omitted) != 0 {
			t.Fatalf("unexpected omissions: %#v", omitted)
		}
		var want, got any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("unrelated configuration changed: %s", output)
		}
	}
}

func TestOmitDeclaredScriptsRejectsMalformedConfiguration(t *testing.T) {
	for _, raw := range []string{`{`, `{"agents":{}}`, `{"scheduler":"invalid"}`, `{"scheduler":{"script":42}}`} {
		if _, _, err := omitDeclaredScripts(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted malformed declaration: %s", raw)
		}
	}
}

func TestOmitDeclaredScriptsDoesNotMutateInput(t *testing.T) {
	raw := json.RawMessage(`{"scheduler":{"script":"print('ok')","enabled":true},"system_prompt":"print('ok')"}`)
	original := string(raw)
	output, omitted, err := omitDeclaredScripts(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original || len(omitted) != 1 || !strings.Contains(string(output), `"system_prompt":"print('ok')"`) || !strings.Contains(string(output), `"enabled":true`) {
		t.Fatalf("omission changed source or unrelated fields: %s", output)
	}
}
