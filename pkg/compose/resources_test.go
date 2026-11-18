package compose

import (
	"strings"
	"testing"
)

func TestNormalizeSandboxResourcesSpec(t *testing.T) {
	cpus := func(value float64) *float64 { return &value }
	tests := []struct {
		name    string
		spec    *SandboxResourcesSpec
		want    *NormalizedSandboxResourcesSpec
		wantErr string
	}{
		{name: "undeclared", spec: nil, want: nil},
		{name: "cpu and memory", spec: &SandboxResourcesSpec{CPUs: cpus(2), Memory: "4Gi"}, want: &NormalizedSandboxResourcesSpec{CPUMillis: 2000, MemoryMiB: 4096}},
		{name: "fractional cpu", spec: &SandboxResourcesSpec{CPUs: cpus(0.25)}, want: &NormalizedSandboxResourcesSpec{CPUMillis: 250}},
		{name: "memory in Mi", spec: &SandboxResourcesSpec{Memory: " 512Mi "}, want: &NormalizedSandboxResourcesSpec{MemoryMiB: 512}},
		{name: "empty block", spec: &SandboxResourcesSpec{}, wantErr: "declare cpus, memory, or both"},
		{name: "zero cpu", spec: &SandboxResourcesSpec{CPUs: cpus(0)}, wantErr: "resources.cpus"},
		{name: "negative cpu", spec: &SandboxResourcesSpec{CPUs: cpus(-1)}, wantErr: "positive"},
		{name: "cpu finer than millicore", spec: &SandboxResourcesSpec{CPUs: cpus(0.0005)}, wantErr: "thousandth"},
		{name: "cpu too large", spec: &SandboxResourcesSpec{CPUs: cpus(2048)}, wantErr: "exceeds"},
		{name: "bare memory number", spec: &SandboxResourcesSpec{Memory: "4096"}, wantErr: "needs a binary unit"},
		{name: "decimal memory unit", spec: &SandboxResourcesSpec{Memory: "4G"}, wantErr: "needs a binary unit"},
		{name: "fractional memory", spec: &SandboxResourcesSpec{Memory: "1.5Gi"}, wantErr: "whole number"},
		{name: "memory too small", spec: &SandboxResourcesSpec{Memory: "32Mi"}, wantErr: "minimum"},
		{name: "memory too large", spec: &SandboxResourcesSpec{Memory: "2048Gi"}, wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSandboxResourcesSpec("agents.worker.policy.resources", tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("normalized = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPolicyResourcesNormalizeFromYAML(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: resources
policies:
  small:
    resources:
      cpus: 0.5
      memory: 1Gi
agents:
  inline:
    provider: codex
    policy:
      resources:
        cpus: 2
        memory: 4Gi
  shared_a:
    provider: codex
    policy: small
  shared_b:
    provider: codex
    policy: small
`, nil)
	byName := map[string]NormalizedAgentSpec{}
	for _, agent := range spec.Agents {
		byName[agent.Name] = agent
	}
	if got := byName["inline"].PolicyBlock().Resources; got == nil || *got != (NormalizedSandboxResourcesSpec{CPUMillis: 2000, MemoryMiB: 4096}) {
		t.Fatalf("inline resources = %+v", got)
	}
	a, b := byName["shared_a"].PolicyBlock().Resources, byName["shared_b"].PolicyBlock().Resources
	if a == nil || *a != (NormalizedSandboxResourcesSpec{CPUMillis: 500, MemoryMiB: 1024}) {
		t.Fatalf("library resources = %+v", a)
	}
	if a == b {
		t.Fatal("agents referencing one library policy share a Resources pointer; an edit through one would change the other")
	}
}

func TestPolicyResourcesChangeSpecHashOnlyWhenDeclared(t *testing.T) {
	undeclared := mustNormalizeCompose(t, `
name: resources-hash
agents:
  worker:
    provider: codex
    policy:
      stopped_runtime_policy: remove
`, nil)
	canonical, err := undeclared.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON: %v", err)
	}
	if strings.Contains(string(canonical), `"resources"`) {
		t.Fatalf("undeclared resources leaked into canonical JSON: %s", canonical)
	}
	small := mustNormalizeCompose(t, `
name: resources-hash
agents:
  worker:
    provider: codex
    policy:
      stopped_runtime_policy: remove
      resources:
        memory: 1Gi
`, nil)
	large := mustNormalizeCompose(t, `
name: resources-hash
agents:
  worker:
    provider: codex
    policy:
      stopped_runtime_policy: remove
      resources:
        memory: 2Gi
`, nil)
	hashes := map[string]string{"undeclared": mustHash(t, undeclared), "small": mustHash(t, small), "large": mustHash(t, large)}
	if hashes["undeclared"] == hashes["small"] || hashes["small"] == hashes["large"] {
		t.Fatalf("resources must change the spec hash: %v", hashes)
	}
}

func TestPolicyResourcesRejectFractionalCPUsOnMicroVMs(t *testing.T) {
	compose := func(driver, cpus string) string {
		return `
name: resources-microvm
agents:
  worker:
    provider: codex
    driver:
      ` + driver + `: {}
    policy:
      resources:
        cpus: ` + cpus + `
`
	}
	for _, driver := range []string{DriverBoxlite, DriverMicrosandbox} {
		_, err := Normalize(mustParseCompose(t, compose(driver, "1.5")), NormalizeOptions{})
		if err == nil || !strings.Contains(err.Error(), "agents.worker.policy.resources.cpus") || !strings.Contains(err.Error(), "whole vCPUs") {
			t.Fatalf("%s: Normalize error = %v, want a whole-vCPU rejection naming the field", driver, err)
		}
		if _, err := Normalize(mustParseCompose(t, compose(driver, "2")), NormalizeOptions{}); err != nil {
			t.Fatalf("%s: whole vCPUs rejected: %v", driver, err)
		}
	}
	if _, err := Normalize(mustParseCompose(t, compose(DriverDocker, "1.5")), NormalizeOptions{}); err != nil {
		t.Fatalf("docker: fractional cpus rejected: %v", err)
	}
}

func TestPolicyResourcesRejectEmptyBlockFromYAML(t *testing.T) {
	spec := mustParseCompose(t, `
name: resources-empty
agents:
  worker:
    provider: codex
    policy:
      resources: {}
`)
	if _, err := Normalize(spec, NormalizeOptions{}); err == nil || !strings.Contains(err.Error(), "agents.worker.policy.resources") {
		t.Fatalf("Normalize error = %v, want an empty-block rejection", err)
	}
}
