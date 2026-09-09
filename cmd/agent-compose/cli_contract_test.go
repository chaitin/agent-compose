package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliContract is deliberately limited to values that are part of the public
// CLI surface. Help text is intentionally not snapshotted: wording can change
// without breaking scripts, while command names, flags, defaults and aliases
// cannot.
type cliContract struct {
	Commands []cliCommandContract `json:"commands"`
	ExitCode map[string]int       `json:"exit_codes"`
	JSON     map[string][]string  `json:"json_fields"`
}

type cliCommandContract struct {
	Path    string   `json:"path"`
	Aliases []string `json:"aliases,omitempty"`
	Flags   []string `json:"flags,omitempty"`
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
		JSON: map[string][]string{
			"version --json": {"version", "os", "arch", "compiled_drivers"},
		},
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
			var flags []string
			cmd.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
				flags = append(flags, flag.Name+"="+flag.DefValue)
			})
			sort.Strings(flags)
			aliases := append([]string(nil), cmd.Aliases...)
			sort.Strings(aliases)
			out = append(out, cliCommandContract{Path: path, Aliases: aliases, Flags: flags})
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

func contractJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}
