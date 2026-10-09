//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestK8sCreatePodAppliesEgressNetworkPolicyBeforePod pins the invariant that
// keeps a declared default-deny sandbox from ever running with unrestricted
// egress: the NetworkPolicy must reach the API before the Pod does, since a
// policy only selects a Pod that already exists.
func TestK8sCreatePodAppliesEgressNetworkPolicyBeforePod(t *testing.T) {
	runtime, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-egress")
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}

	pod, err := runtime.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}

	policy, err := clientset.NetworkingV1().NetworkPolicies("default").Get(context.Background(), k8sEgressNetworkPolicyName(pod.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected the egress NetworkPolicy to exist: %v", err)
	}
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("PolicyTypes = %+v, want exactly [Egress]", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 0 {
		t.Fatalf("egress rules = %+v, want none so all egress is denied", policy.Spec.Egress)
	}
	wantLabel := k8sSandboxLabelValue(sandbox.Summary.ID)
	if len(policy.Spec.PodSelector.MatchLabels) != 1 || policy.Spec.PodSelector.MatchLabels[k8sSandboxLabelID] != wantLabel {
		t.Fatalf("PodSelector.MatchLabels = %+v, want only %s=%q", policy.Spec.PodSelector.MatchLabels, k8sSandboxLabelID, wantLabel)
	}
	if policy.Labels[k8sSandboxLabelID] != pod.Labels[k8sSandboxLabelID] {
		t.Fatalf("NetworkPolicy label %s = %q, want the Pod's %q", k8sSandboxLabelID, policy.Labels[k8sSandboxLabelID], pod.Labels[k8sSandboxLabelID])
	}

	// The fake clientset records every action in call order, so the index of
	// the NetworkPolicy create must precede the index of the Pod create.
	policyIndex, podIndex := -1, -1
	for index, action := range clientset.Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		switch action.GetResource().Resource {
		case "networkpolicies":
			if policyIndex == -1 {
				policyIndex = index
			}
		case "pods":
			if podIndex == -1 {
				podIndex = index
			}
		}
	}
	if policyIndex == -1 {
		t.Fatal("createPod issued no NetworkPolicy create; the egress deny was never applied")
	}
	if podIndex == -1 {
		t.Fatal("createPod issued no Pod create")
	}
	if policyIndex > podIndex {
		t.Fatalf("NetworkPolicy create at action %d happened after Pod create at action %d; the Pod could run with unrestricted egress", policyIndex, podIndex)
	}
}

// TestK8sCreatePodSkipsEgressNetworkPolicyWhenNotDenied pins D3 and the
// permissive case: only a declared default-deny policy generates a
// NetworkPolicy, so an undeclared sandbox keeps today's unrestricted egress.
func TestK8sCreatePodSkipsEgressNetworkPolicyWhenNotDenied(t *testing.T) {
	tests := []struct {
		name   string
		policy *SandboxNetworkPolicy
	}{
		{name: "undeclared policy", policy: nil},
		{name: "permissive policy", policy: &SandboxNetworkPolicy{Default: egress.Allow}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, clientset := newTestK8sRuntime()
			sandbox := testSandbox(t, "sandbox-open")
			sandbox.NetworkPolicy = test.policy

			if policy, needed := runtime.k8sSandboxEgressNetworkPolicy(sandbox, VMState{}); needed || policy != nil {
				t.Fatalf("k8sSandboxEgressNetworkPolicy() = (%+v, %v), want (nil, false)", policy, needed)
			}
			if _, err := runtime.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{}); err != nil {
				t.Fatalf("createPod() error = %v", err)
			}
			policies, err := clientset.NetworkingV1().NetworkPolicies("default").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("list NetworkPolicies: %v", err)
			}
			if len(policies.Items) != 0 {
				t.Fatalf("createPod created NetworkPolicies %+v, want none without default-deny", policies.Items)
			}
		})
	}
}

// TestK8sEgressNetworkPolicyApplyAndRemove covers the object lifecycle: a
// second apply updates the existing object rather than failing, remove deletes
// it, and remove tolerates an object that is already gone because every
// Pod-deletion path calls it unconditionally.
func TestK8sEgressNetworkPolicyApplyAndRemove(t *testing.T) {
	ctx := context.Background()
	runtime, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-lifecycle")
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}
	podName := runtime.podName(sandbox, VMState{})

	policy, needed := runtime.k8sSandboxEgressNetworkPolicy(sandbox, VMState{})
	if !needed {
		t.Fatal("k8sSandboxEgressNetworkPolicy() reported no policy for a default-deny sandbox")
	}
	if created, err := runtime.applySandboxEgressNetworkPolicy(ctx, clientset, policy); err != nil || !created {
		t.Fatalf("first apply = (created %v, err %v), want (true, nil)", created, err)
	}
	if created, err := runtime.applySandboxEgressNetworkPolicy(ctx, clientset, policy); err != nil || created {
		t.Fatalf("second apply (update existing) = (created %v, err %v), want (false, nil)", created, err)
	}

	if err := runtime.removeSandboxEgressNetworkPolicy(ctx, clientset, "default", podName); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	if _, err := clientset.NetworkingV1().NetworkPolicies("default").Get(ctx, k8sEgressNetworkPolicyName(podName), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Get after remove error = %v, want NotFound", err)
	}
	if err := runtime.removeSandboxEgressNetworkPolicy(ctx, clientset, "default", podName); err != nil {
		t.Fatalf("remove of an already-absent NetworkPolicy error = %v, want nil", err)
	}
}

// failPodCreate makes the next Pod create fail without reaching the tracker, so
// the createPod failure path can be exercised deterministically.
func failPodCreate(clientset *fake.Clientset) {
	clientset.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("pod create refused"))
	})
}

// TestK8sCreatePodRollsBackOwnEgressNetworkPolicyWhenPodCreateFails pins that a
// policy this call introduced is removed again when the Pod it was meant to
// select never appears, so a failed EnsureSandbox leaves no orphan deny behind.
func TestK8sCreatePodRollsBackOwnEgressNetworkPolicyWhenPodCreateFails(t *testing.T) {
	runtime, clientset := newTestK8sRuntime()
	failPodCreate(clientset)
	sandbox := testSandbox(t, "sandbox-rollback")
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}

	if _, err := runtime.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("createPod() error = nil, want the injected Pod create failure")
	}
	policies, err := clientset.NetworkingV1().NetworkPolicies("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("failed createPod left NetworkPolicies %+v, want none", policies.Items)
	}
}

// TestK8sCreatePodKeepsExistingEgressNetworkPolicyWhenPodCreateFails is the
// safety counterpart: when the Pod name is already taken by another Pod, the
// policy that was already denying its egress must survive the failed create, or
// that Pod would silently lose its deny.
func TestK8sCreatePodKeepsExistingEgressNetworkPolicyWhenPodCreateFails(t *testing.T) {
	ctx := context.Background()
	runtime, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-existing")
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{Default: egress.Deny}

	policy, needed := runtime.k8sSandboxEgressNetworkPolicy(sandbox, VMState{})
	if !needed {
		t.Fatal("k8sSandboxEgressNetworkPolicy() reported no policy for a default-deny sandbox")
	}
	if created, err := runtime.applySandboxEgressNetworkPolicy(ctx, clientset, policy); err != nil || !created {
		t.Fatalf("pre-apply = (created %v, err %v), want (true, nil)", created, err)
	}

	failPodCreate(clientset)
	if _, err := runtime.createPod(ctx, clientset, sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("createPod() error = nil, want the injected Pod create failure")
	}

	if _, err := clientset.NetworkingV1().NetworkPolicies("default").Get(ctx, policy.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("pre-existing NetworkPolicy %s was removed by a failed Pod create: %v", policy.Name, err)
	}
}
