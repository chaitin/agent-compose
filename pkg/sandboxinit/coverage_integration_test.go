package sandboxinit

import "testing"

// TestSandboxInitCoreIntegration runs the core init protocol and supervisor
// tests as one integration-shaped suite. These are host-level process and
// protocol tests; the unit shape covers the same code, while this suite makes
// the integration coverage gate exercise the complete boundary.
func TestSandboxInitCoreIntegration(t *testing.T) {
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"init/ping-list", TestInitPingAndList},
		{"init/exec-streams", TestInitExecStreamsStdoutStderrAndExit},
		{"init/immediate-exit", TestInitImmediateExitDoesNotLoseTheExitFrame},
		{"init/concurrent-exec", TestInitConcurrentExecSessions},
		{"init/kill-tree", TestInitKillTreeByMarker},
		{"init/tty", TestInitTTYStreamsAndExits},
		{"init/interactive", TestInitInteractiveStdinEcho},
		{"init/signal", TestInitSignalTerminatesSession},
		{"init/large-output", TestInitLargeOutputDoesNotDeadlock},
		{"init/shutdown", TestInitShutdownStopsTheMainProcess},
		{"init/reject-version", TestInitRejectsUnsupportedProtocolVersion},
		{"init/main-status", TestInitExitsWithTheMainProcessStatus},
		{"bridge/protocol", TestBridgeCarriesTheProtocolOverStdio},
		{"audit/generation", TestEgressSupervisorStampsTheLaunchGeneration},
		{"audit/jsonl", TestEgressAuditLogWritesTheDaemonSchema},
		{"audit/append", TestEgressAuditLogAppendsWithoutOverwritingExistingLines},
		{"dns/synthetic", TestInitEgressPolicyDNSAnswersSyntheticAddresses},
		{"dns/engine-host", TestInitEgressPolicyDNSPassesEngineHostThrough},
		{"relay/env", TestLoadEgressRelayTargetRemovesItsEnvironment},
		{"relay/configure", TestConfigureRelayRequiresTheTokenAndWaits},
		{"relay/bridge", TestRelayBridgesTheDuplicatedSocketToTheSupervisor},
		{"relay/reject-peer", TestAcceptRelayedPeerIgnoresAnotherLoopbackClient},
		{"relay/requires-socket", TestLoadEgressRelayTargetRequiresASocket},
		{"audit/connect", TestInitEgressAuditReportsConnect},
		{"audit/tty", TestInitEgressAuditTTYSession},
		{"audit/concurrent", TestInitEgressAuditConcurrentSessions},
		{"dns/sendto", TestInitEgressPolicyDNSSendtoUnconnected},
		{"dns/sendmsg", TestInitEgressPolicyDNSSendmsgUnconnected},
		{"dns/peer", TestInitEgressPolicyDNSConnectedGetpeername},
		{"config/egress", TestConfigureEgressSendsTheTokenOverTheControlSocket},
		{"config/egress-reject", TestConfigureEgressSurfacesARejectedRequest},
		{"init/no-main", TestInitRunsWithoutAMainShell},
		{"init/tty-eof", TestInitTTYStdinEOFDoesNotHangUpTheTerminal},
		{"filter/egress-program", TestEgressFilterProgram},
		{"filter/hardening-program", TestHardeningFilterProgram},
		{"binary/header", TestEgressProxyConnectRequestCarriesTheBinaryHeader},
		{"binary/process", TestProcessBinaryReportsTheRunningExecutable},
		{"process/markers", TestMarkedProcessesExistTracksDetachedProcesses},
		{"enforce/no-proxy-env", TestInitEgressEnforceRemovesProxyEnvironment},
		{"enforce/proxy-refuses", TestInitEgressEnforceDeniesWhenProxyRefuses},
		{"enforce/loopback", TestInitEgressEnforceBypassesLoopback},
		{"enforce/unreachable", TestInitEgressEnforceFailsClosedWhenProxyIsUnreachable},
		{"fd/nonleader", TestDuplicateTargetFDFromNonLeaderThread},
		{"supervisor/generation", TestEgressSupervisorStampsTheLaunchGeneration},
		{"init/identity", TestInitHandshakeReportsSandboxIdentityAndCapabilities},
		{"init/legacy", TestInitHandshakeAcceptsLegacyInitWithoutIdentity},
		{"init/tty-stdin-eof", TestInitTTYStdinEOFDoesNotHangUpTheTerminal},
		{"init/no-main-shell", TestInitRunsWithoutAMainShell},
		{"init/reject-version-2", TestInitRejectsUnsupportedProtocolVersion},
		{"init/main-status-2", TestInitExitsWithTheMainProcessStatus},
		{"init/bridge", TestBridgeCarriesTheProtocolOverStdio},
		{"dns/sendmsg2", TestInitEgressPolicyDNSSendmsgUnconnected},
		{"dns/connected", TestInitEgressPolicyDNSConnectedGetpeername},
		{"dns/engine", TestInitEgressPolicyDNSPassesEngineHostThrough},
		{"filter/egress2", TestEgressFilterProgram},
		{"filter/hardening2", TestHardeningFilterProgram},
		{"landlock/resolve", TestResolveLandlockSetup},
		{"landlock/access", TestLandlockHandledAccessGatesRightsByABI},
		{"kernel/release", TestParseKernelRelease},
		{"bypass/endpoint", TestEgressBypassMatchesOnlyTheProxyEndpoint},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

func TestSandboxInitCoreE2E(t *testing.T) { TestSandboxInitCoreIntegration(t) }
