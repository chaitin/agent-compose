package projects

import (
	"slices"
	"strings"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func SameSandboxEnvItems(a, b []domain.SandboxEnvVar) bool {
	a = domain.NormalizeEnvItems(a)
	b = domain.NormalizeEnvItems(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func SameCapsetIDs(a, b []string) bool {
	a = capabilities.NormalizeCapsetIDs(a)
	b = capabilities.NormalizeCapsetIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func SameSkills(a, b []domain.AgentSkill) bool {
	a = domain.NormalizeAgentSkills(a)
	b = domain.NormalizeAgentSkills(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func SameSchedulerTriggerSpecs(a, b []domain.SchedulerTrigger) bool {
	a = NormalizeComparableSchedulerTriggers(a)
	b = NormalizeComparableSchedulerTriggers(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID ||
			a[i].Kind != b[i].Kind ||
			a[i].Topic != b[i].Topic ||
			a[i].IntervalMs != b[i].IntervalMs ||
			a[i].AutoID != b[i].AutoID ||
			a[i].SpecJSON != b[i].SpecJSON {
			return false
		}
	}
	return true
}

func NormalizeComparableSchedulerTriggers(items []domain.SchedulerTrigger) []domain.SchedulerTrigger {
	cloned := append([]domain.SchedulerTrigger(nil), items...)
	for i := range cloned {
		cloned[i].ID = strings.TrimSpace(cloned[i].ID)
		cloned[i].Kind = strings.TrimSpace(cloned[i].Kind)
		cloned[i].Topic = strings.TrimSpace(cloned[i].Topic)
		cloned[i].SpecJSON = strings.TrimSpace(cloned[i].SpecJSON)
	}
	slices.SortFunc(cloned, func(a, b domain.SchedulerTrigger) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cloned
}

func AgentDefinitionUnchanged(existing, current domain.AgentDefinition) bool {
	return existing.Name == current.Name &&
		existing.Description == current.Description &&
		existing.Enabled == current.Enabled &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.SystemPrompt == current.SystemPrompt &&
		existing.Driver == current.Driver &&
		existing.GuestImage == current.GuestImage &&
		existing.WorkspaceID == current.WorkspaceID &&
		existing.ConfigJSON == current.ConfigJSON &&
		SameSandboxEnvItems(existing.EnvItems, current.EnvItems) &&
		SameCapsetIDs(existing.CapsetIDs, current.CapsetIDs) &&
		SameSkills(existing.Skills, current.Skills) &&
		existing.ProjectID == current.ProjectID &&
		existing.ProjectRevision == current.ProjectRevision &&
		existing.AgentName == current.AgentName
}

func SchedulerRecordUnchanged(existing, current domain.ProjectSchedulerRecord) bool {
	return existing.ID == current.ID &&
		existing.Revision == current.Revision &&
		existing.Enabled == current.Enabled &&
		existing.TriggerCount == current.TriggerCount &&
		existing.SpecJSON == current.SpecJSON
}

func SchedulerDefinitionUnchanged(existing, current domain.Scheduler) bool {
	return existing.Summary.Name == current.Summary.Name &&
		existing.Summary.Description == current.Summary.Description &&
		existing.Summary.Enabled == current.Summary.Enabled &&
		existing.Summary.Runtime == current.Summary.Runtime &&
		existing.Summary.WorkspaceID == current.Summary.WorkspaceID &&
		existing.Summary.AgentID == current.Summary.AgentID &&
		existing.Summary.Driver == current.Summary.Driver &&
		existing.Summary.GuestImage == current.Summary.GuestImage &&
		existing.Summary.DefaultAgent == current.Summary.DefaultAgent &&
		existing.Summary.SandboxPolicy == current.Summary.SandboxPolicy &&
		existing.Summary.ConcurrencyPolicy == current.Summary.ConcurrencyPolicy &&
		existing.Summary.ProjectID == current.Summary.ProjectID &&
		existing.Summary.ProjectRevision == current.Summary.ProjectRevision &&
		existing.Summary.AgentName == current.Summary.AgentName &&
		existing.Summary.ProjectSchedulerID == current.Summary.ProjectSchedulerID &&
		existing.Script == current.Script &&
		SameSandboxEnvItems(existing.EnvItems, current.EnvItems) &&
		SameCapsetIDs(existing.Summary.CapsetIDs, current.Summary.CapsetIDs) &&
		SameSchedulerTriggerSpecs(existing.Triggers, current.Triggers)
}

func ProjectRecordUnchanged(existing, current domain.ProjectRecord) bool {
	return existing.ID == current.ID &&
		existing.Name == current.Name &&
		existing.SourcePath == current.SourcePath &&
		existing.SpecHash == current.SpecHash &&
		existing.CurrentRevision == current.CurrentRevision &&
		existing.RemovedAt.IsZero()
}

func ProjectAgentRecordUnchanged(existing, current domain.ProjectAgentRecord) bool {
	return existing.ID == current.ID &&
		existing.Revision == current.Revision &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.Image == current.Image &&
		existing.Driver == current.Driver &&
		existing.SchedulerEnabled == current.SchedulerEnabled &&
		existing.SpecJSON == current.SpecJSON
}
