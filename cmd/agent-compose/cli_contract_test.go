package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliContract is deliberately limited to values that are part of the public
// CLI surface. Help text is intentionally not snapshotted: wording can change
// without breaking scripts, while command names, flags, defaults and aliases
// cannot.
type cliContract struct {
	Commands []cliCommandContract         `json:"commands"`
	ExitCode map[string]int               `json:"exit_codes"`
	JSON     map[string]map[string]string `json:"json_fields"`
}

type cliCommandContract struct {
	Path  string            `json:"path"`
	Use   string            `json:"use"`
	Flags []cliFlagContract `json:"flags,omitempty"`
}

type cliFlagContract struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default"`
	Optional  string `json:"optional,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

func TestPublicCLIContractSnapshot(t *testing.T) {
	root := newRootCommand(nil, nil, func(context.Context) error { return nil })
	got := cliContract{
		Commands: collectCLICommandContracts(root),
		ExitCode: map[string]int{
			"success":     0,
			"general":     exitCodeGeneral,
			"usage":       exitCodeUsage,
			"unavailable": exitCodeUnavailable,
			"unsupported": exitCodeUnsupported,
		},
		JSON: collectCLIJSONContracts(),
	}
	path := filepath.Join("testdata", "cli-contract.json")
	if os.Getenv("UPDATE_CLI_CONTRACT") == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLI contract snapshot %s: %v (run UPDATE_CLI_CONTRACT=1 to update intentionally)", path, err)
	}
	var want cliContract
	if err := json.Unmarshal(wantData, &want); err != nil {
		t.Fatalf("decode CLI contract snapshot: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public CLI contract changed\n got: %s\nwant: %s\nIf intentional, review and run UPDATE_CLI_CONTRACT=1", contractJSON(got), string(wantData))
	}
	stdout, stderr, _, code := executeCLICommand("--json", "version")
	if code != 0 || stderr != "" {
		t.Fatalf("version --json behavior = code %d stderr %q", code, stderr)
	}
	var version map[string]any
	if err := json.Unmarshal([]byte(stdout), &version); err != nil {
		t.Fatalf("version --json is invalid JSON: %v", err)
	}
	assertJSONFields(t, version, got.JSON["build_info"])

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"version"}, 0},
		{"unknown flag", []string{"--contract-unknown"}, exitCodeUsage},
		{"invalid global value", []string{"--timeout", "-1s", "version"}, exitCodeUsage},
	} {
		t.Run("exit/"+tc.name, func(t *testing.T) {
			_, _, _, gotCode := executeCLICommand(tc.args...)
			if gotCode != tc.want {
				t.Fatalf("exit code = %d, want %d", gotCode, tc.want)
			}
		})
	}
}

func collectCLICommandContracts(root *cobra.Command) []cliCommandContract {
	var out []cliCommandContract
	var visit func(*cobra.Command, string)
	visit = func(cmd *cobra.Command, parent string) {
		path := cmd.Name()
		if parent != "" {
			path = parent + " " + path
		}
		if path != root.Name() {
			var flags []cliFlagContract
			cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
				flags = append(flags, cliFlagContract{Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(), Default: flag.DefValue, Optional: flag.NoOptDefVal, Hidden: flag.Hidden})
			})
			cmd.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
				flags = append(flags, cliFlagContract{Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(), Default: flag.DefValue, Optional: flag.NoOptDefVal, Hidden: flag.Hidden})
			})
			sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
			out = append(out, cliCommandContract{Path: path, Use: cmd.Use, Flags: flags})
		}
		children := append([]*cobra.Command(nil), cmd.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			visit(child, path)
		}
	}
	visit(root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func collectCLIJSONContracts() map[string]map[string]string {
	values := map[string]any{
		"build_info": buildInfo{}, "agent_list": composeAgentListOutput{},
		"cache_list": composeCacheListOutput{}, "cache_inspect": composeCacheInspectOutput{},
		"cache_operation": composeCacheOperationOutput{}, "exec": composeExecOutput{},
		"image_list": composeImageListOutput{}, "image_inspect": composeImageInspectOutput{},
		"image_pull": composeImagePullOutput{}, "image_build": composeImageBuildOutput{},
		"image_remove": composeImageRemoveOutput{}, "run_jupyter": composeRunJupyterOutput{},
		"logs": composeLogsOutput{}, "ps": composePSOutput{}, "up": composeUpOutput{},
		"down": composeDownOutput{}, "project_list": composeProjectListOutput{},
		"project": composeProjectOutput{}, "agent_inspect": composeAgentInspectOutput{},
		"scheduler_prune": composeSchedulerPruneOutput{}, "run": composeRunOutput{},
		"sandbox_list": composePSSandboxOutput{}, "sandbox_prune": composeSandboxPruneOutput{},
		"sandbox_logs": composeSandboxLogsOutput{}, "sandbox_action": composeSandboxActionOutput{},
		"sandbox": composeSandboxOutput{}, "stats": composeStatsOutput{},
		"project_stats": composeProjectStatsOutput{}, "scheduler_list": composeSchedulerListOutput{},
		"scheduler_inspect": composeSchedulerInspectOutput{}, "scheduler_runs": composeSchedulerRunsOutput{},
		"scheduler_logs": composeSchedulerLogsOutput{}, "volume_list": composeVolumeListOutput{},
		"volume_inspect": composeVolumeInspectOutput{}, "volume_create": composeVolumeCreateOutput{},
		"volume_remove": composeVolumeRemoveOutput{}, "volume_prune": composeVolumePruneOutput{},
	}
	out := make(map[string]map[string]string, len(values))
	for name, value := range values {
		out[name] = jsonStructFields(value)
	}
	return out
}

func jsonStructFields(value any) map[string]string {
	typ := reflect.TypeOf(value)
	fields := make(map[string]string)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = jsonTypeName(field.Type)
	}
	return fields
}

func jsonTypeName(typ reflect.Type) string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct, reflect.Interface:
		return "object"
	default:
		return typ.Kind().String()
	}
}

func assertJSONFields(t *testing.T, value map[string]any, schema map[string]string) {
	t.Helper()
	if len(value) != len(schema) {
		t.Fatalf("JSON fields = %v, want %v", value, schema)
	}
	for name, wantType := range schema {
		raw, ok := value[name]
		if !ok {
			t.Fatalf("JSON missing field %q", name)
		}
		if got := jsonTypeName(reflect.TypeOf(raw)); got != wantType {
			t.Fatalf("JSON field %q type = %s, want %s", name, got, wantType)
		}
	}
}

func contractJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}
