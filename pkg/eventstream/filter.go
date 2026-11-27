package eventstream

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidEventType rejects an unknown exact type or a malformed prefix.
	ErrInvalidEventType = errors.New("invalid event type")
)

// Filter is a compiled subscription filter. It is immutable after NewFilter.
type Filter struct {
	projectIDs  map[string]struct{}
	patterns    []string
	allTypes    bool
	minSeverity Severity
	includeLogs bool
	// explicitLogs are log types the caller selected by exact name. They are
	// delivered even when includeLogs is false, which remains the wildcard and
	// empty-list gate that prevents accidental high-volume subscriptions.
	explicitLogs map[string]struct{}
}

// NewFilter validates and compiles a subscription filter. An empty project
// list selects every project; an empty type list selects every type subject to
// includeLogs. An exact log type is always included; includeLogs controls log
// types matched by a wildcard or by the empty list, so "run.*" does not
// accidentally opt into high-volume logs.
func NewFilter(projectIDs, types []string, minSeverity Severity, includeLogs bool) (Filter, error) {
	if minSeverity < SeverityInfo || minSeverity > SeverityError {
		return Filter{}, fmt.Errorf("invalid minimum severity %d", minSeverity)
	}
	filter := Filter{minSeverity: minSeverity, includeLogs: includeLogs, allTypes: len(types) == 0}
	if len(projectIDs) > 0 {
		filter.projectIDs = make(map[string]struct{}, len(projectIDs))
		for _, raw := range projectIDs {
			projectID := strings.TrimSpace(raw)
			if projectID == "" {
				return Filter{}, fmt.Errorf("project id must not be empty")
			}
			filter.projectIDs[projectID] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(types))
	for _, raw := range types {
		eventType := strings.TrimSpace(raw)
		if eventType == "" {
			return Filter{}, fmt.Errorf("%w: empty type", ErrInvalidEventType)
		}
		if _, ok := seen[eventType]; ok {
			continue
		}
		seen[eventType] = struct{}{}
		if prefix, wildcard := strings.CutSuffix(eventType, ".*"); wildcard {
			if prefix == "" || strings.Contains(prefix, "*") {
				return Filter{}, fmt.Errorf("%w: %q", ErrInvalidEventType, raw)
			}
			filter.patterns = append(filter.patterns, prefix+".*")
			continue
		}
		if strings.Contains(eventType, "*") {
			return Filter{}, fmt.Errorf("%w: %q", ErrInvalidEventType, raw)
		}
		if !IsKnownType(eventType) {
			return Filter{}, fmt.Errorf("%w: %q", ErrInvalidEventType, raw)
		}
		if IsLogType(eventType) {
			if filter.explicitLogs == nil {
				filter.explicitLogs = make(map[string]struct{})
			}
			filter.explicitLogs[eventType] = struct{}{}
		}
		filter.patterns = append(filter.patterns, eventType)
	}
	return filter, nil
}

// Matches reports whether event belongs to this subscription.
func (f Filter) Matches(event *Event) bool {
	if event == nil || event.Severity < f.minSeverity {
		return false
	}
	if len(f.projectIDs) > 0 {
		if strings.TrimSpace(event.ProjectID) == "" {
			return false
		}
		if _, ok := f.projectIDs[event.ProjectID]; !ok {
			return false
		}
	}
	if IsLogType(event.Type) && !f.includeLogs {
		if _, explicit := f.explicitLogs[event.Type]; !explicit {
			return false
		}
	}
	if f.allTypes {
		return true
	}
	for _, pattern := range f.patterns {
		if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
			if strings.HasPrefix(event.Type, prefix+".") {
				return true
			}
			continue
		}
		if event.Type == pattern {
			return true
		}
	}
	return false
}

// IncludeLogs reports whether a wildcard or empty type list admits log
// event types. Explicitly selected log types are always admitted.
func (f Filter) IncludeLogs() bool {
	return f.includeLogs
}
