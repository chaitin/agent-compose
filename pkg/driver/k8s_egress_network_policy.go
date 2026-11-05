//go:build k8scompose

package driver

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// k8sEgressNetworkPolicyName is the per-sandbox CNI policy that makes the
// workload's network default-deny before any process-level mediation runs.
// The security boundary is the network namespace, not an exec trampoline: a
// process started by the kubelet, a health probe, or any other path that does
// not go through ac-init still has no route to a destination the declaration
// does not allow.
const k8sEgressNetworkPolicySuffix = "-egress"

// ensureK8sEgressNetworkPolicy installs/updates the default-deny egress
// NetworkPolicy for one enforcing sandbox. The only permitted destinations are
// the engine-owned endpoints (facade, telemetry, and the forward proxy the init
// uses to mediate allowed connections); the declared destination policy remains
// a proxy decision.
func (r *k8sRuntime) ensureK8sEgressNetworkPolicy(ctx context.Context, clientset kubernetes.Interface, pod *corev1.Pod, sandbox *Sandbox, vmState VMState) error {
	if r == nil || r.config == nil || pod == nil || sandbox == nil || sandbox.NetworkPolicy == nil ||
		!sandbox.NetworkPolicy.Restricts() || !sandbox.NetworkPolicy.MediatedByForwardProxy ||
		!SandboxInitEnabledFor(r.config, vmState) || !sandboxInitEgressRequiresPtrace(r.config) {
		return nil
	}
	policy, err := k8sEgressNetworkPolicy(ctx, pod, sandbox.NetworkPolicy)
	if err != nil {
		return err
	}
	networkPolicies := clientset.NetworkingV1().NetworkPolicies(pod.Namespace)
	existing, err := networkPolicies.Get(ctx, policy.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := networkPolicies.Create(ctx, policy, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create k8s egress NetworkPolicy %s: %w", policy.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect k8s egress NetworkPolicy %s: %w", policy.Name, err)
	}
	if k8sNetworkPolicyEqual(existing, policy) {
		return nil
	}
	policy.ResourceVersion = existing.ResourceVersion
	if _, err := networkPolicies.Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update k8s egress NetworkPolicy %s: %w", policy.Name, err)
	}
	return nil
}

func k8sEgressNetworkPolicy(ctx context.Context, pod *corev1.Pod, policy *SandboxNetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	if pod == nil || policy == nil {
		return nil, fmt.Errorf("k8s egress NetworkPolicy requires a pod and a policy")
	}
	rules, err := k8sEngineEndpointEgressRules(ctx, policy.EngineEndpoints)
	if err != nil {
		return nil, err
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name + k8sEgressNetworkPolicySuffix,
			Namespace: pod.Namespace,
			Labels: map[string]string{
				k8sSandboxLabelID:     pod.Labels[k8sSandboxLabelID],
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       pod.Name,
				UID:        pod.UID,
			}},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				k8sSandboxLabelID: pod.Labels[k8sSandboxLabelID],
			}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}, nil
}

// k8sEngineEndpointEgressRules renders the engine endpoints as CNI egress
// rules. A hostname is resolved once, here, because NetworkPolicy ipBlock only
// accepts CIDRs; a failure to resolve fails the start rather than creating a
// policy that would cut the facade off.
func k8sEngineEndpointEgressRules(ctx context.Context, endpoints []egress.EngineEndpoint) ([]networkingv1.NetworkPolicyEgressRule, error) {
	type target struct {
		cidr  string
		ports map[string]networkingv1.NetworkPolicyPort
	}
	targets := make(map[string]*target)
	for _, endpoint := range endpoints {
		cidrs, err := k8sEndpointCIDRs(ctx, endpoint.Endpoint.Host)
		if err != nil {
			return nil, fmt.Errorf("resolve engine endpoint %s host %q for the k8s egress policy: %w", endpoint.Purpose, endpoint.Endpoint.Host, err)
		}
		for _, cidr := range cidrs {
			entry := targets[cidr]
			if entry == nil {
				entry = &target{cidr: cidr, ports: make(map[string]networkingv1.NetworkPolicyPort)}
				targets[cidr] = entry
			}
			for _, port := range k8sEngineEndpointPorts(endpoint.Endpoint) {
				key := fmt.Sprintf("%s/%d", *port.Protocol, port.Port.IntValue())
				entry.ports[key] = port
			}
		}
	}
	cidrs := make([]string, 0, len(targets))
	for cidr := range targets {
		cidrs = append(cidrs, cidr)
	}
	sort.Slice(cidrs, func(i, j int) bool { return cidrs[i] < cidrs[j] })
	rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(cidrs))
	for _, cidr := range cidrs {
		ports := make([]networkingv1.NetworkPolicyPort, 0, len(targets[cidr].ports))
		keys := make([]string, 0, len(targets[cidr].ports))
		for key := range targets[cidr].ports {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			ports = append(ports, targets[cidr].ports[key])
		}
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: ports,
		})
	}
	return rules, nil
}

func k8sEngineEndpointPorts(endpoint egress.Endpoint) []networkingv1.NetworkPolicyPort {
	port := intstr.FromInt(endpoint.Port)
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	switch endpoint.Protocol {
	case egress.ProtocolUDP:
		return []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &port}}
	case egress.ProtocolAny:
		return []networkingv1.NetworkPolicyPort{
			{Protocol: &tcp, Port: &port},
			{Protocol: &udp, Port: &port},
		}
	default:
		return []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}
	}
}

func k8sEndpointCIDRs(ctx context.Context, host string) ([]string, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if _, network, err := net.ParseCIDR(host); err == nil {
		return []string{network.String()}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return []string{k8sIPCIDR(ip)}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(addresses))
	cidrs := make([]string, 0, len(addresses))
	for _, address := range addresses {
		cidr := k8sIPCIDR(address.IP)
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		cidrs = append(cidrs, cidr)
	}
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	sort.Strings(cidrs)
	return cidrs, nil
}

func k8sIPCIDR(ip net.IP) string {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String() + "/32"
	}
	return ip.String() + "/128"
}

func k8sNetworkPolicyEqual(a, b *networkingv1.NetworkPolicy) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.OwnerReferences) != len(b.OwnerReferences) || len(a.OwnerReferences) != 1 {
		return false
	}
	if a.OwnerReferences[0].UID != b.OwnerReferences[0].UID || a.OwnerReferences[0].Name != b.OwnerReferences[0].Name {
		return false
	}
	return a.Spec.PodSelector.String() == b.Spec.PodSelector.String() &&
		k8sEgressRulesEqual(a.Spec.Egress, b.Spec.Egress) &&
		len(a.Spec.PolicyTypes) == len(b.Spec.PolicyTypes)
}

func k8sEgressRulesEqual(a, b []networkingv1.NetworkPolicyEgressRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i].To) != len(b[i].To) || len(a[i].Ports) != len(b[i].Ports) {
			return false
		}
		for j := range a[i].To {
			if a[i].To[j].IPBlock == nil || b[i].To[j].IPBlock == nil || a[i].To[j].IPBlock.CIDR != b[i].To[j].IPBlock.CIDR {
				return false
			}
		}
		for j := range a[i].Ports {
			if a[i].Ports[j].Protocol == nil || b[i].Ports[j].Protocol == nil ||
				a[i].Ports[j].Port == nil || b[i].Ports[j].Port == nil ||
				*a[i].Ports[j].Protocol != *b[i].Ports[j].Protocol ||
				a[i].Ports[j].Port.IntValue() != b[i].Ports[j].Port.IntValue() {
				return false
			}
		}
	}
	return true
}
