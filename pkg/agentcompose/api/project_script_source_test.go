package api

import (
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/proto"
)

func TestRedactSchedulerScriptSourceCredentials(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{Agents: []*agentcomposev2.AgentSpec{{Name: "reviewer", Scheduler: &agentcomposev2.SchedulerSpec{ScriptSource: &agentcomposev2.SchedulerScriptSource{Provider: "git", Url: "https://example.test/repo", Ref: "main", Path: "script.js", Username: "user", Password: "password", Token: "token"}}}}}
	before := proto.Clone(spec)
	result := RedactProjectSpecSecrets(spec)
	source := result.Agents[0].Scheduler.ScriptSource
	if source.Username != "********" || source.Password != "********" || source.Token != "********" || source.Path != "script.js" || source.Ref != "main" || source.Url != "https://example.test/repo" {
		t.Fatalf("redacted source: %v", source)
	}
	if !proto.Equal(before, spec) {
		t.Fatal("redaction mutated input")
	}
}
