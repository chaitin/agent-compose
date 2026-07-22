package loaders

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

type SchedulerRunPruneFilter struct {
	LoaderIDs []string
	TriggerID string
	Statuses  []string
	OlderThan time.Duration
	Now       time.Time
}

type SchedulerRunPruneRequest struct {
	LoaderIDs []string
	TriggerID string
	Statuses  []string
	OlderThan time.Duration
	Force     bool
}

type SchedulerRunPruneStats struct {
	Runs              uint64
	LoaderEvents      uint64
	EventDeliveries   uint64
	EventSandboxLinks uint64
	ArtifactDirs      uint64
	ArtifactBytes     uint64
}

type SchedulerRunPruneResidue struct {
	LoaderID string
	RunID    string
	Path     string
	Error    string
}

type SchedulerRunPruneResult struct {
	DryRun      bool
	Matched     SchedulerRunPruneStats
	Removed     SchedulerRunPruneStats
	SkippedRuns uint64
	Residues    []SchedulerRunPruneResidue
	Warnings    []string
}

type SchedulerRunPruneDatabaseStats struct {
	LoaderEvents      uint64
	EventDeliveries   uint64
	EventSandboxLinks uint64
	Runs              uint64
}

type SchedulerRunPruneDatabaseResult struct {
	Stats       SchedulerRunPruneDatabaseStats
	RemovedKeys []LoaderRunKey
}

type SchedulerRunPruneStore interface {
	ListLoaderRunsForPrune(context.Context, SchedulerRunPruneFilter) ([]domain.LoaderRunSummary, error)
	CountLoaderRunPruneData(context.Context, []LoaderRunKey) (SchedulerRunPruneDatabaseStats, error)
	DeleteLoaderRunPruneData(context.Context, []LoaderRunKey) (SchedulerRunPruneDatabaseResult, error)
}

type SchedulerRunArtifactPruner interface {
	InspectRunArtifacts(loaderID, runID, recordedDir string) (SchedulerRunArtifactInfo, error)
	RemoveRunArtifacts(loaderID, runID, recordedDir string) (SchedulerRunArtifactInfo, error)
}

type SchedulerRunArtifactInfo struct {
	Path   string
	Exists bool
	Bytes  uint64
}

type schedulerRunPruneCandidate struct {
	run      domain.LoaderRunSummary
	artifact SchedulerRunArtifactInfo
}

func (c *Controller) PruneSchedulerRuns(ctx context.Context, request SchedulerRunPruneRequest) (SchedulerRunPruneResult, error) {
	store, ok := c.deps.Store.(SchedulerRunPruneStore)
	if !ok || store == nil {
		return SchedulerRunPruneResult{}, fmt.Errorf("scheduler run prune store is unavailable")
	}
	filter, err := normalizeSchedulerRunPruneFilter(request, c.now())
	if err != nil {
		return SchedulerRunPruneResult{}, err
	}
	runs, err := store.ListLoaderRunsForPrune(ctx, filter)
	if err != nil {
		return SchedulerRunPruneResult{}, err
	}
	result := SchedulerRunPruneResult{DryRun: !request.Force}
	if filter.OlderThan > 0 {
		missingCompletedAt := 0
		for _, run := range runs {
			if run.CompletedAt.IsZero() {
				missingCompletedAt++
			}
		}
		if missingCompletedAt > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%d terminal run(s) have no completion timestamp; older-than used their start time", missingCompletedAt))
		}
	}
	keys := schedulerRunKeys(runs)
	databaseStats, err := store.CountLoaderRunPruneData(ctx, keys)
	if err != nil {
		return result, err
	}
	addSchedulerRunPruneDatabaseStats(&result.Matched, databaseStats)

	artifactPruner, _ := c.deps.Artifacts.(SchedulerRunArtifactPruner)
	if len(runs) > 0 && artifactPruner == nil {
		return result, fmt.Errorf("scheduler run artifact pruner is unavailable")
	}
	candidates := make([]schedulerRunPruneCandidate, 0, len(runs))
	busyLoaders := make(map[string]struct{})
	invalidArtifacts := 0
	for _, run := range runs {
		if c.loaderBusy(run.LoaderID) {
			result.SkippedRuns++
			busyLoaders[run.LoaderID] = struct{}{}
			continue
		}
		candidate := schedulerRunPruneCandidate{run: run}
		candidate.artifact, err = artifactPruner.InspectRunArtifacts(run.LoaderID, run.ID, run.ArtifactsDir)
		if err != nil {
			result.SkippedRuns++
			invalidArtifacts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("scheduler run %s/%s artifacts are unsafe to prune: %v", run.LoaderID, run.ID, err))
			continue
		}
		if candidate.artifact.Exists {
			result.Matched.ArtifactDirs++
			result.Matched.ArtifactBytes += candidate.artifact.Bytes
		}
		candidates = append(candidates, candidate)
	}
	if len(busyLoaders) > 0 {
		busyRuns := result.SkippedRuns - min(result.SkippedRuns, uint64(invalidArtifacts))
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d matching run(s) from %d busy scheduler(s)", busyRuns, len(busyLoaders)))
	}
	if result.DryRun || len(candidates) == 0 {
		return result, nil
	}

	removedDatabase, err := store.DeleteLoaderRunPruneData(ctx, schedulerRunCandidateKeys(candidates))
	if err != nil {
		return result, err
	}
	addSchedulerRunPruneDatabaseStats(&result.Removed, removedDatabase.Stats)
	removedKeys := make(map[LoaderRunKey]struct{}, len(removedDatabase.RemovedKeys))
	for _, key := range removedDatabase.RemovedKeys {
		removedKeys[key] = struct{}{}
	}
	for _, candidate := range candidates {
		key := LoaderRunKey{LoaderID: candidate.run.LoaderID, RunID: candidate.run.ID}
		if _, removed := removedKeys[key]; !removed {
			result.SkippedRuns++
			result.Warnings = append(result.Warnings, fmt.Sprintf("scheduler run %s/%s no longer matched during force recheck and was not removed", candidate.run.LoaderID, candidate.run.ID))
			continue
		}
		if !candidate.artifact.Exists {
			continue
		}
		removed, removeErr := artifactPruner.RemoveRunArtifacts(candidate.run.LoaderID, candidate.run.ID, candidate.run.ArtifactsDir)
		if removeErr != nil {
			result.Residues = append(result.Residues, SchedulerRunPruneResidue{
				LoaderID: candidate.run.LoaderID,
				RunID:    candidate.run.ID,
				Path:     candidate.artifact.Path,
				Error:    removeErr.Error(),
			})
			continue
		}
		if removed.Exists {
			result.Removed.ArtifactDirs++
			result.Removed.ArtifactBytes += removed.Bytes
		}
	}
	return result, nil
}

func normalizeSchedulerRunPruneFilter(request SchedulerRunPruneRequest, now time.Time) (SchedulerRunPruneFilter, error) {
	loaderIDs := normalizedStrings(request.LoaderIDs)
	if request.OlderThan < 0 {
		return SchedulerRunPruneFilter{}, domain.ClassifyError(domain.ErrInvalidArgument, "scheduler run prune older-than must not be negative", nil)
	}
	statuses := request.Statuses
	if len(statuses) == 0 {
		statuses = []string{
			domain.LoaderRunStatusSucceeded,
			domain.LoaderRunStatusFailed,
			domain.LoaderRunStatusCanceled,
			domain.LoaderRunStatusSkipped,
		}
	}
	normalizedStatuses := make([]string, 0, len(statuses))
	seenStatuses := make(map[string]struct{}, len(statuses))
	for _, raw := range statuses {
		status := strings.ToLower(strings.TrimSpace(raw))
		if !SchedulerRunStatusIsTerminal(status) {
			return SchedulerRunPruneFilter{}, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("scheduler run prune status %q is not terminal", raw), nil)
		}
		if _, exists := seenStatuses[status]; exists {
			continue
		}
		seenStatuses[status] = struct{}{}
		normalizedStatuses = append(normalizedStatuses, status)
	}
	sort.Strings(normalizedStatuses)
	return SchedulerRunPruneFilter{
		LoaderIDs: loaderIDs,
		TriggerID: strings.TrimSpace(request.TriggerID),
		Statuses:  normalizedStatuses,
		OlderThan: request.OlderThan,
		Now:       now.UTC(),
	}, nil
}

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func schedulerRunKeys(runs []domain.LoaderRunSummary) []LoaderRunKey {
	keys := make([]LoaderRunKey, 0, len(runs))
	for _, run := range runs {
		keys = append(keys, LoaderRunKey{LoaderID: run.LoaderID, RunID: run.ID})
	}
	return keys
}

func schedulerRunCandidateKeys(candidates []schedulerRunPruneCandidate) []LoaderRunKey {
	keys := make([]LoaderRunKey, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, LoaderRunKey{LoaderID: candidate.run.LoaderID, RunID: candidate.run.ID})
	}
	return keys
}

func addSchedulerRunPruneDatabaseStats(target *SchedulerRunPruneStats, source SchedulerRunPruneDatabaseStats) {
	target.Runs += source.Runs
	target.LoaderEvents += source.LoaderEvents
	target.EventDeliveries += source.EventDeliveries
	target.EventSandboxLinks += source.EventSandboxLinks
}

func (c *Controller) loaderBusy(loaderID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running[strings.TrimSpace(loaderID)] > 0
}
