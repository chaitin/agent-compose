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
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
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
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
	testJupyterGuestCoverageWorkflow(t)
	testCredentialSurfaceWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
	TestNewSandboxNetworkPolicyNormalizesAndValidates(t)
	TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam(t)
	TestSandboxNetworkPolicyDenyByDefault(t)
	TestSandboxNetworkPolicyEndpointOrdering(t)
	TestSandboxNetworkPolicyRejectsMalformedHost(t)
	TestSandboxNetworkEnforcementFor(t)
	TestRequireSandboxNetworkEnforcementRefuseModeAborts(t)
	TestRequireSandboxNetworkEnforcementRejectsMalformedPolicy(t)
	TestDecideSandboxNetworkEgressUsesTheEgressModel(t)
	TestSandboxNetworkEgressPolicyPermissiveIsAllowAll(t)
	TestPlanMicrosandboxNetwork(t)
	TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers(t)
}

func TestE2ERuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
	testJupyterGuestCoverageWorkflow(t)
	testCredentialSurfaceWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
	TestNewSandboxNetworkPolicyNormalizesAndValidates(t)
	TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam(t)
	TestSandboxNetworkPolicyDenyByDefault(t)
	TestSandboxNetworkPolicyEndpointOrdering(t)
	TestSandboxNetworkPolicyRejectsMalformedHost(t)
	TestSandboxNetworkEnforcementFor(t)
	TestRequireSandboxNetworkEnforcementRefuseModeAborts(t)
	TestRequireSandboxNetworkEnforcementRejectsMalformedPolicy(t)
	TestDecideSandboxNetworkEgressUsesTheEgressModel(t)
	TestSandboxNetworkEgressPolicyPermissiveIsAllowAll(t)
	TestPlanMicrosandboxNetwork(t)
	TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers(t)
}

// The network policy and capability seam decides whether a declared policy is
// enforced, refused, or ignored, so it must carry coverage in the broader
// shapes and not only in the unit shape.
func TestIntegrationRuntimeNetworkPolicyWorkflows(t *testing.T) {
	t.Run("plan microsandbox network", TestPlanMicrosandboxNetwork)
	t.Run("plan microsandbox deny", TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers)
	t.Run("network egress decision uses the egress model", TestDecideSandboxNetworkEgressUsesTheEgressModel)
	t.Run("enforcement gate fails closed when configured to refuse", TestRequireSandboxNetworkEnforcementRefuseModeAborts)
	t.Run("enforcement gate rejects a malformed policy", TestRequireSandboxNetworkEnforcementRejectsMalformedPolicy)
	t.Run("permissive policy is allow-all", TestSandboxNetworkEgressPolicyPermissiveIsAllowAll)
	t.Run("enforcement matrix", TestSandboxNetworkEnforcementFor)
	t.Run("new policy normalizes and validates", TestNewSandboxNetworkPolicyNormalizesAndValidates)
	t.Run("policy deny by default", TestSandboxNetworkPolicyDenyByDefault)
	t.Run("declaration seam", TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam)
	t.Run("policy endpoint ordering", TestSandboxNetworkPolicyEndpointOrdering)
	t.Run("policy rejects a malformed host", TestSandboxNetworkPolicyRejectsMalformedHost)
	t.Run("compose declaration seam", TestSandboxNetworkPolicyFromComposeDeclaration)
	t.Run("undeclared compose block", TestSandboxNetworkPolicyFromUndeclaredComposeBlock)
	t.Run("egress capability reports engine auto allow", TestEgressCapabilityReportsEngineAutoAllow)
	t.Run("docker capabilities match host config", TestDockerCapabilitiesMatchHostConfig)
	t.Run("microvm capabilities match shared resources", TestMicroVMCapabilitiesMatchSharedResourceConfig)
	t.Run("runtime capability facts are probe free", TestRuntimeCapabilityFactsAreStaticAndProbeFree)
	t.Run("runtime capability facts cover every dimension", TestRuntimeCapabilityFactsCoverEveryDimension)
	t.Run("stopped runtime retention declaration", TestStoppedRuntimeRetentionDeclarationMatchesImplementation)
	t.Run("no new privileges warning", TestExecOutputFilterReportsNoNewPrivilegesWarning)
	t.Run("security facts merge", TestExecSecurityFactsMergeAccumulates)
	t.Run("interaction writer projects frames", TestDockerInteractionWriterProjectsFramesAndReportsStderr)
	t.Run("report undeclared is not deny", TestReportAgentNetworkPolicyUndeclaredIsNotDeny)
	t.Run("report allow list does not restrict", TestReportAgentNetworkPolicyAllowListWithoutDenyDoesNotRestrict)
	t.Run("report deny list", TestReportAgentNetworkPolicyDenyListIsReported)
	t.Run("report mediated deny is enforced", TestReportAgentNetworkPolicyMediatedDenyIsEnforced)
	t.Run("report names what is unapplied", TestReportAgentNetworkPolicyNamesWhatTheMechanismLeavesUnapplied)
	t.Run("report refuses invalid declaration", TestReportAgentNetworkPolicyRefusesAnInvalidDeclaration)
	t.Run("admission report agrees with creation gate", TestAdmissionReportAgreesWithTheCreationGate)
	t.Run("admission report cannot see unapplied deny domains", TestAdmissionReportCannotSeeUnappliedDenyDomains)
	t.Run("report names every mechanism", TestReportAgentNetworkPolicyNamesEveryMechanism)
}

func TestE2ERuntimeNetworkPolicyWorkflows(t *testing.T) {
	TestIntegrationRuntimeNetworkPolicyWorkflows(t)
}
