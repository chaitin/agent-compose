//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	if pod.Spec.SecurityContext != nil || container.SecurityContext != nil {
		t.Fatalf("pod security context = %+v / %+v; the security-context declarations are stale", pod.Spec.SecurityContext, container.SecurityContext)
	}
	for _, dimension := range []string{dimensionCapabilityDrop, dimensionReadOnlyRootfs, dimensionNonRootUser, dimensionUserNamespaces} {
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

	// A declared default-deny policy is refused before createPod, because a
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
	if !strings.Contains(egressCapability.Observed, "refused before any Pod is created") {
		t.Fatalf("egress_policy observed = %q, want it to state that a declared default-deny policy is refused", egressCapability.Observed)
	}

	// The refusal is the observable behavior: EnsureSandbox must return the
	// machine-consumable reason and create neither a Pod nor a NetworkPolicy.
	denyRuntime, denyClientset := newTestK8sRuntime()
	denySandbox := testSandbox(t, "sandbox-egress-deny")
	denySandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}
	if _, err := denyRuntime.EnsureSandbox(context.Background(), denySandbox, VMState{}, ProxyState{}); !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("EnsureSandbox() error = %v, want ErrSandboxNetworkEnforcementUnavailable", err)
	}
	namespace := denyRuntime.namespaceFor(VMState{})
	policies, err := denyClientset.NetworkingV1().NetworkPolicies(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("a refused default-deny sandbox created NetworkPolicies %+v, want none", policies.Items)
	}
	pods, err := denyClientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("a refused default-deny sandbox created Pods %+v, want none", pods.Items)
	}
}
