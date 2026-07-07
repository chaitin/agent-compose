package loaders

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/capabilities"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func NormalizeLoader(item domain.Loader, assignID bool) (domain.Loader, error) {
	now := time.Now().UTC()
	item.Summary.ID = strings.TrimSpace(item.Summary.ID)
	if assignID && item.Summary.ID == "" {
		item.Summary.ID = uuid.NewString()
	}
	if item.Summary.ID == "" {
		return domain.Loader{}, fmt.Errorf("loader id is required")
	}
	item.Summary.Name = strings.TrimSpace(item.Summary.Name)
	if item.Summary.Name == "" {
		item.Summary.Name = domain.DefaultLoaderName(now)
	}
	item.Summary.Description = strings.TrimSpace(item.Summary.Description)
	runtime, err := domain.NormalizeLoaderRuntime(item.Summary.Runtime)
	if err != nil {
		return domain.Loader{}, err
	}
	item.Summary.Runtime = runtime
	item.Script = strings.ReplaceAll(item.Script, "\r\n", "\n")
	if strings.TrimSpace(item.Script) == "" {
		return domain.Loader{}, fmt.Errorf("loader script is required")
	}
	item.Summary.WorkspaceID = strings.TrimSpace(item.Summary.WorkspaceID)
	item.Summary.AgentID = strings.TrimSpace(item.Summary.AgentID)
	item.Summary.Driver = strings.TrimSpace(item.Summary.Driver)
	if item.Summary.Driver != "" {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(item.Summary.Driver, item.Summary.Driver)
		if err != nil {
			return domain.Loader{}, err
		}
		item.Summary.Driver = driver
	}
	item.Summary.GuestImage = strings.TrimSpace(item.Summary.GuestImage)
	item.Summary.DefaultAgent = domain.NormalizeAgentKind(item.Summary.DefaultAgent)
	if item.Summary.DefaultAgent == "" {
		item.Summary.DefaultAgent = "codex"
	}
	item.Summary.SessionPolicy = domain.NormalizeLoaderSessionPolicy(item.Summary.SessionPolicy)
	item.Summary.ConcurrencyPolicy = domain.NormalizeLoaderConcurrencyPolicy(item.Summary.ConcurrencyPolicy)
	item.Summary.CapsetIDs = capabilities.NormalizeCapsetIDs(item.Summary.CapsetIDs)
	item.Summary.ManagedProjectID = strings.TrimSpace(item.Summary.ManagedProjectID)
	item.Summary.ManagedAgentName = strings.TrimSpace(item.Summary.ManagedAgentName)
	item.Summary.ManagedSchedulerID = strings.TrimSpace(item.Summary.ManagedSchedulerID)
	if item.Summary.ManagedProjectID == "" {
		item.Summary.ManagedRevision = 0
		item.Summary.ManagedAgentName = ""
		item.Summary.ManagedSchedulerID = ""
	} else {
		if item.Summary.ManagedAgentName == "" || item.Summary.ManagedSchedulerID == "" {
			return domain.Loader{}, fmt.Errorf("scheduler execution agent name and scheduler id are required")
		}
		if item.Summary.ManagedRevision < 0 {
			return domain.Loader{}, fmt.Errorf("scheduler execution project revision cannot be negative")
		}
	}
	item.EnvItems = domain.NormalizeEnvItems(item.EnvItems)
	item.Triggers = append([]domain.LoaderTrigger(nil), item.Triggers...)
	return item, nil
}

func EncodeEnvItems(items []domain.SessionEnvVar) (string, error) {
	normalized := domain.NormalizeEnvItems(items)
	if normalized == nil {
		normalized = []domain.SessionEnvVar{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode loader env items: %w", err)
	}
	return string(data), nil
}

func DecodeEnvItems(raw string) ([]domain.SessionEnvVar, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []domain.SessionEnvVar
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode loader env items: %w", err)
	}
	return domain.NormalizeEnvItems(items), nil
}

func NormalizeLoaderTrigger(loaderID string, trigger domain.LoaderTrigger) (domain.LoaderTrigger, error) {
	trigger.LoaderID = strings.TrimSpace(loaderID)
	trigger.ID = strings.TrimSpace(trigger.ID)
	if trigger.LoaderID == "" {
		return domain.LoaderTrigger{}, fmt.Errorf("loader id is required")
	}
	if trigger.ID == "" {
		return domain.LoaderTrigger{}, fmt.Errorf("loader trigger id is required")
	}
	kind, err := domain.NormalizeLoaderTriggerKind(trigger.Kind)
	if err != nil {
		return domain.LoaderTrigger{}, err
	}
	trigger.Kind = kind
	trigger.Topic = strings.TrimSpace(trigger.Topic)
	switch trigger.Kind {
	case domain.LoaderTriggerKindInterval:
		if trigger.IntervalMs <= 0 {
			return domain.LoaderTrigger{}, fmt.Errorf("loader interval trigger %s requires a positive interval", trigger.ID)
		}
		trigger.Topic = ""
	case domain.LoaderTriggerKindEvent:
		if trigger.Topic == "" {
			return domain.LoaderTrigger{}, fmt.Errorf("loader event trigger %s requires a topic", trigger.ID)
		}
		trigger.IntervalMs = 0
	case domain.LoaderTriggerKindTimeout:
		if trigger.IntervalMs <= 0 {
			return domain.LoaderTrigger{}, fmt.Errorf("loader timeout trigger %s requires a positive delay", trigger.ID)
		}
		trigger.Topic = ""
	case domain.LoaderTriggerKindCron:
		trigger.Topic = ""
		trigger.IntervalMs = 0
		normalizedSpecJSON, err := NormalizeLoaderCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return domain.LoaderTrigger{}, fmt.Errorf("loader cron trigger %s: %w", trigger.ID, err)
		}
		trigger.SpecJSON = normalizedSpecJSON
	}
	trigger.SpecJSON = strings.TrimSpace(trigger.SpecJSON)
	if trigger.SpecJSON == "" {
		trigger.SpecJSON = "{}"
	}
	if !domain.TimeIsSet(trigger.NextFireAt) {
		trigger.NextFireAt = time.Time{}
	} else {
		trigger.NextFireAt = trigger.NextFireAt.UTC()
	}
	if !domain.TimeIsSet(trigger.LastFiredAt) {
		trigger.LastFiredAt = time.Time{}
	} else {
		trigger.LastFiredAt = trigger.LastFiredAt.UTC()
	}
	return trigger, nil
}
