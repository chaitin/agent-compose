package projects

import (
	"slices"
	"strings"

	"agent-compose/pkg/capabilities"
	domain "agent-compose/pkg/model"
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

func SameLoaderTriggerSpecs(a, b []domain.LoaderTrigger) bool {
	a = NormalizeComparableLoaderTriggers(a)
	b = NormalizeComparableLoaderTriggers(b)
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

func NormalizeComparableLoaderTriggers(items []domain.LoaderTrigger) []domain.LoaderTrigger {
	cloned := append([]domain.LoaderTrigger(nil), items...)
	for i := range cloned {
		cloned[i].ID = strings.TrimSpace(cloned[i].ID)
		cloned[i].Kind = strings.TrimSpace(cloned[i].Kind)
		cloned[i].Topic = strings.TrimSpace(cloned[i].Topic)
		cloned[i].SpecJSON = strings.TrimSpace(cloned[i].SpecJSON)
	}
	slices.SortFunc(cloned, func(a, b domain.LoaderTrigger) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cloned
}

func ManagedAgentDefinitionUnchanged(existing, current domain.AgentDefinition) bool {
	return existing.Name == current.Name &&
		existing.Description == current.Description &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.SystemPrompt == current.SystemPrompt &&
		existing.Driver == current.Driver &&
		existing.GuestImage == current.GuestImage &&
		existing.WorkspaceID == current.WorkspaceID &&
		existing.ConfigJSON == current.ConfigJSON &&
		SameSandboxEnvItems(existing.EnvItems, current.EnvItems) &&
		SameCapsetIDs(existing.CapsetIDs, current.CapsetIDs) &&
		existing.ManagedProjectID == current.ManagedProjectID &&
		existing.ManagedProjectRevision == current.ManagedProjectRevision &&
		existing.ManagedAgentName == current.ManagedAgentName
}

func SchedulerRecordUnchanged(existing, current domain.ProjectSchedulerRecord) bool {
	return existing.ManagedLoaderID == current.ManagedLoaderID &&
		existing.Revision == current.Revision &&
		existing.Enabled == current.Enabled &&
		existing.TriggerCount == current.TriggerCount &&
		existing.SpecJSON == current.SpecJSON
}

func ManagedLoaderUnchanged(existing, current domain.Loader) bool {
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
		existing.Summary.ManagedProjectID == current.Summary.ManagedProjectID &&
		existing.Summary.ManagedRevision == current.Summary.ManagedRevision &&
		existing.Summary.ManagedAgentName == current.Summary.ManagedAgentName &&
		existing.Summary.ManagedSchedulerID == current.Summary.ManagedSchedulerID &&
		existing.Script == current.Script &&
		SameSandboxEnvItems(existing.EnvItems, current.EnvItems) &&
		SameCapsetIDs(existing.Summary.CapsetIDs, current.Summary.CapsetIDs) &&
		SameLoaderTriggerSpecs(existing.Triggers, current.Triggers)
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
	return existing.ManagedAgentID == current.ManagedAgentID &&
		existing.Revision == current.Revision &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.Image == current.Image &&
		existing.Driver == current.Driver &&
		existing.SchedulerEnabled == current.SchedulerEnabled &&
		existing.SpecJSON == current.SpecJSON
}
