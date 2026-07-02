package loader

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

const loaderDefaultCronTimezone = "UTC"

type loaderCronSpec struct {
	Kind     string `json:"kind,omitempty"`
	Expr     string `json:"expr"`
	Timezone string `json:"timezone,omitempty"`
}

var loaderCronParser = cronlib.NewParser(cronlib.SecondOptional | cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow | cronlib.Descriptor)

func loaderTriggerNextFireAt(now time.Time, trigger LoaderTrigger, fired bool) (time.Time, error) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(trigger.Kind)) {
	case LoaderTriggerKindInterval:
		return loaderTriggerScheduledAt(now, trigger.IntervalMs), nil
	case LoaderTriggerKindTimeout:
		if fired {
			return time.Time{}, nil
		}
		return loaderTriggerScheduledAt(now, trigger.IntervalMs), nil
	case LoaderTriggerKindCron:
		spec, err := parseLoaderCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return time.Time{}, err
		}
		location, err := time.LoadLocation(spec.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
		}
		schedule, err := loaderCronParser.Parse(spec.Expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
		}
		return schedule.Next(now.In(location)).UTC(), nil
	default:
		return time.Time{}, nil
	}
}

func loaderTriggerSource(trigger LoaderTrigger) string {
	switch strings.ToLower(strings.TrimSpace(trigger.Kind)) {
	case LoaderTriggerKindInterval:
		return fmt.Sprintf("interval:%d", trigger.IntervalMs)
	case LoaderTriggerKindTimeout:
		return fmt.Sprintf("timeout:%d", trigger.IntervalMs)
	case LoaderTriggerKindCron:
		spec, err := parseLoaderCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return "cron"
		}
		return fmt.Sprintf("cron:%s@%s", spec.Expr, spec.Timezone)
	default:
		return ""
	}
}

func normalizeLoaderCronSpecJSON(raw string) (string, error) {
	spec, err := parseLoaderCronSpecJSON(raw)
	if err != nil {
		return "", err
	}
	return marshalJSONCompact(spec)
}

func loaderCronSpecJSON(expr, timezone string) (string, error) {
	spec, err := normalizeLoaderCronSpec(loaderCronSpec{
		Kind:     LoaderTriggerKindCron,
		Expr:     expr,
		Timezone: timezone,
	})
	if err != nil {
		return "", err
	}
	return marshalJSONCompact(spec)
}

func LoaderCronSpecJSON(expr, timezone string) (string, error) {
	return loaderCronSpecJSON(expr, timezone)
}

func parseLoaderCronSpecJSON(raw string) (loaderCronSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return loaderCronSpec{}, fmt.Errorf("cron spec is required")
	}
	var spec loaderCronSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return loaderCronSpec{}, fmt.Errorf("decode cron spec: %w", err)
	}
	return normalizeLoaderCronSpec(spec)
}

func normalizeLoaderCronSpec(spec loaderCronSpec) (loaderCronSpec, error) {
	spec.Kind = LoaderTriggerKindCron
	spec.Expr = strings.TrimSpace(spec.Expr)
	spec.Timezone = strings.TrimSpace(spec.Timezone)
	if spec.Expr == "" {
		return loaderCronSpec{}, fmt.Errorf("cron expr is required")
	}
	if spec.Timezone == "" {
		spec.Timezone = loaderDefaultCronTimezone
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return loaderCronSpec{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
	}
	if _, err := loaderCronParser.Parse(spec.Expr); err != nil {
		return loaderCronSpec{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
	}
	return spec, nil
}
