package main

import (
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type composePSOptions struct {
	All     bool
	Status  string
	Verbose bool
}

func formatDurationMs(value int64) string {
	if value <= 0 {
		return "-"
	}
	return time.Duration(value * int64(time.Millisecond)).String()
}

func resolveComposeAgentNameFromSpec(normalized *compose.NormalizedProjectSpec, projectID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent ref is required")}
	}
	if normalized == nil {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %q not found in current project", ref)}
	}
	candidates := make([]composeAgentRefCandidate, 0, len(normalized.Agents))
	for _, agent := range normalized.Agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		id, err := domain.StableManagedAgentID(projectID, name)
		if err != nil {
			return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("resolve agent %q id: %w", name, err)}
		}
		candidates = append(candidates, composeAgentRefCandidate{Name: name, ID: id, ShortID: shortOpaqueID(id)})
	}
	return resolveComposeAgentNameFromCandidates(ref, candidates)
}

func resolveComposeAgentNameFromCandidates(ref string, candidates []composeAgentRefCandidate) (string, error) {
	ref = strings.TrimSpace(ref)
	for _, candidate := range candidates {
		if candidate.Name == ref {
			return candidate.Name, nil
		}
	}
	var matches []composeAgentRefCandidate
	for _, candidate := range candidates {
		if resourceIDMatchesRef(candidate.ID, candidate.ShortID, ref) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %q not found in current project", ref)}
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Name)
		}
		sort.Strings(names)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent ref %q is ambiguous in current project; matches: %s", ref, strings.Join(names, ", "))}
	}
	return matches[0].Name, nil
}

func writeDeprecatedWarning(out io.Writer, oldUsage string, newUsage string) error {
	_, err := fmt.Fprintf(out, "Warning: %s is deprecated and will be removed in a future release; use %s instead.\n", oldUsage, newUsage)
	return err
}

func loadResolvedNormalizedCompose(ctx context.Context, cli cliOptions) (string, *compose.NormalizedProjectSpec, error) {
	return loadNormalizedComposeWithOptions(ctx, cli, true)
}

func writeComposeChangeTable(out io.Writer, changes []composeDisplayChangeOutput) error {
	hasMessage := false
	for _, change := range changes {
		if strings.TrimSpace(change.Message) != "" {
			hasMessage = true
			break
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "ID\tNAME\tTYPE\tACTION"
	if hasMessage {
		header += "\tMESSAGE"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for _, change := range changes {
		format := "%s\t%s\t%s\t%s\n"
		args := []any{
			change.ID,
			change.Name,
			change.ResourceType,
			change.Action,
		}
		if hasMessage {
			format = "%s\t%s\t%s\t%s\t%s\n"
			args = append(args, change.Message)
		}
		if _, err := fmt.Fprintf(tw, format, args...); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func (b *composeDisplayChangeBuilder) add(change composeDisplayChangeOutput) {
	if change.ResourceType == "" || change.ResourceType == "project_revision" {
		return
	}
	key := composeDisplayChangeKey(change)
	if index, ok := b.seenKey[key]; ok {
		b.items[index] = mergeComposeDisplayChange(b.items[index], change)
		return
	}
	b.seenKey[key] = len(b.items)
	b.items = append(b.items, change)
}

func firstNonZeroUint64(values ...uint64) uint64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func writeStringListSection(out io.Writer, title string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "%s:\n", title); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(out, "- %s\n", value); err != nil {
			return err
		}
	}
	return nil
}

func writeStringMapSection(out io.Writer, title string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if _, err := fmt.Fprintf(out, "%s:\n", title); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := fmt.Fprintf(out, "- %s=%s\n", key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func formatProtoTimestamp(value interface{ AsTime() time.Time }) string {
	if value == nil {
		return ""
	}
	parsed := value.AsTime()
	if parsed.IsZero() {
		return "invalid"
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func parseOlderThanSeconds(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		daysText := strings.TrimSpace(strings.TrimSuffix(value, "d"))
		days, parseErr := strconv.ParseFloat(daysText, 64)
		if parseErr != nil {
			err = parseErr
		} else {
			duration = time.Duration(days * float64(24*time.Hour))
		}
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid --older-than %q: expected a positive duration such as 7d or 24h", value)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid --older-than %q: duration must be positive", value)
	}
	if duration < time.Second {
		return 0, fmt.Errorf("invalid --older-than %q: duration must be at least 1s", value)
	}
	return uint64(duration / time.Second), nil
}

func displayOpaqueID(id string) string {
	return strings.TrimPrefix(strings.TrimSpace(id), identity.Prefix)
}

func displayOpaqueIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, displayOpaqueID(id))
	}
	return out
}

func shortOpaqueID(id string) string {
	id = displayOpaqueID(id)
	if id == "" {
		return ""
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func cloneStringMapForCLI(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func shellQuoteCLIArg(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}();&|<>#") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

const (
	exitCodeGeneral     = 1
	exitCodeUsage       = 2
	exitCodeUnavailable = 3
	exitCodeUnsupported = 4
)
