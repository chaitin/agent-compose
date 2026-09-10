package runs

import (
	"encoding/json"
	"fmt"
	"strings"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func normalizeRevisionClosedSetsJSON(data []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if err := normalizeRevisionWorkspaceModes(root); err != nil {
		return nil, err
	}
	agents, _ := root["agents"].([]any)
	for index, value := range agents {
		agent, _ := value.(map[string]any)
		if err := normalizeRevisionVolumes(agent["volumes"]); err != nil {
			return nil, fmt.Errorf("agents[%d].volumes: %w", index, err)
		}
		scheduler, _ := agent["scheduler"].(map[string]any)
		if scheduler == nil {
			continue
		}
		defaultClosedSetString(scheduler, "sandbox_policy", "new")
		if err := replaceClosedSetString(scheduler, "sandbox_policy", map[string]int32{"sticky": int32(agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_STICKY), "new": int32(agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_NEW)}); err != nil {
			return nil, fmt.Errorf("agents[%d].scheduler.sandbox_policy: %w", index, err)
		}
		defaultClosedSetString(scheduler, "concurrency_policy", "skip")
		if err := replaceClosedSetString(scheduler, "concurrency_policy", map[string]int32{"skip": int32(agentcomposev2.SchedulerConcurrencyPolicy_SCHEDULER_CONCURRENCY_POLICY_SKIP), "parallel": int32(agentcomposev2.SchedulerConcurrencyPolicy_SCHEDULER_CONCURRENCY_POLICY_PARALLEL)}); err != nil {
			return nil, fmt.Errorf("agents[%d].scheduler.concurrency_policy: %w", index, err)
		}
		triggers, _ := scheduler["triggers"].([]any)
		for triggerIndex, triggerValue := range triggers {
			trigger, _ := triggerValue.(map[string]any)
			if err := replaceClosedSetString(trigger, "kind", map[string]int32{"cron": int32(agentcomposev2.TriggerKind_TRIGGER_KIND_CRON), "interval": int32(agentcomposev2.TriggerKind_TRIGGER_KIND_INTERVAL), "timeout": int32(agentcomposev2.TriggerKind_TRIGGER_KIND_TIMEOUT), "event": int32(agentcomposev2.TriggerKind_TRIGGER_KIND_EVENT)}); err != nil {
				return nil, fmt.Errorf("agents[%d].scheduler.triggers[%d].kind: %w", index, triggerIndex, err)
			}
			if err := replaceClosedSetString(trigger, "sandbox_policy", map[string]int32{"sticky": int32(agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_STICKY), "new": int32(agentcomposev2.SchedulerSandboxPolicy_SCHEDULER_SANDBOX_POLICY_NEW)}); err != nil {
				return nil, fmt.Errorf("agents[%d].scheduler.triggers[%d].sandbox_policy: %w", index, triggerIndex, err)
			}
		}
	}
	return json.Marshal(root)
}

func defaultClosedSetString(object map[string]any, field, fallback string) {
	if object == nil {
		return
	}
	raw, exists := object[field]
	if !exists || raw == nil {
		object[field] = fallback
		return
	}
	if text, ok := raw.(string); ok && strings.TrimSpace(text) == "" {
		object[field] = fallback
	}
}

func normalizeRevisionVolumes(value any) error {
	volumes, _ := value.([]any)
	for index, volumeValue := range volumes {
		volume, _ := volumeValue.(map[string]any)
		if err := replaceClosedSetString(volume, "type", map[string]int32{"volume": int32(agentcomposev2.VolumeMountType_VOLUME_MOUNT_TYPE_VOLUME), "bind": int32(agentcomposev2.VolumeMountType_VOLUME_MOUNT_TYPE_BIND)}); err != nil {
			return fmt.Errorf("[%d].type: %w", index, err)
		}
	}
	return nil
}

func replaceClosedSetString(object map[string]any, field string, values map[string]int32) error {
	if object == nil {
		return nil
	}
	raw, exists := object[field]
	if !exists || raw == nil {
		return nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil
	}
	value, ok := values[text]
	if !ok {
		return fmt.Errorf("unknown value %q", text)
	}
	object[field] = value
	return nil
}
