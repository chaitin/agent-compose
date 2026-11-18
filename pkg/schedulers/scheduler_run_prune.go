package schedulers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type SchedulerRunPruneFilter struct {
	SchedulerIDs []string
	TriggerID    string
	Statuses     []string
	OlderThan    time.Duration
	Now          time.Time
}

type SchedulerRunPruneRequest struct {
	SchedulerIDs []string
	TriggerID    string
	Statuses     []string
	OlderThan    time.Duration
	Force        bool
}

type SchedulerRunPruneStats struct {
	Runs              uint64
	SchedulerEvents   uint64
	EventDeliveries   uint64
	EventSandboxLinks uint64
	ArtifactDirs      uint64
	ArtifactBytes     uint64
}

type SchedulerRunPruneResidue struct {
	SchedulerID string
	RunID       string
	Path        string
	Error       string
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
	SchedulerEvents   uint64
	EventDeliveries   uint64
	EventSandboxLinks uint64
	Runs              uint64
}

type SchedulerRunPruneDatabaseResult struct {
	Stats       SchedulerRunPruneDatabaseStats
	RemovedKeys []SchedulerRunKey
}

type SchedulerRunPruneStore interface {
	ListSchedulerRunsForPrune(context.Context, SchedulerRunPruneFilter) ([]domain.SchedulerRunSummary, error)
	CountSchedulerRunPruneData(context.Context, []SchedulerRunKey) (SchedulerRunPruneDatabaseStats, error)
	DeleteSchedulerRunPruneData(context.Context, []SchedulerRunKey) (SchedulerRunPruneDatabaseResult, error)
}

type SchedulerRunArtifactPruner interface {
	InspectRunArtifacts(schedulerID, runID, recordedDir string) (SchedulerRunArtifactInfo, error)
	RemoveRunArtifacts(schedulerID, runID, recordedDir string) (SchedulerRunArtifactInfo, error)
}

type SchedulerRunArtifactInfo struct {
	Path   string
	Exists bool
	Bytes  uint64
}

type schedulerRunPruneCandidate struct {
	run      domain.SchedulerRunSummary
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
	runs, err := store.ListSchedulerRunsForPrune(ctx, filter)
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
	databaseStats, err := store.CountSchedulerRunPruneData(ctx, keys)
	if err != nil {
		return result, err
	}
	addSchedulerRunPruneDatabaseStats(&result.Matched, databaseStats)

	artifactPruner, _ := c.deps.Artifacts.(SchedulerRunArtifactPruner)
	if len(runs) > 0 && artifactPruner == nil {
		return result, fmt.Errorf("scheduler run artifact pruner is unavailable")
	}
	candidates := c.collectSchedulerRunPruneCandidates(runs, artifactPruner, &result)
	if result.DryRun || len(candidates) == 0 {
		return result, nil
	}

	removedDatabase, err := store.DeleteSchedulerRunPruneData(ctx, schedulerRunCandidateKeys(candidates))
	if err != nil {
		return result, err
	}
	addSchedulerRunPruneDatabaseStats(&result.Removed, removedDatabase.Stats)
	removedKeys := make(map[SchedulerRunKey]struct{}, len(removedDatabase.RemovedKeys))
	for _, key := range removedDatabase.RemovedKeys {
		removedKeys[key] = struct{}{}
	}
	removeSchedulerRunPruneArtifacts(artifactPruner, candidates, removedKeys, &result)
	return result, nil
}

// collectSchedulerRunPruneCandidates inspects each run's artifacts, skipping
// runs owned by a busy scheduler, and accumulates matched-artifact stats and
// warnings into result.
func (c *Controller) collectSchedulerRunPruneCandidates(runs []domain.SchedulerRunSummary, artifactPruner SchedulerRunArtifactPruner, result *SchedulerRunPruneResult) []schedulerRunPruneCandidate {
	candidates := make([]schedulerRunPruneCandidate, 0, len(runs))
	busySchedulers := make(map[string]struct{})
	invalidArtifacts := 0
	for _, run := range runs {
		if c.schedulerBusy(run.SchedulerID) {
			result.SkippedRuns++
			busySchedulers[run.SchedulerID] = struct{}{}
			continue
		}
		candidate := schedulerRunPruneCandidate{run: run}
		var err error
		candidate.artifact, err = artifactPruner.InspectRunArtifacts(run.SchedulerID, run.ID, run.ArtifactsDir)
		if err != nil {
			result.SkippedRuns++
			invalidArtifacts++
			result.Warnings = append(result.Warnings, fmt.Sprintf("scheduler run %s/%s artifacts are unsafe to prune: %v", run.SchedulerID, run.ID, err))
			continue
		}
		if candidate.artifact.Exists {
			result.Matched.ArtifactDirs++
			result.Matched.ArtifactBytes += candidate.artifact.Bytes
		}
		candidates = append(candidates, candidate)
	}
	if len(busySchedulers) > 0 {
		busyRuns := result.SkippedRuns - min(result.SkippedRuns, uint64(invalidArtifacts))
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d matching run(s) from %d busy scheduler(s)", busyRuns, len(busySchedulers)))
	}
	return candidates
}

// removeSchedulerRunPruneArtifacts removes on-disk artifacts for candidates
// whose database rows were actually deleted, accumulating removed-artifact
// stats, residues, and warnings into result.
func removeSchedulerRunPruneArtifacts(artifactPruner SchedulerRunArtifactPruner, candidates []schedulerRunPruneCandidate, removedKeys map[SchedulerRunKey]struct{}, result *SchedulerRunPruneResult) {
	for _, candidate := range candidates {
		key := SchedulerRunKey{SchedulerID: candidate.run.SchedulerID, RunID: candidate.run.ID}
		if _, removed := removedKeys[key]; !removed {
			result.SkippedRuns++
			result.Warnings = append(result.Warnings, fmt.Sprintf("scheduler run %s/%s no longer matched during force recheck and was not removed", candidate.run.SchedulerID, candidate.run.ID))
			continue
		}
		if !candidate.artifact.Exists {
			continue
		}
		removed, removeErr := artifactPruner.RemoveRunArtifacts(candidate.run.SchedulerID, candidate.run.ID, candidate.run.ArtifactsDir)
		if removeErr != nil {
			result.Residues = append(result.Residues, SchedulerRunPruneResidue{
				SchedulerID: candidate.run.SchedulerID,
				RunID:       candidate.run.ID,
				Path:        candidate.artifact.Path,
				Error:       removeErr.Error(),
			})
			continue
		}
		if removed.Exists {
			result.Removed.ArtifactDirs++
			result.Removed.ArtifactBytes += removed.Bytes
		}
	}
}

func normalizeSchedulerRunPruneFilter(request SchedulerRunPruneRequest, now time.Time) (SchedulerRunPruneFilter, error) {
	schedulerIDs := idset.Canonical(request.SchedulerIDs)
	if request.OlderThan < 0 {
		return SchedulerRunPruneFilter{}, domain.ClassifyError(domain.ErrInvalidArgument, "scheduler run prune older-than must not be negative", nil)
	}
	statuses := request.Statuses
	if len(statuses) == 0 {
		statuses = []string{
			domain.SchedulerRunStatusSucceeded,
			domain.SchedulerRunStatusFailed,
			domain.SchedulerRunStatusCanceled,
			domain.SchedulerRunStatusSkipped,
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
		SchedulerIDs: schedulerIDs,
		TriggerID:    strings.TrimSpace(request.TriggerID),
		Statuses:     normalizedStatuses,
		OlderThan:    request.OlderThan,
		Now:          now.UTC(),
	}, nil
}

func schedulerRunKeys(runs []domain.SchedulerRunSummary) []SchedulerRunKey {
	keys := make([]SchedulerRunKey, 0, len(runs))
	for _, run := range runs {
		keys = append(keys, SchedulerRunKey{SchedulerID: run.SchedulerID, RunID: run.ID})
	}
	return keys
}

func schedulerRunCandidateKeys(candidates []schedulerRunPruneCandidate) []SchedulerRunKey {
	keys := make([]SchedulerRunKey, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, SchedulerRunKey{SchedulerID: candidate.run.SchedulerID, RunID: candidate.run.ID})
	}
	return keys
}

func addSchedulerRunPruneDatabaseStats(target *SchedulerRunPruneStats, source SchedulerRunPruneDatabaseStats) {
	target.Runs += source.Runs
	target.SchedulerEvents += source.SchedulerEvents
	target.EventDeliveries += source.EventDeliveries
	target.EventSandboxLinks += source.EventSandboxLinks
}

func (c *Controller) schedulerBusy(schedulerID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	schedulerID = strings.TrimSpace(schedulerID)
	// Exact history removal and prune must not delete the same scheduler's
	// rows and artifact directories concurrently.
	return c.running[schedulerID] > 0 || c.skipRecording[schedulerID] > 0 || c.historyRemoving[schedulerID]
}
