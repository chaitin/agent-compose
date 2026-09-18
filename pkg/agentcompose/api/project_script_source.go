package api

import (
	"strings"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func validateSchedulerScriptSource(scheduler *agentcomposev2.SchedulerSpec, path string) *agentcomposev2.ProjectValidationIssue {
	source := scheduler.GetScriptSource()
	if source == nil {
		return nil
	}
	if scheduler.GetScript() != "" {
		return ProjectValidationIssue(path+".script_source", "script and script_source are mutually exclusive")
	}
	if len(scheduler.GetTriggers()) > 0 {
		return ProjectValidationIssue(path+".script_source", "script_source and triggers are mutually exclusive")
	}
	if strings.TrimSpace(source.GetProvider()) == "" {
		return ProjectValidationIssue(path+".script_source.provider", "script source provider is required")
	}
	return nil
}

func schedulerScriptSourceYAMLShape(source *agentcomposev2.SchedulerScriptSource) map[string]any {
	return map[string]any{
		"provider": source.GetProvider(),
		"url":      source.GetUrl(),
		"ref":      source.GetRef(),
		"path":     source.GetPath(),
		"username": source.GetUsername(),
		"password": source.GetPassword(),
		"token":    source.GetToken(),
	}
}

func redactSchedulerScriptSource(source *agentcomposev2.SchedulerScriptSource) {
	if source == nil {
		return
	}
	if source.GetUsername() != "" {
		source.Username = secretRedactedValue
	}
	if source.GetPassword() != "" {
		source.Password = secretRedactedValue
	}
	if source.GetToken() != "" {
		source.Token = secretRedactedValue
	}
}
