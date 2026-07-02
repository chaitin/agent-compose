package agentcompose

import (
	"time"

	loaderdomain "agent-compose/internal/agentcompose/loader"
)

const loaderDefaultCronTimezone = loaderdomain.DefaultCronTimezone

type loaderCronSpec = loaderdomain.CronSpec

func loaderTriggerNextFireAt(now time.Time, trigger LoaderTrigger, fired bool) (time.Time, error) {
	return loaderdomain.TriggerNextFireAt(now, trigger, fired)
}

func loaderTriggerSource(trigger LoaderTrigger) string {
	return loaderdomain.TriggerSource(trigger)
}

func normalizeLoaderCronSpecJSON(raw string) (string, error) {
	return loaderdomain.NormalizeCronSpecJSON(raw)
}

func loaderCronSpecJSON(expr, timezone string) (string, error) {
	return loaderdomain.CronSpecJSON(expr, timezone)
}

func parseLoaderCronSpecJSON(raw string) (loaderCronSpec, error) {
	return loaderdomain.ParseCronSpecJSON(raw)
}

func normalizeLoaderCronSpec(spec loaderCronSpec) (loaderCronSpec, error) {
	return loaderdomain.NormalizeCronSpec(spec)
}
