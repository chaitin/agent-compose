package driver

import (
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestRuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	testExecOutputFilterWorkflows(t)
	testJupyterGuestCoverageWorkflow(t)
	testCredentialSurfaceWorkflow(t)
}

// testCredentialSurfaceWorkflow drives the credential surface from a declared
// name to a microVM secret binding: classification, the declared/runtime trust
// asymmetry in guest environment, injection planning for a scoped handle, and
// the secret binding that consumes the plan. The last step is the real seam
// between the credential model and this package, so it is asserted here rather
// than only inside either package.
func testCredentialSurfaceWorkflow(t *testing.T) {
	t.Helper()
	TestCredentialEnvNameEnumeratesEveryCredentialForm(t)
	TestCredentialEnvNameRejectsUnrecognizedNames(t)
	TestIsHeldCredentialNameOnlyClaimsAbsorbedCredentials(t)
	TestSandboxEnvMapKeepsNonLLMSecretEnv(t)
	TestSandboxEnvMapDropsDeclaredProviderEndpoints(t)
	TestSandboxEnvMapTrustAsymmetryIsExplicit(t)
	TestSandboxEnvMapPassesThroughUnabsorbedCredentials(t)
	TestSandboxSecretBindingsNormalizesSpecs(t)
	TestSandboxSecretBindingsRejectsUnsafeSpecs(t)
	TestSandboxSecretBindingsRejectsDuplicateEnvironmentVariables(t)
	TestSandboxSecretBindingsEmptyForNoSpecs(t)

	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	_, handle, err := credentials.NewHandle(credentials.NewHandleRequest{
		Kind:      credentials.KindGit,
		EnvName:   "GIT_TOKEN",
		SandboxID: "sbx-1",
		Scope: credentials.Scope{
			Endpoint: "git.example.com",
			Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
		},
	}, now)
	if err != nil {
		t.Fatalf("NewHandle() error = %v", err)
	}
	snapshot := credentials.NewSnapshot([]credentials.Material{{Handle: handle, Value: "secret-truth"}})
	specs, err := credentials.PlanInjection(snapshot, []credentials.InjectionRequest{{
		HandleID: handle.ID,
		Request: credentials.Request{
			Endpoint: "git.example.com",
			Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
		},
	}}, now)
	if err != nil {
		t.Fatalf("PlanInjection() error = %v", err)
	}
	bindings, err := sandboxSecretBindings(specs)
	if err != nil {
		t.Fatalf("sandboxSecretBindings() rejected a planned spec: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("sandboxSecretBindings() = %#v, want exactly one binding", bindings)
	}
	binding := bindings[0]
	if binding.EnvVar != "GIT_TOKEN" || binding.Value != "secret-truth" {
		t.Fatalf("binding = %#v, want the planned environment variable and credential truth", binding)
	}
	if !strings.HasPrefix(binding.Placeholder, credentials.PlaceholderPrefix) || binding.Placeholder == credentials.PlaceholderPrefix {
		t.Fatalf("binding placeholder = %q, want a prefix followed by a fingerprint", binding.Placeholder)
	}
	if len(binding.AllowHosts) != 1 || binding.AllowHosts[0] != "git.example.com" || !binding.RequireTLS {
		t.Fatalf("binding scope = %#v, want only the planned host with TLS required", binding)
	}
}

func TestIntegrationRuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	testExecOutputFilterWorkflows(t)
	testJupyterGuestCoverageWorkflow(t)
	testCredentialSurfaceWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
}

func TestE2ERuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	testExecOutputFilterWorkflows(t)
	testJupyterGuestCoverageWorkflow(t)
	testCredentialSurfaceWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
}
