//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/cache"

	_ "modernc.org/sqlite"
)

const boxliteDBFileName = "boxlite.db"

const (
	boxliteLocalImageCacheKind = "boxlite-local-image"
	boxliteDiskImageCacheKind  = "boxlite-disk-image"
	boxliteLastUsedSourceMTime = "mtime"
	boxliteReferenceTypeDB     = "boxlite-db"
)

func hasActiveBoxliteBoxes(dbPath string) (bool, error) {
	state, err := inspectBoxliteActiveState(dbPath)
	if err != nil {
		return false, err
	}
	if !state.known {
		return false, fmt.Errorf("boxlite active state is unknown: %s", state.warning)
	}
	return state.active, nil
}

type boxliteActiveState struct {
	known   bool
	active  bool
	warning string
}

func inspectBoxliteActiveState(dbPath string) (boxliteActiveState, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return boxliteActiveState{warning: "boxlite db path is empty"}, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return boxliteActiveState{warning: fmt.Sprintf("boxlite db %s is missing", dbPath)}, nil
		}
		return boxliteActiveState{}, fmt.Errorf("stat boxlite db %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return boxliteActiveState{}, fmt.Errorf("open boxlite db %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	var count int
	err = db.QueryRow(`SELECT COUNT(1) FROM box_state WHERE LOWER(TRIM(status)) NOT IN ('', 'stopped', 'exited', 'dead', 'removed')`).Scan(&count)
	if err != nil {
		if isBoxliteUnknownSchemaError(err) {
			return boxliteActiveState{warning: fmt.Sprintf("boxlite db schema is unknown: %v", err)}, nil
		}
		return boxliteActiveState{}, fmt.Errorf("query active boxlite boxes: %w", err)
	}
	return boxliteActiveState{known: true, active: count > 0}, nil
}

func listBoxliteRuntimeDerivedCaches(ctx context.Context, boxliteHome string) (cache.ListResult, error) {
	if err := ctx.Err(); err != nil {
		return cache.ListResult{}, err
	}
	boxliteHome = strings.TrimSpace(boxliteHome)
	if boxliteHome == "" {
		return cache.ListResult{Warnings: []string{"boxlite runtime cache scan skipped: BOXLITE_HOME is empty"}}, nil
	}
	if !filepath.IsAbs(boxliteHome) {
		return cache.ListResult{Warnings: []string{fmt.Sprintf("boxlite runtime cache scan skipped: BOXLITE_HOME %s is not absolute", boxliteHome)}}, nil
	}

	dbPath := filepath.Join(boxliteHome, boxliteDBFileName)
	state, stateWarnings := boxliteInventoryActiveState(dbPath)
	result := cache.ListResult{Warnings: stateWarnings}
	for _, root := range boxliteRuntimeDerivedRoots(boxliteHome) {
		items, warnings := scanBoxliteRuntimeDerivedRoot(ctx, root.path, root.kind, state, dbPath)
		result.Items = append(result.Items, items...)
		result.Warnings = cache.AppendWarnings(result.Warnings, warnings...)
	}
	return result, nil
}

func pruneBoxliteRuntimeDerivedCaches(ctx context.Context, boxliteHome string, req cache.PruneRequest) (cache.Result, error) {
	list, err := listBoxliteRuntimeDerivedCaches(ctx, boxliteHome)
	if err != nil {
		return cache.Result{}, err
	}
	result, err := cache.PruneItems(ctx, cache.ItemRemovalContext{Items: list.Items, Now: time.Now().UTC(), Remove: boxliteRuntimeDerivedRemover{boxliteHome: boxliteHome}.Remove}, req)
	if err != nil {
		return result, err
	}
	result.Warnings = cache.AppendWarnings(list.Warnings, result.Warnings...)
	return result, nil
}

func removeBoxliteRuntimeDerivedCache(ctx context.Context, boxliteHome string, req cache.RemoveRequest) (cache.Result, error) {
	list, err := listBoxliteRuntimeDerivedCaches(ctx, boxliteHome)
	if err != nil {
		return cache.Result{}, err
	}
	result, err := cache.RemoveItem(ctx, cache.ItemRemovalContext{Items: list.Items, Now: time.Now().UTC(), Remove: boxliteRuntimeDerivedRemover{boxliteHome: boxliteHome}.Remove}, req)
	if err != nil {
		return result, err
	}
	result.Warnings = cache.AppendWarnings(list.Warnings, result.Warnings...)
	return result, nil
}

type boxliteRuntimeDerivedRoot struct {
	path string
	kind string
}

func boxliteRuntimeDerivedRoots(boxliteHome string) []boxliteRuntimeDerivedRoot {
	return []boxliteRuntimeDerivedRoot{
		{path: filepath.Join(boxliteHome, "images", "local"), kind: boxliteLocalImageCacheKind},
		{path: filepath.Join(boxliteHome, "images", "disk-images"), kind: boxliteDiskImageCacheKind},
	}
}

func boxliteInventoryActiveState(dbPath string) (boxliteActiveState, []string) {
	state, err := inspectBoxliteActiveState(dbPath)
	if err != nil {
		return boxliteActiveState{warning: err.Error()}, []string{err.Error()}
	}
	if !state.known && strings.TrimSpace(state.warning) != "" {
		return state, []string{state.warning}
	}
	return state, nil
}

func scanBoxliteRuntimeDerivedRoot(ctx context.Context, root, kind string, state boxliteActiveState, dbPath string) ([]cache.Item, []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read boxlite runtime cache root %s: %v", root, err)}
	}
	items := make([]cache.Item, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, []string{err.Error()}
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			item := boxliteRuntimeDerivedItem(path, kind, nil, state, dbPath)
			item.Warnings = cache.AppendWarnings(item.Warnings, fmt.Sprintf("stat boxlite runtime cache item %s: %v", path, err))
			items = append(items, cache.EvaluateProtection(item))
			continue
		}
		items = append(items, boxliteRuntimeDerivedItem(path, kind, info, state, dbPath))
	}
	return items, nil
}

func boxliteRuntimeDerivedItem(path, kind string, info os.FileInfo, state boxliteActiveState, dbPath string) cache.Item {
	size, warnings := cache.EstimateSize(path)
	item := cache.Item{
		Domain:         cache.DomainRuntimeDerivedCache,
		Driver:         cache.DriverBoxLite,
		Kind:           kind,
		Path:           path,
		SizeBytes:      size,
		Status:         cache.StatusOrphaned,
		LastUsedSource: boxliteLastUsedSourceMTime,
		Warnings:       warnings,
	}
	if info != nil {
		item.LastUsedAt = info.ModTime().UTC()
	}
	switch {
	case !state.known:
		item.Status = cache.StatusUnknown
		item.Warnings = cache.AppendWarnings(item.Warnings, state.warning)
	case state.active:
		item.Status = cache.StatusActive
		item.References = []cache.Reference{{
			Type:        boxliteReferenceTypeDB,
			Path:        dbPath,
			Status:      "active",
			Description: "active BoxLite box state exists",
		}}
	default:
		item.Status = cache.StatusOrphaned
	}
	if cacheID, err := cache.GenerateCacheID(item); err == nil {
		item.CacheID = cacheID
	} else {
		item.Status = cache.StatusUnknown
		item.Warnings = cache.AppendWarnings(item.Warnings, err.Error())
	}
	if item.Status != cache.StatusActive {
		item.Status = cache.StatusUnknown
		item.Warnings = cache.AppendWarnings(item.Warnings, "BoxLite v0.10.4 ABI does not support safe image remove/prune")
	}
	return cache.EvaluateProtection(item)
}

type boxliteRuntimeDerivedRemover struct {
	boxliteHome string
}

func (r boxliteRuntimeDerivedRemover) Remove(ctx context.Context, item cache.Item) error {
	return fmt.Errorf("%w: BoxLite v0.10.4 ABI does not support safe image remove/prune", cache.ErrRemoveUnavailable)
}

func isBoxliteUnknownSchemaError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") || strings.Contains(message, "no such column")
}
