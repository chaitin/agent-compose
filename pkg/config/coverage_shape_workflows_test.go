package config

import "testing"

// Global egress policy parsing is configuration-boundary behavior, so the
// broader shapes must carry it too.
func TestE2EGlobalNetworkPolicyWorkflows(t *testing.T) {
	t.Run("load global policy", TestLoadGlobalNetworkPolicy)
	t.Run("reject invalid global policy", TestLoadGlobalNetworkPolicyRejectsInvalidConfig)
}
