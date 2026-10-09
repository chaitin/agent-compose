//go:build linux && cgo && microsandboxcgo

package driver

import (
	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// microsandboxSDKNetworkConfig binds a pure microsandboxNetworkPlan to the
// Microsandbox Go SDK. It lives behind the same build tag as the runtime so the
// untagged mapping does not pull the SDK into the docker-only macOS build.
//
// Ingress stays allowed, matching the previous NetworkPolicy.AllowAll()
// behavior for inbound traffic: this change only tightens egress and restores
// the DNS rebind protection the driver had disabled.
func microsandboxSDKNetworkConfig(policy *SandboxNetworkPolicy) (*microsandbox.NetworkConfig, error) {
	plan, err := planMicrosandboxNetwork(policy)
	if err != nil {
		return nil, err
	}
	rebindProtection := plan.RebindProtection
	config := &microsandbox.NetworkConfig{
		DefaultEgress:  microsandbox.PolicyAction(plan.DefaultEgress),
		DefaultIngress: microsandbox.PolicyActionAllow,
		DNS:            &microsandbox.DNSConfig{RebindProtection: &rebindProtection},
		DenyDomains:    append([]string(nil), plan.DenyDomains...),
	}
	for _, rule := range plan.Rules {
		config.Rules = append(config.Rules, microsandbox.PolicyRule{
			Action:      microsandbox.PolicyAction(rule.Action),
			Direction:   microsandbox.PolicyDirectionEgress,
			Destination: rule.Destination,
			Port:        rule.Port,
			Protocol:    microsandbox.PolicyProtocol(rule.Protocol),
		})
	}
	return config, nil
}
