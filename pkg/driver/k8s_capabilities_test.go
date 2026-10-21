//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestK8sCapabilitiesMatchPodSpec cross-asserts the Kubernetes declaration
// against the Pod object the driver actually creates. It is compiled only with
// the k8scompose build tag, alongside the k8s driver and its test helpers.
func TestK8sCapabilitiesMatchPodSpec(t *testing.T) {
	facts := capabilityFactsForTest(t, RuntimeDriverK8s)
	runtime, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-capabilities")

	pod, err := runtime.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("pod containers = %+v, want exactly 1", pod.Spec.Containers)
	}
	container := pod.Spec.Containers[0]

	if container.Resources.Limits != nil || container.Resources.Requests != nil {
		t.Fatalf("pod container now declares resources %+v; the resource_limits declaration is stale", container.Resources)
	}
	if resources := capabilityDimensionForTest(t, facts, dimensionResourceLimits); resources.Enforced {
		t.Fatalf("resource_limits declaration = %+v, want not enforced", resources)
	}

	if pod.Spec.SecurityContext != nil {
		t.Fatalf("pod security context = %+v, want unset; only the container drops capabilities", pod.Spec.SecurityContext)
	}
	if container.SecurityContext == nil || container.SecurityContext.Capabilities == nil {
		t.Fatalf("container security context = %+v, want NET_RAW dropped", container.SecurityContext)
	}
	drop := container.SecurityContext.Capabilities.Drop
	if len(drop) != 1 || drop[0] != "NET_RAW" {
		t.Fatalf("container capability drop = %v, want exactly NET_RAW", drop)
	}
	capabilityDrop := capabilityDimensionForTest(t, facts, dimensionCapabilityDrop)
	if !capabilityDrop.Enforced || capabilityDrop.Mechanism != mechanismK8sCapabilityDrop {
		t.Fatalf("capability_drop declaration = %+v, want enforced via %q", capabilityDrop, mechanismK8sCapabilityDrop)
	}
	for _, dropped := range drop {
		if !strings.Contains(capabilityDrop.Observed, string(dropped)) {
			t.Fatalf("capability_drop observed = %q does not name dropped capability %q; update the report when Capabilities.Drop changes", capabilityDrop.Observed, dropped)
		}
	}
	if readOnly := capabilityDimensionForTest(t, facts, dimensionReadOnlyRootfs); !strings.Contains(readOnly.Observed, "ReadonlyRootfs") {
		t.Fatalf("read_only_rootfs observed = %q, want it to name the unset ReadonlyRootfs field", readOnly.Observed)
	}
	for _, dimension := range []string{dimensionReadOnlyRootfs, dimensionNonRootUser, dimensionUserNamespaces} {
		if capability := capabilityDimensionForTest(t, facts, dimension); capability.Enforced {
			t.Fatalf("%s declaration = %+v, want not enforced", dimension, capability)
		}
	}
	if pod.Spec.ServiceAccountName != "" {
		t.Fatalf("pod service account = %q, want empty", pod.Spec.ServiceAccountName)
	}
	if pod.Spec.HostNetwork {
		t.Fatal("pod now uses the host network; the egress_policy declaration is stale")
	}

	// A declared default-deny policy is not applied by this driver, because a
	// NetworkPolicy deny would also cut the engine's own LLM facade and
	// telemetry endpoints and the CNI cannot be assumed to enforce it. The
	// declaration must therefore report no egress enforcement for k8s.
	egressCapability := capabilityDimensionForTest(t, facts, dimensionEgressPolicy)
	if egressCapability.Enforced || egressCapability.Mechanism != reasonNotConfigured {
		t.Fatalf("egress_policy declaration = %+v, want not enforced via %q", egressCapability, reasonNotConfigured)
	}
	if !strings.Contains(egressCapability.Observed, string(SandboxEgressStrengthNone)) {
		t.Fatalf("egress_policy observed = %q, want strength %q", egressCapability.Observed, SandboxEgressStrengthNone)
	}
	if !strings.Contains(egressCapability.Observed, "no egress filter is installed by this driver") {
		t.Fatalf("egress_policy observed = %q, want it to state that this driver installs no egress filter", egressCapability.Observed)
	}

	// What the engine does with that gap is the deployment's choice, so the test
	// states the choice instead of assuming one. Under the default the sandbox
	// starts and the engine reports: that is the behavior every deployment gets
	// unless it opts into refusing, and asserting a refusal here without
	// configuring it pinned a behavior no default deployment has.
	denySandbox := testSandbox(t, "sandbox-egress-deny")
	denySandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}

	warnRuntime, warnClientset := newTestK8sRuntime()
	if _, err := warnRuntime.EnsureSandbox(context.Background(), denySandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox() error = %v, want the sandbox to start under the default %q mode", err, config.EgressUnenforceableWarn)
	}
	namespace := warnRuntime.namespaceFor(VMState{})
	policies, err := warnClientset.NetworkingV1().NetworkPolicies(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("a sandbox started under %q created NetworkPolicies %+v, want none: this driver installs no egress filter", config.EgressUnenforceableWarn, policies.Items)
	}
	pods, err := warnClientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("a sandbox started under %q created %d Pods, want the one it runs in", config.EgressUnenforceableWarn, len(pods.Items))
	}

	// The refusal is the other observable behavior: a deployment that asked for
	// it gets the machine-consumable reason and neither a Pod nor a
	// NetworkPolicy, so nothing runs with unrestricted egress.
	refuseRuntime, refuseClientset := newTestK8sRuntime()
	refuseRuntime.config.EgressUnenforceablePolicy = config.EgressUnenforceableRefuse
	if _, err := refuseRuntime.EnsureSandbox(context.Background(), denySandbox, VMState{}, ProxyState{}); !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("EnsureSandbox() error = %v, want ErrSandboxNetworkEnforcementUnavailable", err)
	}
	refused, err := refuseClientset.NetworkingV1().NetworkPolicies(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(refused.Items) != 0 {
		t.Fatalf("a refused default-deny sandbox created NetworkPolicies %+v, want none", refused.Items)
	}
	refusedPods, err := refuseClientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Pods: %v", err)
	}
	if len(refusedPods.Items) != 0 {
		t.Fatalf("a refused default-deny sandbox created Pods %+v, want none", refusedPods.Items)
	}
}
