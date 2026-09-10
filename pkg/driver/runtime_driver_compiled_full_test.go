//go:build linux && cgo && boxlitecgo && microsandboxcgo && !k8scompose

package driver

import (
	"reflect"
	"testing"
)

func TestFullRuntimeDriverCompiledConstraintFixture(t *testing.T) {
	want := []string{
		RuntimeDriverDocker,
		RuntimeDriverBoxlite,
		RuntimeDriverMicrosandbox,
	}
	if got := CompiledRuntimeDrivers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CompiledRuntimeDrivers() = %v, want full Linux capability %v", got, want)
	}
}
