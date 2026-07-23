package adapters

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestNewRuntimeProviderValidatesConfiguredDefaultCompiled(t *testing.T) {
	if provider, err := NewRuntimeProvider(nil); err == nil || provider != nil || !strings.Contains(err.Error(), "config is required") {
		t.Fatalf("NewRuntimeProvider(nil) = %T, %v; want nil provider and config error", provider, err)
	}

	uncompiledDriver := firstUncompiledRuntimeDriver()
	if uncompiledDriver == "" {
		t.Skip("all recognized runtime drivers are compiled")
	}

	provider, err := NewRuntimeProvider(&appconfig.Config{RuntimeDriver: uncompiledDriver})
	if provider != nil {
		t.Fatalf("NewRuntimeProvider(%q) provider = %T, want nil", uncompiledDriver, provider)
	}
	if !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		t.Fatalf("NewRuntimeProvider(%q) error = %v, want ErrRuntimeDriverNotCompiled", uncompiledDriver, err)
	}
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("NewRuntimeProvider(%q) error = %v, want domain ErrUnsupported", uncompiledDriver, err)
	}
	var notCompiled *driverpkg.RuntimeDriverNotCompiledError
	if !errors.As(err, &notCompiled) || notCompiled.Driver != uncompiledDriver {
		t.Fatalf("NewRuntimeProvider(%q) typed error = %#v, want driver %q", uncompiledDriver, notCompiled, uncompiledDriver)
	}
}

func TestNewRuntimeProviderConstructionIsLazy(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "intentionally-missing")
	config := &appconfig.Config{
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		BoxliteHome:         filepath.Join(missingRoot, "boxlite-home"),
		BoxliteRuntimeDir:   filepath.Join(missingRoot, "boxlite-runtime"),
		MicrosandboxHome:    filepath.Join(missingRoot, "microsandbox-home"),
		MicrosandboxMSBPath: filepath.Join(missingRoot, "bin", "msb"),
		MicrosandboxLibPath: filepath.Join(missingRoot, "lib", "libmicrosandbox_go_ffi.so"),
	}

	provider, err := NewRuntimeProvider(config)
	if err != nil {
		t.Fatalf("NewRuntimeProvider() with unavailable native paths returned error: %v", err)
	}
	resolved, ok := provider.(*runtimeProvider)
	if !ok {
		t.Fatalf("NewRuntimeProvider() = %T, want *runtimeProvider", provider)
	}
	if len(resolved.runtimes) != 3 {
		t.Fatalf("registered runtimes = %d, want 3 lazy wrappers", len(resolved.runtimes))
	}
	if _, err := provider.ForDriver(driverpkg.RuntimeDriverDocker); err != nil {
		t.Fatalf("ForDriver(docker) after lazy construction returned error: %v", err)
	}
}

func TestRuntimeProviderForDriverValidationOrdering(t *testing.T) {
	runtime := &runtimeProviderTestRuntime{}

	t.Run("invalid name before configured lookup", func(t *testing.T) {
		provider := &runtimeProvider{runtimes: map[string]SandboxRuntime{"future-runtime": runtime}}
		_, err := provider.ForDriver(" Future-Runtime ")
		if err == nil || !strings.Contains(err.Error(), "unsupported agent-compose runtime driver") {
			t.Fatalf("ForDriver(invalid) error = %v, want unsupported driver name", err)
		}
		if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) || errors.Is(err, domain.ErrUnsupported) || strings.Contains(err.Error(), "not configured") {
			t.Fatalf("ForDriver(invalid) error = %v, want name validation before capability and lookup", err)
		}
	})

	t.Run("compiled capability before configured lookup", func(t *testing.T) {
		uncompiledDriver := firstUncompiledRuntimeDriver()
		if uncompiledDriver == "" {
			t.Skip("all recognized runtime drivers are compiled")
		}
		provider := &runtimeProvider{runtimes: map[string]SandboxRuntime{uncompiledDriver: runtime}}
		got, err := provider.ForDriver(uncompiledDriver)
		if got != nil || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
			t.Fatalf("ForDriver(%q) = %T, %v; want typed not-compiled error", uncompiledDriver, got, err)
		}
		if !errors.Is(err, domain.ErrUnsupported) {
			t.Fatalf("ForDriver(%q) error = %v, want domain ErrUnsupported", uncompiledDriver, err)
		}
		var notCompiled *driverpkg.RuntimeDriverNotCompiledError
		if !errors.As(err, &notCompiled) || notCompiled.Driver != uncompiledDriver {
			t.Fatalf("ForDriver(%q) typed error = %#v, want preserved driver error", uncompiledDriver, notCompiled)
		}
	})

	t.Run("configured lookup after compiled validation", func(t *testing.T) {
		provider := &runtimeProvider{runtimes: map[string]SandboxRuntime{}}
		got, err := provider.ForDriver(driverpkg.RuntimeDriverDocker)
		if got != nil || err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("ForDriver(docker) = %T, %v; want not-configured error", got, err)
		}
		if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
			t.Fatalf("ForDriver(docker) error = %v, compiled driver must reach configured lookup", err)
		}
	})

	t.Run("resolved alias returns configured runtime", func(t *testing.T) {
		provider := &runtimeProvider{runtimes: map[string]SandboxRuntime{driverpkg.RuntimeDriverDocker: runtime}}
		got, err := provider.ForDriver(" docker-engine ")
		if err != nil || got != runtime {
			t.Fatalf("ForDriver(docker-engine) = %T, %v; want configured Docker runtime", got, err)
		}
	})
}

func firstUncompiledRuntimeDriver() string {
	for _, driver := range []string{
		driverpkg.RuntimeDriverBoxlite,
		driverpkg.RuntimeDriverMicrosandbox,
		driverpkg.RuntimeDriverDocker,
	} {
		if !driverpkg.IsRuntimeDriverCompiled(driver) {
			return driver
		}
	}
	return ""
}

type runtimeProviderTestRuntime struct{}

func (*runtimeProviderTestRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (*runtimeProviderTestRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (*runtimeProviderTestRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (*runtimeProviderTestRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (*runtimeProviderTestRuntime) ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func TestDomainStreamFromDriver(t *testing.T) {
	tests := []struct {
		name   string
		stream driverpkg.StdioStream
		want   domain.StdioStream
	}{
		{name: "zero", want: domain.StdioStdout},
		{name: "stdout", stream: driverpkg.StdioStdout, want: domain.StdioStdout},
		{name: "stderr", stream: driverpkg.StdioStderr, want: domain.StdioStderr},
		{name: "unknown", stream: driverpkg.StdioStream("future"), want: domain.StdioStdout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainStreamFromDriver(tt.stream); got != tt.want {
				t.Fatalf("domainStreamFromDriver(%q) = %q, want %q", tt.stream, got, tt.want)
			}
		})
	}
}

func TestClassifyExecTerminationErrorMapsDriverSentinelToDomain(t *testing.T) {
	driverErr := fmt.Errorf("cancel exec: %w", driverpkg.ErrExecTerminationUnconfirmed)
	err := classifyExecTerminationError(driverErr)
	if !errors.Is(err, domain.ErrExecTerminationUnconfirmed) {
		t.Fatalf("classified error = %v, want domain termination sentinel", err)
	}
	if !errors.Is(err, driverpkg.ErrExecTerminationUnconfirmed) {
		t.Fatalf("classified error lost driver cause: %v", err)
	}
}
