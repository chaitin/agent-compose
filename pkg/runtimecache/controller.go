package runtimecache

import (
	"context"
	"fmt"
	"time"
)

type Source interface {
	List(context.Context) (ListResult, error)
	Remove(context.Context, Item) error
}

type Controller struct {
	Sources []Source
	Now     func() time.Time
}

func (c *Controller) ListCaches(ctx context.Context, req ListRequest) (ListResult, error) {
	items, warnings, _, err := c.inventory(ctx)
	if err != nil {
		return ListResult{}, err
	}
	filtered, err := FilterItems(items, req.Filter, c.now())
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: filtered, Warnings: warnings}, nil
}

func (c *Controller) InspectCache(ctx context.Context, cacheID string) (ListResult, error) {
	result, err := c.ListCaches(ctx, ListRequest{Filter: Filter{CacheID: cacheID}})
	if err != nil {
		return result, err
	}
	if len(result.Items) == 0 {
		return result, fmt.Errorf("%w: %s", ErrCacheNotFound, cacheID)
	}
	return result, nil
}

func (c *Controller) PruneCaches(ctx context.Context, req PruneRequest) (Result, error) {
	items, warnings, sources, err := c.inventory(ctx)
	if err != nil {
		return Result{}, err
	}
	result, err := PruneItems(ctx, items, req, c.now(), sourceRemoveFunc(sources))
	if err != nil {
		return result, err
	}
	result.Warnings = AppendWarnings(warnings, result.Warnings...)
	return result, nil
}

func (c *Controller) RemoveCache(ctx context.Context, req RemoveRequest) (Result, error) {
	items, warnings, sources, err := c.inventory(ctx)
	if err != nil {
		return Result{}, err
	}
	result, err := RemoveItem(ctx, items, req, c.now(), sourceRemoveFunc(sources))
	if err != nil {
		return result, err
	}
	result.Warnings = AppendWarnings(warnings, result.Warnings...)
	return result, nil
}

func (c *Controller) inventory(ctx context.Context) ([]Item, []string, map[string]Source, error) {
	var items []Item
	var warnings []string
	sources := make(map[string]Source)
	for _, source := range c.Sources {
		if source == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		result, err := source.List(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		warnings = AppendWarnings(warnings, result.Warnings...)
		for _, item := range result.Items {
			items = append(items, item)
			if item.CacheID != "" {
				sources[item.CacheID] = source
			}
		}
	}
	return items, warnings, sources, nil
}

func (c *Controller) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func sourceRemoveFunc(sources map[string]Source) RemoveFunc {
	return func(ctx context.Context, item Item) error {
		source := sources[item.CacheID]
		if source == nil {
			return ErrRemoveUnavailable
		}
		return source.Remove(ctx, item)
	}
}

type MaterializedSource struct {
	Scanner MaterializedScanner
	Remover MaterializedRemover
}

func (s MaterializedSource) List(ctx context.Context) (ListResult, error) {
	return s.Scanner.List(ctx)
}

func (s MaterializedSource) Remove(ctx context.Context, item Item) error {
	return s.Remover.Remove(ctx, item)
}
