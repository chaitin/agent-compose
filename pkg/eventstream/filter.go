package eventstream

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidEventType rejects an unknown exact type or a malformed prefix.
	ErrInvalidEventType = errors.New("invalid event type")
	// ErrLogEventsDisabled rejects an exact log type when include_logs is false.
	ErrLogEventsDisabled = errors.New("log events require include_logs")
)

// Filter is a compiled subscription filter. It is immutable after NewFilter.
type Filter struct {
	projectIDs  map[string]struct{}
	patterns    []string
	allTypes    bool
	minSeverity Severity
	includeLogs bool
}

// NewFilter validates and compiles a subscription filter. An empty project
// list selects every project; an empty type list selects every type subject to
// includeLogs. Exact log types are rejected when includeLogs is false, while a
// wildcard such as "run.*" simply excludes logs in that case.
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
		if IsLogType(eventType) && !includeLogs {
			return Filter{}, fmt.Errorf("%w: %q", ErrLogEventsDisabled, raw)
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
		return false
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

// IncludeLogs reports whether the filter admits log event types.
func (f Filter) IncludeLogs() bool {
	return f.includeLogs
}
