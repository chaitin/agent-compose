//go:build k8scompose

package driver

import (
	"context"
	"testing"

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

	policies, err := clientset.NetworkingV1().NetworkPolicies(runtime.namespaceFor(VMState{})).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("createPod now creates NetworkPolicies %+v; the egress_policy declaration is stale", policies.Items)
	}
	if egress := capabilityDimensionForTest(t, facts, dimensionEgressPolicy); egress.Enforced {
		t.Fatalf("egress_policy declaration = %+v, want not enforced", egress)
	}
}
