package compose

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestNormalizeInterpolatesEnvValues(t *testing.T) {
	spec := mustParseCompose(t, `
name: interpolated
variables:
  API_KEY:
    value: ${API_KEY}
    secret: true
  ENDPOINT: https://${HOST}:${PORT}/v1
  LITERAL: $HOST ${} ${HOST
agents:
  reviewer:
    env:
      AUTH_HEADER: Bearer ${API_KEY}
      EMPTY: ${EMPTY_VALUE}
`)

	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{
		"API_KEY":     "sk-test",
		"HOST":        "api.example",
		"PORT":        "443",
		"EMPTY_VALUE": "",
	}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.Variables["API_KEY"]; got.Value != "sk-test" || !got.Secret {
		t.Fatalf("API_KEY = %#v, want interpolated secret", got)
	}
	if got := normalized.Variables["ENDPOINT"].Value; got != "https://api.example:443/v1" {
		t.Fatalf("ENDPOINT = %q", got)
	}
	if got := normalized.Variables["LITERAL"].Value; got != "$HOST ${} ${HOST" {
		t.Fatalf("LITERAL = %q", got)
	}
	if got := normalized.Agents[0].Env["AUTH_HEADER"].Value; got != "Bearer sk-test" {
		t.Fatalf("AUTH_HEADER = %q", got)
	}
	if got := normalized.Agents[0].Env["EMPTY"].Value; got != "" {
		t.Fatalf("EMPTY = %q, want empty string", got)
	}
}

func TestNormalizeInterpolationMissingEnvIncludesFieldPath(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{
			name: "project variable",
			raw: `
name: missing-env
variables:
  OPENAI_API_KEY: ${OPENAI_API_KEY}
agents:
  reviewer:
    provider: codex
`,
			wantField: "variables.OPENAI_API_KEY.value",
		},
		{
			name: "agent env",
			raw: `
name: missing-agent-env
agents:
  reviewer:
    env:
      TOKEN: ${TOKEN}
`,
			wantField: "agents.reviewer.env.TOKEN.value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, tt.raw)
			_, err := Normalize(spec, NormalizeOptions{Env: map[string]string{}})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, tt.wantField) {
				t.Fatalf("error = %q, want field %s", got, tt.wantField)
			}
		})
	}
}

func TestNormalizeDoesNotInterpolateFromComposeVariables(t *testing.T) {
	spec := mustParseCompose(t, `
name: compose-vars-not-env
variables:
  TOKEN: compose-token
agents:
  reviewer:
    env:
      TOKEN_COPY: ${TOKEN}
`)

	_, err := Normalize(spec, NormalizeOptions{Env: map[string]string{}})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.env.TOKEN_COPY.value") {
		t.Fatalf("error = %q, want agent env path", got)
	}
}

func TestRedactedOutputDoesNotLeakSecretValues(t *testing.T) {
	spec := mustParseCompose(t, `
name: redacted
variables:
  API_KEY:
    value: ${API_KEY}
    secret: true
  EMPTY_SECRET:
    value: ${EMPTY_SECRET}
    secret: true
  PUBLIC: visible
agents:
  reviewer:
    env:
      TOKEN:
        value: ${TOKEN}
        secret: true
      MODE: strict
`)
	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{
		"API_KEY":      "sk-secret",
		"EMPTY_SECRET": "",
		"TOKEN":        "agent-secret",
	}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}

	jsonData, err := normalized.MarshalCanonicalJSON(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	yamlData, err := normalized.MarshalCanonicalYAML(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	for _, data := range [][]byte{jsonData, yamlData} {
		if bytes.Contains(data, []byte("sk-secret")) || bytes.Contains(data, []byte("agent-secret")) {
			t.Fatalf("secret leaked in redacted output: %s", data)
		}
		if !bytes.Contains(data, []byte(redactedEnvValue)) {
			t.Fatalf("redacted marker missing from output: %s", data)
		}
		if !bytes.Contains(data, []byte("visible")) || !bytes.Contains(data, []byte("strict")) {
			t.Fatalf("non-secret value missing from output: %s", data)
		}
		if !bytes.Contains(data, []byte("secret")) {
			t.Fatalf("secret metadata missing from output: %s", data)
		}
	}
	if got := normalized.Variables["API_KEY"].Value; got != "sk-secret" {
		t.Fatalf("redaction mutated normalized variable = %q", got)
	}
	if got := normalized.Agents[0].Env["TOKEN"].Value; got != "agent-secret" {
		t.Fatalf("redaction mutated normalized agent env = %q", got)
	}

	secondJSON, err := normalized.MarshalCanonicalJSON(true)
	if err != nil {
		t.Fatalf("second MarshalCanonicalJSON returned error: %v", err)
	}
	if !bytes.Equal(jsonData, secondJSON) {
		t.Fatalf("redacted output was not stable:\n%s\n%s", jsonData, secondJSON)
	}
	secondYAML, err := normalized.MarshalCanonicalYAML(true)
	if err != nil {
		t.Fatalf("second MarshalCanonicalYAML returned error: %v", err)
	}
	if !bytes.Equal(yamlData, secondYAML) {
		t.Fatalf("redacted YAML output was not stable:\n%s\n%s", yamlData, secondYAML)
	}
}

func TestRedactedOutputPreservesSchedulerScript(t *testing.T) {
	normalized := mustNormalizeCompose(t, `
name: redacted-script
variables:
  API_KEY:
    value: ${API_KEY}
    secret: true
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "1h");
        export async function main(payload) {
          return scheduler.agent("review with sk-script-literal");
        }
`, map[string]string{"API_KEY": "sk-secret"})

	redacted := normalized.Redacted()
	if redacted == nil || len(redacted.Agents) != 1 || redacted.Agents[0].Scheduler == nil {
		t.Fatalf("redacted scheduler missing: %#v", redacted)
	}
	if got, want := redacted.Agents[0].Scheduler.Script, normalized.Agents[0].Scheduler.Script; got != want {
		t.Fatalf("redacted scheduler script = %q, want %q", got, want)
	}
	if got := redacted.Variables["API_KEY"].Value; got != redactedEnvValue {
		t.Fatalf("redacted API_KEY = %q, want redacted marker", got)
	}
}

func TestCanonicalOutputIncludesSchedulerScriptAndIsStable(t *testing.T) {
	normalized := mustNormalizeCompose(t, `
name: canonical-script
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "1h");
        export async function main(payload) {
          return payload;
        }
`, nil)

	jsonData, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	yamlData, err := normalized.MarshalCanonicalYAML(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	for _, data := range [][]byte{jsonData, yamlData} {
		if !bytes.Contains(data, []byte("script")) || !bytes.Contains(data, []byte("scheduler.interval")) || !bytes.Contains(data, []byte("hourly-review")) {
			t.Fatalf("scheduler script missing from canonical output: %s", data)
		}
	}

	secondJSON, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("second MarshalCanonicalJSON returned error: %v", err)
	}
	if !bytes.Equal(jsonData, secondJSON) {
		t.Fatalf("canonical JSON output was not stable:\n%s\n%s", jsonData, secondJSON)
	}
	secondYAML, err := normalized.MarshalCanonicalYAML(false)
	if err != nil {
		t.Fatalf("second MarshalCanonicalYAML returned error: %v", err)
	}
	if !bytes.Equal(yamlData, secondYAML) {
		t.Fatalf("canonical YAML output was not stable:\n%s\n%s", yamlData, secondYAML)
	}
}

func TestCanonicalOutputIncludesAgentCapsetIDs(t *testing.T) {
	normalized := mustNormalizeCompose(t, `
name: canonical-capsets
agents:
  reviewer:
    provider: codex
    capset_ids:
      - xray-dev
`, nil)

	jsonData, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	yamlData, err := normalized.MarshalCanonicalYAML(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	for _, data := range [][]byte{jsonData, yamlData} {
		if !bytes.Contains(data, []byte("capset_ids")) || !bytes.Contains(data, []byte("xray-dev")) {
			t.Fatalf("capset ids missing from canonical output: %s", data)
		}
	}
	redacted := normalized.Redacted()
	if redacted == nil || len(redacted.Agents) != 1 || len(redacted.Agents[0].CapsetIDs) != 1 || redacted.Agents[0].CapsetIDs[0] != "xray-dev" {
		t.Fatalf("redacted output lost capset ids: %#v", redacted)
	}
}

func TestCanonicalOutputIncludesJupyterAndIsStable(t *testing.T) {
	normalized := mustNormalizeCompose(t, `
name: canonical-jupyter
agents:
  reviewer:
    provider: codex
    jupyter:
      enabled: true
      guest_port: 8888
`, nil)

	jsonData, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	yamlData, err := normalized.MarshalCanonicalYAML(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	for _, data := range [][]byte{jsonData, yamlData} {
		if !bytes.Contains(data, []byte("jupyter")) || !bytes.Contains(data, []byte("guest_port")) || !bytes.Contains(data, []byte("8888")) {
			t.Fatalf("jupyter missing from canonical output: %s", data)
		}
	}

	secondJSON, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("second MarshalCanonicalJSON returned error: %v", err)
	}
	if !bytes.Equal(jsonData, secondJSON) {
		t.Fatalf("canonical JSON output was not stable:\n%s\n%s", jsonData, secondJSON)
	}

	redacted := normalized.Redacted()
	if redacted == nil || len(redacted.Agents) != 1 || redacted.Agents[0].Jupyter == nil || redacted.Agents[0].Jupyter.GuestPort != 8888 {
		t.Fatalf("redacted output lost jupyter: %#v", redacted)
	}
}

func TestSpecHashIncludesJupyterChanges(t *testing.T) {
	disabled := mustNormalizeCompose(t, `
name: jupyter-hash
agents:
  reviewer:
    provider: codex
`, nil)
	enabled := mustNormalizeCompose(t, `
name: jupyter-hash
agents:
  reviewer:
    provider: codex
    jupyter:
      enabled: true
      guest_port: 8888
`, nil)
	changedPort := mustNormalizeCompose(t, `
name: jupyter-hash
agents:
  reviewer:
    provider: codex
    jupyter:
      enabled: true
      guest_port: 9999
`, nil)

	if got := mustHash(t, enabled); got == mustHash(t, disabled) {
		t.Fatalf("hash did not change when jupyter was enabled")
	}
	if got := mustHash(t, changedPort); got == mustHash(t, enabled) {
		t.Fatalf("hash did not change when jupyter guest port changed")
	}
}

func TestSpecHashIgnoresFieldAndMapOrder(t *testing.T) {
	first := mustNormalizeCompose(t, `
name: hash-project
variables:
  B: two
  A: one
agents:
  worker:
    env:
      Z: last
      A: first
    scheduler:
      triggers:
        - cron: "@hourly"
  reviewer:
    provider: codex
`, nil)
	second := mustNormalizeCompose(t, `
agents:
  reviewer:
    provider: codex
  worker:
    scheduler:
      triggers:
        - cron: "@hourly"
    env:
      A: first
      Z: last
variables:
  A: one
  B: two
name: hash-project
`, nil)

	firstHash := mustHash(t, first)
	secondHash := mustHash(t, second)
	if firstHash != secondHash {
		t.Fatalf("hash mismatch for reordered specs: %s != %s", firstHash, secondHash)
	}
}

func TestSpecHashIncludesSemanticDifferences(t *testing.T) {
	base := mustNormalizeCompose(t, `
name: hash-diff
variables:
  TOKEN:
    value: ${TOKEN}
    secret: true
agents:
  reviewer:
    scheduler:
      triggers:
        - cron: "@hourly"
        - event:
            topic: git.push
`, map[string]string{"TOKEN": "one"})
	changedSecret := mustNormalizeCompose(t, `
name: hash-diff
variables:
  TOKEN:
    value: ${TOKEN}
    secret: true
agents:
  reviewer:
    scheduler:
      triggers:
        - cron: "@hourly"
        - event:
            topic: git.push
`, map[string]string{"TOKEN": "two"})
	changedTriggerOrder := mustNormalizeCompose(t, `
name: hash-diff
variables:
  TOKEN:
    value: ${TOKEN}
    secret: true
agents:
  reviewer:
    scheduler:
      triggers:
        - event:
            topic: git.push
        - cron: "@hourly"
`, map[string]string{"TOKEN": "one"})
	changedSecretFlag := mustNormalizeCompose(t, `
name: hash-diff
variables:
  TOKEN:
    value: ${TOKEN}
    secret: false
agents:
  reviewer:
    scheduler:
      triggers:
        - cron: "@hourly"
        - event:
            topic: git.push
`, map[string]string{"TOKEN": "one"})

	baseHash := mustHash(t, base)
	if got := mustHash(t, changedSecret); got == baseHash {
		t.Fatalf("hash did not change when secret value changed")
	}
	if got := mustHash(t, changedSecretFlag); got == baseHash {
		t.Fatalf("hash did not change when secret flag changed")
	}
	if got := mustHash(t, changedTriggerOrder); got == baseHash {
		t.Fatalf("hash did not change when trigger order changed")
	}

	baseRedacted, err := base.MarshalCanonicalJSON(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON base redacted: %v", err)
	}
	changedRedacted, err := changedSecret.MarshalCanonicalJSON(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON changed redacted: %v", err)
	}
	if !bytes.Equal(baseRedacted, changedRedacted) {
		t.Fatalf("redacted outputs should match for different secret values:\n%s\n%s", baseRedacted, changedRedacted)
	}
}

func TestSpecHashIncludesSchedulerScriptChanges(t *testing.T) {
	base := mustNormalizeCompose(t, `
name: script-hash
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "1h");
        export async function main(payload) {
          return payload;
        }
`, nil)
	changed := mustNormalizeCompose(t, `
name: script-hash
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "2h");
        export async function main(payload) {
          return payload;
        }
`, nil)

	if got := mustHash(t, changed); got == mustHash(t, base) {
		t.Fatalf("hash did not change when scheduler script changed")
	}
}

func TestResolvedSchedulerScriptURLUsesSnapshotForOutputAndHash(t *testing.T) {
	inline := mustNormalizeCompose(t, `
name: script-url-hash
agents:
  reviewer:
    scheduler:
      script: scheduler.interval("hourly-review", "1h");
`, nil)
	resolve := func(location, content string) *NormalizedProjectSpec {
		spec := mustParseCompose(t, "name: script-url-hash\nagents:\n  reviewer:\n    scheduler:\n      script:\n        url: "+location+"\n")
		normalized, err := Normalize(spec, NormalizeOptions{
			ComposePath:       "/project/agent-compose.yml",
			ResolveScriptURLs: true,
			ScriptSourceResolver: ScriptSourceResolverFunc(func(context.Context, string) ([]byte, error) {
				return []byte(content), nil
			}),
		})
		if err != nil {
			t.Fatalf("Normalize URL source: %v", err)
		}
		return normalized
	}
	first := resolve("https://one.example/scheduler.js", inline.Agents[0].Scheduler.Script)
	second := resolve("https://two.example/other.js", inline.Agents[0].Scheduler.Script)
	changed := resolve("https://one.example/scheduler.js", `scheduler.interval("hourly-review", "2h");`)
	if mustHash(t, first) != mustHash(t, inline) || mustHash(t, second) != mustHash(t, inline) {
		t.Fatal("equivalent URL snapshots and inline scripts must have identical hashes")
	}
	if mustHash(t, changed) == mustHash(t, inline) {
		t.Fatal("changed URL content must change the hash")
	}
	inlineJSON, _ := inline.MarshalCanonicalJSON(false)
	urlJSON, _ := first.MarshalCanonicalJSON(false)
	if !bytes.Equal(inlineJSON, urlJSON) {
		t.Fatalf("canonical snapshots differ:\n%s\n%s", inlineJSON, urlJSON)
	}
}

func TestUnresolvedSchedulerScriptURLFailsCanonicalOutputAndHash(t *testing.T) {
	spec := mustParseCompose(t, "name: unresolved-url\nagents:\n  reviewer:\n    scheduler:\n      script:\n        url: ./scheduler.js\n")
	normalized, err := Normalize(spec, NormalizeOptions{ComposePath: "/project/agent-compose.yml"})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if !normalized.Agents[0].Scheduler.HasScript() {
		t.Fatal("unresolved source should retain HasScript semantics")
	}
	if _, err := normalized.Hash(); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("Hash error = %v", err)
	}
	if _, err := normalized.MarshalCanonicalYAML(false); err == nil || !strings.Contains(err.Error(), "scheduler.script.url") {
		t.Fatalf("MarshalCanonicalYAML error = %v", err)
	}
}

func TestSpecHashNormalizesDefaults(t *testing.T) {
	omitted := mustNormalizeCompose(t, `
name: defaults-hash
agents:
  reviewer:
    provider: codex
`, nil)
	explicit := mustNormalizeCompose(t, `
name: defaults-hash
agents:
  reviewer:
    provider: codex
    driver:
      docker: {}
`, nil)

	if got, want := mustHash(t, omitted), mustHash(t, explicit); got != want {
		t.Fatalf("default hash mismatch: %s != %s", got, want)
	}
}

func TestSpecHashSupportsJSONInputOrder(t *testing.T) {
	yamlSpec := mustNormalizeCompose(t, `
name: json-hash
variables:
  A: one
agents:
  reviewer:
    provider: codex
`, nil)
	jsonSpec := mustNormalizeCompose(t, `{"agents":{"reviewer":{"provider":"codex"}},"variables":{"A":"one"},"name":"json-hash"}`, nil)

	if got, want := mustHash(t, yamlSpec), mustHash(t, jsonSpec); got != want {
		t.Fatalf("YAML/JSON hash mismatch: %s != %s", got, want)
	}
}

func mustNormalizeCompose(t *testing.T, raw string, env map[string]string) *NormalizedProjectSpec {
	t.Helper()
	spec := mustParseCompose(t, raw)
	normalized, err := Normalize(spec, NormalizeOptions{Env: env})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	return normalized
}

func mustHash(t *testing.T, spec *NormalizedProjectSpec) string {
	t.Helper()
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	return hash
}
