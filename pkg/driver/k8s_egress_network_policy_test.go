//go:build k8scompose

package driver

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestK8sEnforceCreatesDefaultDenyEgressNetworkPolicy(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	r.config.SandboxInitEnabled = true
	r.config.SandboxInitEgress = "enforce"
	sandbox := testSandbox(t, "sandbox-egress-policy")
	sandbox.NetworkPolicy = &SandboxNetworkPolicy{
		Default:                egress.Deny,
		MediatedByForwardProxy: true,
		EngineEndpoints: []egress.EngineEndpoint{{
			Purpose: egress.PurposeLLMFacade,
			Endpoint: egress.Endpoint{
				Host: "10.2.96.126", Port: 17413, Protocol: egress.ProtocolAny,
			},
			Source: "AGENT_COMPOSE_RUNTIME_BASE_URL",
		}},
	}
	pod, err := r.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}
	if err := r.ensureK8sEgressNetworkPolicy(context.Background(), clientset, pod, sandbox, VMState{}); err != nil {
		t.Fatalf("ensureK8sEgressNetworkPolicy() error = %v", err)
	}
	policies, err := clientset.NetworkingV1().NetworkPolicies("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 1 {
		t.Fatalf("NetworkPolicies = %d, want one per-sandbox egress policy", len(policies.Items))
	}
	policy := policies.Items[0]
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("policy types = %v, want egress", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("egress rules = %+v, want one engine endpoint rule", policy.Spec.Egress)
	}
	rule := policy.Spec.Egress[0]
	if len(rule.To) != 1 || rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "10.2.96.126/32" {
		t.Fatalf("egress destination = %+v, want 10.2.96.126/32", rule.To)
	}
	if len(rule.Ports) != 2 {
		t.Fatalf("egress ports = %+v, want TCP and UDP 17413", rule.Ports)
	}
}

func TestK8sEnforceWithoutPolicyDoesNotCreateNetworkPolicy(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	r.config.SandboxInitEnabled = true
	r.config.SandboxInitEgress = "enforce"
	sandbox := testSandbox(t, "sandbox-no-policy")
	pod, err := r.createPod(context.Background(), clientset, sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}
	if err := r.ensureK8sEgressNetworkPolicy(context.Background(), clientset, pod, sandbox, VMState{}); err != nil {
		t.Fatalf("ensureK8sEgressNetworkPolicy() error = %v", err)
	}
	policies, err := clientset.NetworkingV1().NetworkPolicies("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NetworkPolicies: %v", err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("NetworkPolicies = %d, want none for an unmediated sandbox", len(policies.Items))
	}
}
