package api

import (
	"strings"
	"time"

	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func LoaderSummaryToProto(item domain.LoaderSummary) *agentcomposev1.LoaderSummary {
	return &agentcomposev1.LoaderSummary{
		LoaderId:          item.ID,
		Name:              item.Name,
		Description:       item.Description,
		Enabled:           item.Enabled,
		Runtime:           item.Runtime,
		WorkspaceId:       item.WorkspaceID,
		AgentId:           item.AgentID,
		Driver:            item.Driver,
		GuestImage:        item.GuestImage,
		DefaultAgent:      item.DefaultAgent,
		SessionPolicy:     item.SessionPolicy,
		ConcurrencyPolicy: item.ConcurrencyPolicy,
		CapsetIds:         item.CapsetIDs,
		CreatedAt:         item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:         item.UpdatedAt.Format(time.RFC3339Nano),
		LastError:         item.LastError,
		TriggerCount:      uint32(item.TriggerCount),
		RunCount:          uint32(item.RunCount),
		EventCount:        uint32(item.EventCount),
		LatestRunAt:       FormatMaybeTime(item.LatestRunAt),
	}
}

func LoaderDetailToProto(item domain.Loader) *agentcomposev1.LoaderDetail {
	resp := &agentcomposev1.LoaderDetail{
		Summary:   LoaderSummaryToProto(item.Summary),
		Script:    item.Script,
		CapsetIds: item.Summary.CapsetIDs,
	}
	for _, trigger := range item.Triggers {
		resp.Triggers = append(resp.Triggers, LoaderTriggerToProto(trigger))
	}
	for _, envItem := range item.EnvItems {
		value := envItem.Value
		if envItem.Secret && value != "" {
			value = secretRedactedValue
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: envItem.Name, Value: value, Secret: envItem.Secret})
	}
	return resp
}

func LoaderTriggerToProto(item domain.LoaderTrigger) *agentcomposev1.LoaderTrigger {
	return &agentcomposev1.LoaderTrigger{
		LoaderId:    item.LoaderID,
		TriggerId:   item.ID,
		Kind:        LoaderTriggerKindToProto(item.Kind),
		Topic:       item.Topic,
		IntervalMs:  item.IntervalMs,
		Enabled:     item.Enabled,
		AutoId:      item.AutoID,
		SpecJson:    item.SpecJSON,
		NextFireAt:  FormatMaybeTime(item.NextFireAt),
		LastFiredAt: FormatMaybeTime(item.LastFiredAt),
	}
}

func LoaderRunSummaryToProto(item domain.LoaderRunSummary) *agentcomposev1.LoaderRunSummary {
	return &agentcomposev1.LoaderRunSummary{
		RunId:              item.ID,
		LoaderId:           item.LoaderID,
		TriggerId:          item.TriggerID,
		TriggerKind:        LoaderTriggerKindToProto(item.TriggerKind),
		TriggerSource:      item.TriggerSource,
		Status:             item.Status,
		StartedAt:          item.StartedAt.Format(time.RFC3339Nano),
		CompletedAt:        FormatMaybeTime(item.CompletedAt),
		DurationMs:         item.DurationMs,
		Error:              item.Error,
		ResultJson:         item.ResultJSON,
		PayloadJson:        item.PayloadJSON,
		SourceScriptSha256: item.SourceScriptHash,
		ArtifactsDir:       item.ArtifactsDir,
	}
}

func LoaderRunDetailToProto(item domain.LoaderRunSummary) *agentcomposev1.LoaderRunDetail {
	return &agentcomposev1.LoaderRunDetail{Summary: LoaderRunSummaryToProto(item)}
}

func LoaderEventToProto(item domain.LoaderEvent) *agentcomposev1.LoaderEvent {
	return &agentcomposev1.LoaderEvent{
		Id:                   item.ID,
		LoaderId:             item.LoaderID,
		RunId:                item.RunID,
		TriggerId:            item.TriggerID,
		Type:                 item.Type,
		Level:                item.Level,
		Message:              item.Message,
		PayloadJson:          item.PayloadJSON,
		LinkedSessionId:      item.LinkedSessionID,
		LinkedCellId:         item.LinkedCellID,
		LinkedAgentSessionId: item.LinkedAgentSessionID,
		CreatedAt:            item.CreatedAt.Format(time.RFC3339Nano),
	}
}

func LoaderTriggerKindToProto(kind string) agentcomposev1.LoaderTriggerKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case domain.LoaderTriggerKindInterval:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_INTERVAL
	case domain.LoaderTriggerKindEvent:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_EVENT
	case domain.LoaderTriggerKindTimeout:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_TIMEOUT
	case domain.LoaderTriggerKindCron:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_CRON
	default:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_UNSPECIFIED
	}
}

func FormatMaybeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
