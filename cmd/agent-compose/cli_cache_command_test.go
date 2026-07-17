package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestCLICacheInspectUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"cache", "inspect"}, want: "accepts 1 arg(s), received 0"},
		{name: "extra", args: []string{"cache", "inspect", "cache-id", "extra"}, want: "accepts 1 arg(s), received 2"},
		{name: "empty", args: []string{"cache", "inspect", ""}, want: "cache inspect requires a cache id"},
		{name: "generic missing", args: []string{"inspect", "cache"}, want: "inspect cache requires a cache id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

func TestCLICachePruneUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "invalid duration", args: []string{"cache", "prune", "--older-than", "bogus"}, want: "invalid --older-than"},
		{name: "zero duration", args: []string{"cache", "prune", "--older-than", "0s"}, want: "duration must be positive"},
		{name: "negative duration", args: []string{"cache", "prune", "--older-than", "-1h"}, want: "duration must be positive"},
		{name: "subsecond duration", args: []string{"cache", "prune", "--older-than", "500ms"}, want: "at least 1s"},
		{name: "shortcut conflict", args: []string{"cache", "prune", "--unused", "--orphaned"}, want: "mutually exclusive"},
		{name: "shortcut status conflict", args: []string{"cache", "prune", "--unused", "--status", "orphaned"}, want: "cannot be combined with --status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

func TestCLICacheRemoveUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"cache", "rm"}, want: "cache rm accepts 1 arg(s), received 0"},
		{name: "extra", args: []string{"cache", "rm", "cache-id", "extra"}, want: "cache rm accepts 1 arg(s), received 2"},
		{name: "empty", args: []string{"cache", "rm", ""}, want: "cache rm requires a cache id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

type cacheServiceStub struct {
	listCaches   func(context.Context, *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error)
	inspectCache func(context.Context, *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error)
	pruneCaches  func(context.Context, *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error)
	removeCache  func(context.Context, *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error)

	agentcomposev2connect.UnimplementedCacheServiceHandler
}

func (s cacheServiceStub) ListCaches(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
	if s.listCaches == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListCaches stub is not configured"))
	}
	return s.listCaches(ctx, req)
}

func (s cacheServiceStub) InspectCache(ctx context.Context, req *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error) {
	if s.inspectCache == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectCache stub is not configured"))
	}
	return s.inspectCache(ctx, req)
}

func (s cacheServiceStub) PruneCaches(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
	if s.pruneCaches == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PruneCaches stub is not configured"))
	}
	return s.pruneCaches(ctx, req)
}

func (s cacheServiceStub) RemoveCache(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
	if s.removeCache == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveCache stub is not configured"))
	}
	return s.removeCache(ctx, req)
}

func testCLICache(cacheID string) *agentcomposev2.CacheItem {
	return &agentcomposev2.CacheItem{
		CacheId:        cacheID,
		Domain:         agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE,
		Driver:         "boxlite",
		Kind:           "materialized-rootfs",
		Path:           "/tmp/cache/rootfs",
		SizeBytes:      4096,
		ImageId:        "sha256:cache",
		ImageRef:       "agent:latest",
		ResolvedRef:    "agent@sha256:cache",
		Status:         agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED,
		Removable:      true,
		BlockedReasons: []string{"dry-run only"},
		LastUsedAt:     "2026-06-11T00:00:00Z",
		LastUsedSource: "mtime",
		References: []*agentcomposev2.CacheReference{{
			Type:        "image-metadata",
			Id:          "sha256:cache",
			Name:        "agent:latest",
			Path:        "/tmp/cache/rootfs",
			Status:      "stopped",
			Description: "agent@sha256:cache",
		}},
		Warnings: []string{"item warning"},
	}
}

func requireCLICacheByPath(t *testing.T, caches []composeCacheOutput, path string) composeCacheOutput {
	t.Helper()
	for _, cache := range caches {
		if cache.Path == path {
			if strings.TrimSpace(cache.ID) == "" {
				t.Fatalf("cache for path %s has empty cache id: %#v", path, cache)
			}
			return cache
		}
	}
	t.Fatalf("missing cache for path %s in %#v", path, caches)
	return composeCacheOutput{}
}
