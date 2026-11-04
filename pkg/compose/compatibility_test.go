package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const stableYAMLBaseline = "v2608.1.0"
const stableYAMLBaselineCommit = "243274c1dd3e3c502b548c261be16bcea51fd11e"

type stableYAMLContract struct {
	Baseline     string                    `json:"baseline"`
	SourceCommit string                    `json:"source_commit"`
	Fields       []stableYAMLContractField `json:"fields"`
}

type stableYAMLContractField struct {
	Path     string `json:"path"`
	GoType   string `json:"go_type"`
	Optional bool   `json:"optional"`
}

type stableYAMLContractChanges struct {
	Breaking  []string
	Additions []stableYAMLContractField
}

func TestStableYAMLContract(t *testing.T) {
	contractPath := filepath.Join("testdata", "compat", "contract-v2608.1.0.json")
	current := projectSpecYAMLContract()
	data, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read stable YAML contract: %v", err)
	}
	var baseline stableYAMLContract
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode stable YAML contract: %v", err)
	}
	if baseline.Baseline != stableYAMLBaseline {
		t.Fatalf("contract baseline = %q, want %q", baseline.Baseline, stableYAMLBaseline)
	}
	if baseline.SourceCommit != stableYAMLBaselineCommit {
		t.Fatalf("contract source commit = %q, want %q", baseline.SourceCommit, stableYAMLBaselineCommit)
	}

	changes := compareStableYAMLContracts(baseline, current)
	for _, message := range changes.Breaking {
		t.Error(message)
	}
	if len(changes.Additions) == 0 || len(changes.Breaking) > 0 {
		return
	}
	if os.Getenv("UPDATE_STABLE_YAML_CONTRACT") != "1" {
		for _, field := range changes.Additions {
			t.Errorf("additive YAML field %q is not recorded in the stable contract; review it and run UPDATE_STABLE_YAML_CONTRACT=1 go test ./pkg/compose -run '^TestStableYAMLContract$'", field.Path)
		}
		return
	}

	// Updating only merges reviewed additions. Existing stable entries cannot
	// be removed or rewritten by the update path.
	baseline.Fields = append(baseline.Fields, changes.Additions...)
	sort.Slice(baseline.Fields, func(i, j int) bool { return baseline.Fields[i].Path < baseline.Fields[j].Path })
	data, err = json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatalf("marshal stable YAML contract: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(contractPath, data, 0o644); err != nil {
		t.Fatalf("write stable YAML contract: %v", err)
	}
}

func TestStableYAMLContractChangeClassification(t *testing.T) {
	baseline := stableYAMLContract{Fields: []stableYAMLContractField{
		{Path: "agents.*.enabled", GoType: "optional<bool>", Optional: true},
		{Path: "agents.*.provider", GoType: "string", Optional: true},
	}}
	candidate := stableYAMLContract{Fields: []stableYAMLContractField{
		{Path: "agents.*.enabled", GoType: "string", Optional: true},
		{Path: "agents.*.model", GoType: "string", Optional: true},
	}}

	changes := compareStableYAMLContracts(baseline, candidate)
	if len(changes.Breaking) != 2 {
		t.Fatalf("breaking changes = %#v, want removed provider and changed enabled type", changes.Breaking)
	}
	if len(changes.Additions) != 1 || changes.Additions[0].Path != "agents.*.model" {
		t.Fatalf("additions = %#v, want agents.*.model", changes.Additions)
	}
}

func compareStableYAMLContracts(baseline, candidate stableYAMLContract) stableYAMLContractChanges {
	candidateByPath := make(map[string]stableYAMLContractField, len(candidate.Fields))
	for _, field := range candidate.Fields {
		candidateByPath[field.Path] = field
	}
	baselineByPath := make(map[string]stableYAMLContractField, len(baseline.Fields))
	changes := stableYAMLContractChanges{}
	for _, stable := range baseline.Fields {
		baselineByPath[stable.Path] = stable
		current, ok := candidateByPath[stable.Path]
		if !ok {
			changes.Breaking = append(changes.Breaking, fmt.Sprintf("stable YAML field %q was removed or renamed", stable.Path))
			continue
		}
		if current.GoType != stable.GoType {
			changes.Breaking = append(changes.Breaking, fmt.Sprintf("stable YAML field %q type changed from %q to %q", stable.Path, stable.GoType, current.GoType))
		}
		if stable.Optional && !current.Optional {
			changes.Breaking = append(changes.Breaking, fmt.Sprintf("stable YAML field %q changed from optional to required", stable.Path))
		}
	}
	for _, field := range candidate.Fields {
		if _, ok := baselineByPath[field.Path]; !ok {
			changes.Additions = append(changes.Additions, field)
		}
	}
	return changes
}

// The v2608.1.0 baseline accepted a declarative trigger without a prompt and
// compiled it to "Run agent <name>.". Requiring the prompt is a deliberate
// breaking change, and it cannot surface through contract-v2608.1.0.json: the
// field still carries omitempty, so the reflected contract sees it as optional
// and compareStableYAMLContracts has nothing to report. The fixtures are updated
// to declare prompts, which would otherwise hide the regression, so pin the
// rejection of a baseline-shaped file here.
func TestStableYAMLTriggerWithoutPromptIsNowRejected(t *testing.T) {
	spec := mustParseCompose(t, `
name: legacy-trigger
agents:
  reviewer:
    scheduler:
      triggers:
        - name: daily
          cron: "0 9 * * *"
          timezone: UTC
`)

	_, err := Normalize(spec, NormalizeOptions{})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Normalize error = %v, want ValidationError", err)
	}
	if got, want := validationErr.Path, "agents.reviewer.scheduler.triggers[0].prompt"; got != want {
		t.Fatalf("validation path = %q, want %q", got, want)
	}
	if got, want := validationErr.Message, "trigger prompt is required"; got != want {
		t.Fatalf("validation message = %q, want %q", got, want)
	}
}

func TestStableYAMLFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "compat", "v2608.1.0", "*.yaml"))
	if err != nil {
		t.Fatalf("find stable YAML fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no stable YAML fixtures found")
	}
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), func(t *testing.T) {
			spec, err := ParseFile(path)
			if err != nil {
				t.Fatalf("parse %s fixture: %v", stableYAMLBaseline, err)
			}
			normalized, err := Normalize(spec, NormalizeOptions{
				ComposePath: path,
				Env: map[string]string{
					"API_TOKEN":  "fixture-token",
					"MODEL_NAME": "gpt-5",
				},
			})
			if err != nil {
				t.Fatalf("normalize %s fixture: %v", stableYAMLBaseline, err)
			}
			assertStableFixtureSemantics(t, filepath.Base(path), normalized)
		})
	}
}

func assertStableFixtureSemantics(t *testing.T, name string, normalized *NormalizedProjectSpec) {
	t.Helper()
	switch name {
	case "defaults.yaml":
		if len(normalized.Agents) != 1 || !normalized.Agents[0].Enabled {
			t.Fatalf("omitted agent enabled did not default to true: %#v", normalized.Agents)
		}
		scheduler := normalized.Agents[0].Scheduler
		if scheduler == nil || !scheduler.Enabled || scheduler.SandboxPolicy != "new" || scheduler.ConcurrencyPolicy != "skip" {
			t.Fatalf("stable scheduler defaults changed: %#v", scheduler)
		}
		if workspace := normalized.Workspaces["source"]; workspace.Target != "." {
			t.Fatalf("omitted workspace target = %q, want .", workspace.Target)
		}
	case "full.yaml":
		if len(normalized.Agents) != 1 || normalized.Agents[0].Enabled {
			t.Fatalf("explicit disabled agent changed: %#v", normalized.Agents)
		}
		if normalized.Agents[0].Model != "gpt-5" {
			t.Fatalf("interpolated model = %q, want gpt-5", normalized.Agents[0].Model)
		}
		if got := normalized.Variables["API_TOKEN"]; got.Value != "fixture-token" || !got.Secret {
			t.Fatalf("normalized secret variable = %#v", got)
		}
	case "shorthand.yaml":
		agent := normalized.Agents[0]
		if got := agent.Env["MODE"].Value; got != "review" {
			t.Fatalf("scalar environment shorthand = %q, want review", got)
		}
		if len(agent.Volumes) != 1 || agent.Volumes[0].Target != "/workspace/cache" {
			t.Fatalf("volume short syntax changed: %#v", agent.Volumes)
		}
		if _, ok := agent.MCPServers["local-tools"]; !ok {
			t.Fatalf("scalar MCP reference was not preserved: %#v", agent.MCPServers)
		}
	case "script.yaml":
		scheduler := normalized.Agents[0].Scheduler
		if scheduler == nil || !strings.Contains(scheduler.Script, "scheduler.main") {
			t.Fatalf("inline scheduler script changed: %#v", scheduler)
		}
	default:
		t.Fatalf("fixture %q has no semantic assertions", name)
	}
}

func projectSpecYAMLContract() stableYAMLContract {
	fields := make([]stableYAMLContractField, 0)
	walkYAMLContract(reflect.TypeOf(ProjectSpec{}), "", &fields)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return stableYAMLContract{
		Baseline:     stableYAMLBaseline,
		SourceCommit: stableYAMLBaselineCommit,
		Fields:       fields,
	}
}

func walkYAMLContract(valueType reflect.Type, path string, fields *[]stableYAMLContractField) {
	valueType = indirectType(valueType)
	switch valueType.Kind() {
	case reflect.Map:
		walkYAMLContract(valueType.Elem(), path+".*", fields)
		return
	case reflect.Slice:
		walkYAMLContract(valueType.Elem(), path+"[]", fields)
		return
	case reflect.Struct:
	default:
		return
	}

	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		tag := field.Tag.Get("yaml")
		if tag == "" {
			continue
		}
		parts := strings.Split(tag, ",")
		if parts[0] == "-" || parts[0] == "" {
			continue
		}
		fieldPath := parts[0]
		if path != "" {
			fieldPath = path + "." + fieldPath
		}
		contractField := stableYAMLContractField{
			Path:     fieldPath,
			GoType:   contractType(field.Type),
			Optional: containsString(parts[1:], "omitempty"),
		}
		*fields = append(*fields, contractField)
		walkYAMLContract(field.Type, fieldPath, fields)
	}
}

func indirectType(valueType reflect.Type) reflect.Type {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	return valueType
}

func contractType(valueType reflect.Type) string {
	switch valueType.Kind() {
	case reflect.Pointer:
		return "optional<" + contractType(valueType.Elem()) + ">"
	case reflect.Map:
		return fmt.Sprintf("mapping<%s,%s>", contractType(valueType.Key()), contractType(valueType.Elem()))
	case reflect.Slice:
		return "sequence<" + contractType(valueType.Elem()) + ">"
	case reflect.Struct:
		return "mapping<" + valueType.String() + ">"
	default:
		if valueType.Name() != "" {
			return valueType.String()
		}
		return valueType.Kind().String()
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
