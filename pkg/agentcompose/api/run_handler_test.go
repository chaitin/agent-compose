package api

import (
	"testing"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestRunEventToProtoMapsDomainKinds(t *testing.T) {
	tests := []struct {
		name string
		kind domain.ProjectRunEventKind
		want agentcomposev2.RunEventKind
	}{
		{name: "unspecified", want: agentcomposev2.RunEventKind_RUN_EVENT_KIND_UNSPECIFIED},
		{name: "user message", kind: domain.ProjectRunEventKindUserMessage, want: agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE},
		{name: "agent message", kind: domain.ProjectRunEventKindAgentMessage, want: agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE},
		{name: "agent activity", kind: domain.ProjectRunEventKindAgentActivity, want: agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY},
		{name: "status", kind: domain.ProjectRunEventKindStatus, want: agentcomposev2.RunEventKind_RUN_EVENT_KIND_STATUS},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runEventToProto(domain.ProjectRunEventRecord{Kind: test.kind}).GetKind(); got != test.want {
				t.Fatalf("kind = %v, want %v", got, test.want)
			}
		})
	}
}
