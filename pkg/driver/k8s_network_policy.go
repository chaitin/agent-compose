//go:build k8scompose

package driver

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// This file owns the per-sandbox egress NetworkPolicy for the Kubernetes
// driver: the pure object a default-deny sandbox needs, and the create/update
// and delete calls around it.
//
// Kubernetes NetworkPolicy is L3/L4 and matches pods, ports, and CIDRs - not
// DNS names - so the declared allowance list and the engine-owned endpoints are
// deliberately NOT expressed here. The honest strength is an outer deny: every
// egress connection from the selected Pod is refused. The strength report in
// network_enforcement.go states that limit.

// k8sEgressNetworkPolicyName is the deterministic name for a sandbox Pod's
// egress NetworkPolicy. Deriving it from the Pod name (which podName already
// makes stable per sandbox) means a Pod recreated under the same name reuses
// and overwrites its policy instead of accumulating a new object each time.
func k8sEgressNetworkPolicyName(podName string) string {
	return podName + "-egress"
}

// k8sSandboxEgressNetworkPolicy builds the egress NetworkPolicy for one
// sandbox, or reports that none is needed. It is pure: it performs no I/O and
// depends only on the sandbox, the VM state, and the runtime's namespace
// default.
//
// A policy is needed only for a declared default-deny policy. An undeclared or
// permissive policy returns false so the driver leaves egress as open as it is
// without a declaration (D3).
//
// The returned policy selects the Pod by the same sandbox-ID label createPod
// sets, declares the Egress policy type, and carries no egress rules: with
// PolicyTypeEgress selected and an empty rule list, all egress is denied.
func (r *k8sRuntime) k8sSandboxEgressNetworkPolicy(sandbox *Sandbox, vmState VMState) (*networkingv1.NetworkPolicy, bool) {
	if sandbox.NetworkPolicy == nil || !sandbox.NetworkPolicy.DenyByDefault() {
		return nil, false
	}
	podName := r.podName(sandbox, vmState)
	sandboxLabels := map[string]string{k8sSandboxLabelID: k8sSandboxLabelValue(sandbox.Summary.ID)}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sEgressNetworkPolicyName(podName),
			Namespace: r.namespaceFor(vmState),
			Labels:    map[string]string{k8sSandboxLabelID: k8sSandboxLabelValue(sandbox.Summary.ID)},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: sandboxLabels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}, true
}

// applySandboxEgressNetworkPolicy creates the policy, or updates the existing
// object with the same deterministic name. Update-not-delete+create keeps the
// deny continuous across a Pod recreate: there is never an instant where the
// old policy is gone and the new one has not landed.
//
// created reports whether this call introduced the object rather than updating
// an existing one. A caller that must undo a later failure removes the policy
// only when created is true, so it can never strip the egress deny from a Pod
// that already existed under the same name.
//
// The update needs the live resourceVersion. It is fetched with a Get rather
// than left empty because Kubernetes rejects an Update whose resourceVersion
// is unset on an existing object.
func (r *k8sRuntime) applySandboxEgressNetworkPolicy(ctx context.Context, clientset kubernetes.Interface, policy *networkingv1.NetworkPolicy) (created bool, err error) {
	networkPolicies := clientset.NetworkingV1().NetworkPolicies(policy.Namespace)
	_, err = networkPolicies.Create(ctx, policy, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("create k8s NetworkPolicy %s: %w", policy.Name, err)
	}
	existing, err := networkPolicies.Get(ctx, policy.Name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("read existing k8s NetworkPolicy %s for update: %w", policy.Name, err)
	}
	policy.ResourceVersion = existing.ResourceVersion
	if _, err := networkPolicies.Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("update k8s NetworkPolicy %s: %w", policy.Name, err)
	}
	return false, nil
}

// removeSandboxEgressNetworkPolicy deletes the policy for podName, tolerating
// an already-absent object: every Pod-deletion path calls it unconditionally,
// including for a sandbox that never declared a policy and therefore never had
// one.
func (r *k8sRuntime) removeSandboxEgressNetworkPolicy(ctx context.Context, clientset kubernetes.Interface, namespace, podName string) error {
	name := k8sEgressNetworkPolicyName(podName)
	if err := clientset.NetworkingV1().NetworkPolicies(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete k8s NetworkPolicy %s: %w", name, err)
	}
	return nil
}
