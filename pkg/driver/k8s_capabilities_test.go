//go:build k8scompose

package driver

import (
	"context"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestK8sCapabilitiesMatchPodSpec cross-asserts the Kubernetes declaration
// against the Pod object the driver actually creates. It is compiled only with
// the k8scompose build tag, alongside the k8s driver and its test helpers.
func TestK8sCapabilitiesMatchPodSpec(t *testing.T) {
	facts := capabilityFactsForTest(t, RuntimeDriverK8s)
	runtime, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-capabilities")
	// The egress_policy declaration is an outer deny, so the cross-assertion
	// has to create a Pod for a sandbox that declares default-deny egress.
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}

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

	// A declared default-deny sandbox gets exactly one egress NetworkPolicy,
	// applied before the Pod exists (see createPod), so the Pod never runs with
	// unrestricted egress.
	policies, err := clientset.NetworkingV1().NetworkPolicies(runtime.namespaceFor(VMState{})).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 1 {
		t.Fatalf("createPod created %d NetworkPolicies (%+v), want exactly 1 for a declared default-deny policy", len(policies.Items), policies.Items)
	}
	policy := policies.Items[0]
	if policy.Name != k8sEgressNetworkPolicyName(pod.Name) {
		t.Fatalf("NetworkPolicy name = %q, want %q", policy.Name, k8sEgressNetworkPolicyName(pod.Name))
	}
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("NetworkPolicy PolicyTypes = %+v, want exactly [Egress]", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 0 {
		t.Fatalf("NetworkPolicy has egress rules %+v, want none (deny all)", policy.Spec.Egress)
	}
	if policy.Spec.PodSelector.MatchLabels[k8sSandboxLabelID] != pod.Labels[k8sSandboxLabelID] {
		t.Fatalf("NetworkPolicy PodSelector = %+v, want the Pod's %s=%q", policy.Spec.PodSelector.MatchLabels, k8sSandboxLabelID, pod.Labels[k8sSandboxLabelID])
	}

	egressCapability := capabilityDimensionForTest(t, facts, dimensionEgressPolicy)
	if !egressCapability.Enforced {
		t.Fatalf("egress_policy declaration = %+v, want enforced", egressCapability)
	}
	if egressCapability.Mechanism != mechanismNetworkPolicyEgress {
		t.Fatalf("egress_policy mechanism = %q, want %q", egressCapability.Mechanism, mechanismNetworkPolicyEgress)
	}
	if !strings.Contains(egressCapability.Observed, string(SandboxEgressStrengthOuterDeny)) {
		t.Fatalf("egress_policy observed = %q, want strength %q", egressCapability.Observed, SandboxEgressStrengthOuterDeny)
	}
}
