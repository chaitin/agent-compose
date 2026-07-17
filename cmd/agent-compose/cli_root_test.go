package main

import (
	"agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"testing"

	"connectrpc.com/connect"
)

func TestMain(m *testing.M) {
	clearDaemonTestEnv()
	os.Exit(m.Run())
}

func TestVersionCommandJSONPrintsStableBuildInfo(t *testing.T) {
	oldVersion := config.BuildVersion
	config.BuildVersion = "test-json-version"
	t.Cleanup(func() { config.BuildVersion = oldVersion })

	stdout, stderr, runCount, err := executeCommand("--json", "version")
	if err != nil {
		t.Fatalf("--json version command returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("--json version stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &fields); err != nil {
		t.Fatalf("--json version output is not JSON: %v\n%s", err, stdout)
	}
	if len(fields) != 4 {
		t.Fatalf("--json version fields = %v, want exactly version/os/arch/compiled_drivers", fields)
	}
	for _, name := range []string{"version", "os", "arch", "compiled_drivers"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("--json version fields = %v, missing %q", fields, name)
		}
	}

	var got buildInfo
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode build info: %v", err)
	}
	if got.Version != config.BuildVersion || got.OS != runtime.GOOS || got.Arch != runtime.GOARCH || !reflect.DeepEqual(got.CompiledDrivers, driverpkg.CompiledRuntimeDrivers()) {
		t.Fatalf("build info = %#v, want version=%q os=%q arch=%q drivers=%v", got, config.BuildVersion, runtime.GOOS, runtime.GOARCH, driverpkg.CompiledRuntimeDrivers())
	}
}

func TestCurrentBuildInfoOwnsCompiledDriverSlice(t *testing.T) {
	first := currentBuildInfo()
	if len(first.CompiledDrivers) == 0 {
		t.Fatal("currentBuildInfo compiled drivers is empty")
	}
	first.CompiledDrivers[0] = "mutated"

	second := currentBuildInfo()
	want := driverpkg.CompiledRuntimeDrivers()
	if !reflect.DeepEqual(second.CompiledDrivers, want) {
		t.Fatalf("compiled drivers after caller mutation = %v, want %v", second.CompiledDrivers, want)
	}
}

func TestCommandExitErrorForConnectClassifiesRPCFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "invalid argument", err: connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bad request")), code: exitCodeUsage},
		{name: "not found", err: connect.NewError(connect.CodeNotFound, fmt.Errorf("missing")), code: exitCodeUsage},
		{name: "failed precondition", err: connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("stopped")), code: exitCodeUsage},
		{name: "unavailable", err: connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon down")), code: exitCodeUnavailable},
		{name: "unsupported", err: connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stats unsupported")), code: exitCodeUnsupported},
		{name: "ordinary failure", err: connect.NewError(connect.CodeInternal, fmt.Errorf("boom")), code: exitCodeGeneral},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := commandExitErrorForConnect(fmt.Errorf("operation: %w", tc.err))
			if got := commandExitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d; err=%v", got, tc.code, err)
			}
		})
	}
}
