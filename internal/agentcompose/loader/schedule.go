package loader

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

const DefaultCronTimezone = "UTC"

type CronSpec struct {
	Kind     string `json:"kind,omitempty"`
	Expr     string `json:"expr"`
	Timezone string `json:"timezone,omitempty"`
}

var cronParser = cronlib.NewParser(cronlib.SecondOptional | cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow | cronlib.Descriptor)

func TriggerNextFireAt(now time.Time, trigger Trigger, fired bool) (time.Time, error) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(trigger.Kind)) {
	case TriggerKindInterval:
		return TriggerScheduledAt(now, trigger.IntervalMs), nil
	case TriggerKindTimeout:
		if fired {
			return time.Time{}, nil
		}
		return TriggerScheduledAt(now, trigger.IntervalMs), nil
	case TriggerKindCron:
		spec, err := ParseCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return time.Time{}, err
		}
		location, err := time.LoadLocation(spec.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
		}
		schedule, err := cronParser.Parse(spec.Expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
		}
		return schedule.Next(now.In(location)).UTC(), nil
	default:
		return time.Time{}, nil
	}
}

func TriggerSource(trigger Trigger) string {
	switch strings.ToLower(strings.TrimSpace(trigger.Kind)) {
	case TriggerKindInterval:
		return fmt.Sprintf("interval:%d", trigger.IntervalMs)
	case TriggerKindTimeout:
		return fmt.Sprintf("timeout:%d", trigger.IntervalMs)
	case TriggerKindCron:
		spec, err := ParseCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return "cron"
		}
		return fmt.Sprintf("cron:%s@%s", spec.Expr, spec.Timezone)
	default:
		return ""
	}
}

func NormalizeCronSpecJSON(raw string) (string, error) {
	spec, err := ParseCronSpecJSON(raw)
	if err != nil {
		return "", err
	}
	return MarshalJSONCompact(spec)
}

func CronSpecJSON(expr, timezone string) (string, error) {
	spec, err := NormalizeCronSpec(CronSpec{
		Kind:     TriggerKindCron,
		Expr:     expr,
		Timezone: timezone,
	})
	if err != nil {
		return "", err
	}
	return MarshalJSONCompact(spec)
}

func ParseCronSpecJSON(raw string) (CronSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CronSpec{}, fmt.Errorf("cron spec is required")
	}
	var spec CronSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return CronSpec{}, fmt.Errorf("decode cron spec: %w", err)
	}
	return NormalizeCronSpec(spec)
}

func NormalizeCronSpec(spec CronSpec) (CronSpec, error) {
	spec.Kind = TriggerKindCron
	spec.Expr = strings.TrimSpace(spec.Expr)
	spec.Timezone = strings.TrimSpace(spec.Timezone)
	if spec.Expr == "" {
		return CronSpec{}, fmt.Errorf("cron expr is required")
	}
	if spec.Timezone == "" {
		spec.Timezone = DefaultCronTimezone
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return CronSpec{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
	}
	if _, err := cronParser.Parse(spec.Expr); err != nil {
		return CronSpec{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
	}
	return spec, nil
}

func MarshalJSONCompact(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode json payload: %w", err)
	}
	return string(data), nil
}
