package runs

import (
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestDecodeRevisionSpecMapsStoredClosedSetStrings(t *testing.T) {
	spec, err := DecodeRevisionSpec(`{"name":"demo","agents":[{"name":"worker","provider":"extension-provider","model":"extension-model","image":"registry.example/guest:next","volumes":[{"type":"bind","source":"/src","target":"/dst"}],"scheduler":{"sandbox_policy":"sticky","concurrency_policy":"parallel","triggers":[{"kind":"event","event":{"topic":"extension.topic"},"sandbox_policy":"new"}]}}]}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	agent := spec.GetAgents()[0]
	if agent.GetProvider() != "extension-provider" || agent.GetModel() != "extension-model" || agent.GetImage() != "registry.example/guest:next" {
		t.Fatalf("open extension strings were not preserved: %#v", agent)
	}
	if agent.GetVolumes()[0].GetType() != agentcomposev2.VolumeMountType_VOLUME_MOUNT_TYPE_BIND ||
		agent.GetScheduler().GetSandboxPolicy() != agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_STICKY ||
		agent.GetScheduler().GetConcurrencyPolicy() != agentcomposev2.SchedulerConcurrencyPolicy_SCHEDULER_CONCURRENCY_POLICY_PARALLEL ||
		agent.GetScheduler().GetTriggers()[0].GetKind() != agentcomposev2.TriggerKind_TRIGGER_KIND_EVENT {
		t.Fatalf("closed sets were not mapped: %#v", agent)
	}
}

func TestDecodeRevisionSpecDefaultsHistoricalSchedulerPolicies(t *testing.T) {
	spec, err := DecodeRevisionSpec(`{"name":"historical","agents":[{"name":"missing","scheduler":{}},{"name":"empty","scheduler":{"sandbox_policy":"","concurrency_policy":""}}]}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	for _, agent := range spec.GetAgents() {
		if agent.GetScheduler().GetSandboxPolicy() != agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_NEW {
			t.Fatalf("scheduler sandbox policy for %s = %s", agent.GetName(), agent.GetScheduler().GetSandboxPolicy())
		}
		if agent.GetScheduler().GetConcurrencyPolicy() != agentcomposev2.SchedulerConcurrencyPolicy_SCHEDULER_CONCURRENCY_POLICY_SKIP {
			t.Fatalf("scheduler concurrency policy for %s = %s", agent.GetName(), agent.GetScheduler().GetConcurrencyPolicy())
		}
	}
}

func TestDecodeRevisionSpecRejectsUnknownStoredClosedSetString(t *testing.T) {
	if _, err := DecodeRevisionSpec(`{"agents":[{"scheduler":{"concurrency_policy":"queue"}}]}`); err == nil {
		t.Fatal("DecodeRevisionSpec accepted an unknown concurrency policy")
	}
}

// The stored revision keeps the compose spelling of the agent policy block while
// the decoded message carries the original field name. Dropping the block there
// silently changed the stopped-runtime lifecycle of every run: a sandbox created
// with `retain` recorded `remove` and lost its runtime on the next stop.
func TestDecodeRevisionSpecKeepsTheAgentPolicyBlock(t *testing.T) {
	spec, err := DecodeRevisionSpec(`{"name":"demo","agents":[{"name":"retained","policy":{"stopped_runtime_policy":"retain","network":{"default":"deny-all"}}},{"name":"deprecated","sandbox":{"stopped_runtime_policy":"retain"}}]}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	if got := spec.GetAgents()[0].GetSandbox().GetStoppedRuntimePolicy(); got != "retain" {
		t.Fatalf("policy.stopped_runtime_policy = %q, want retain", got)
	}
	if got := spec.GetAgents()[0].GetSandbox().GetNetwork().GetDefault(); got != "deny-all" {
		t.Fatalf("policy.network.default = %q, want deny-all", got)
	}
	if got := spec.GetAgents()[1].GetSandbox().GetStoppedRuntimePolicy(); got != "retain" {
		t.Fatalf("deprecated sandbox.stopped_runtime_policy = %q, want retain", got)
	}
}
