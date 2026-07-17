package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLICacheListTextJSONAndFilters(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			listCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
				calls++
				filter := req.Msg.GetFilter()
				switch calls {
				case 1:
					if filter.GetDriver() != "boxlite" || filter.GetType() != "materialized" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED {
						t.Fatalf("ListCaches JSON filter = %#v", filter)
					}
				case 2:
					if filter.GetDriver() != "all" || filter.GetType() != "" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
						t.Fatalf("ListCaches text filter = %#v", filter)
					}
				default:
					t.Fatalf("unexpected ListCaches call %d", calls)
				}
				return connect.NewResponse(&agentcomposev2.ListCachesResponse{
					Caches:   []*agentcomposev2.CacheItem{testCLICache("cache-materialized-1")},
					Warnings: []string{"scan warning"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("cache", "ls", "--host", server.URL, "--json", "--driver", "boxlite", "--type", "materialized", "--status", "orphaned")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("cache ls --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeCacheListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("cache ls JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Caches) != 1 || decoded.Caches[0].ID != "cache-materialized-1" || decoded.Caches[0].Type != "materialized" || decoded.Warnings[0] != "scan warning" {
		t.Fatalf("cache ls JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("cache", "ls", "--host", server.URL, "--driver", "all")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache ls text code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"CACHE ID", "cache-materi", "boxlite", "materialized", "orphaned", "/tmp/cache/rootfs"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache ls text %q does not contain %q", textOut, want)
		}
	}
	if calls != 2 {
		t.Fatalf("ListCaches calls = %d, want 2", calls)
	}
}

func TestIntegrationCLICacheListFilterValuesAndUsageErrors(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		values []string
		assert func(*testing.T, *agentcomposev2.CacheFilter, string)
	}{
		{
			name:   "driver",
			flag:   "--driver",
			values: []string{"docker", "boxlite", "microsandbox", "all"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				if filter.GetDriver() != value {
					t.Fatalf("driver filter = %q, want %q", filter.GetDriver(), value)
				}
			},
		},
		{
			name:   "type",
			flag:   "--type",
			values: []string{"oci", "materialized", "runtime", "skill"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				if filter.GetType() != value {
					t.Fatalf("type filter = %q, want %q", filter.GetType(), value)
				}
			},
		},
		{
			name:   "status",
			flag:   "--status",
			values: []string{"active", "referenced", "unused", "expired", "orphaned", "unknown"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				if cacheStatusText(filter.GetStatus()) != value {
					t.Fatalf("status filter = %s, want %q", filter.GetStatus(), value)
				}
			},
		},
	}
	for _, tc := range tests {
		for _, value := range tc.values {
			t.Run(tc.name+"_"+value, func(t *testing.T) {
				calls := 0
				server := newComposeServiceStubServer(t, composeServiceStubs{
					cache: cacheServiceStub{
						listCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
							calls++
							tc.assert(t, req.Msg.GetFilter(), value)
							return connect.NewResponse(&agentcomposev2.ListCachesResponse{}), nil
						},
					},
				})
				defer server.Close()

				stdout, stderr, _, exitCode := executeCLICommand("cache", "ls", "--host", server.URL, tc.flag, value)
				if exitCode != 0 || stderr != "" {
					t.Fatalf("cache ls %s %s code/stderr = %d / %q", tc.flag, value, exitCode, stderr)
				}
				if !strings.Contains(stdout, "CACHE ID") {
					t.Fatalf("cache ls %s %s stdout = %q", tc.flag, value, stdout)
				}
				if calls != 1 {
					t.Fatalf("ListCaches calls = %d, want 1", calls)
				}
			})
		}
	}

	invalid := []struct {
		args []string
		want string
	}{
		{args: []string{"cache", "ls", "--driver", "podman"}, want: "invalid --driver"},
		{args: []string{"cache", "ls", "--type", "blob"}, want: "invalid --type"},
		{args: []string{"cache", "ls", "--status", "deleted"}, want: "invalid --status"},
	}
	for _, tc := range invalid {
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
	}
}

func TestIntegrationCLICacheInspectTextJSONAndNotFound(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			inspectCache: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error) {
				calls++
				switch req.Msg.GetCacheId() {
				case "cache-materialized-1":
					return connect.NewResponse(&agentcomposev2.InspectCacheResponse{
						Cache:    testCLICache(req.Msg.GetCacheId()),
						Warnings: []string{"top warning"},
					}), nil
				case "missing-cache":
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cache not found"))
				default:
					t.Fatalf("unexpected InspectCache cache_id = %q", req.Msg.GetCacheId())
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "inspect", "--host", server.URL, "cache-materialized-1")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache inspect text code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"Cache ID: cache-materialized-1", "Domain: materialized-image-cache", "References:", "Blocked reasons:", "top warning"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache inspect text %q does not contain %q", textOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "inspect", "--host", server.URL, "--json", "cache-materialized-1")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache inspect JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheInspectOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache inspect JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.Cache.ID != "cache-materialized-1" || decoded.Cache.Status != "orphaned" || decoded.Warnings[0] != "top warning" {
		t.Fatalf("cache inspect JSON = %#v", decoded)
	}

	genericOut, genericErr, _, genericCode := executeCLICommand("inspect", "--host", server.URL, "--json", "cache", "cache-materialized-1")
	if genericCode != 0 || genericErr != "" {
		t.Fatalf("inspect cache JSON code/stderr = %d / %q", genericCode, genericErr)
	}
	var genericDecoded composeCacheInspectOutput
	if err := json.Unmarshal([]byte(genericOut), &genericDecoded); err != nil {
		t.Fatalf("inspect cache JSON decode failed: %v\n%s", err, genericOut)
	}
	if genericDecoded.Cache.ID != "cache-materialized-1" {
		t.Fatalf("inspect cache JSON = %#v", genericDecoded)
	}

	missingOut, missingErr, _, missingCode := executeCLICommand("cache", "inspect", "--host", server.URL, "missing-cache")
	if missingCode != exitCodeUsage {
		t.Fatalf("cache inspect missing exit code = %d, want usage; stderr=%q", missingCode, missingErr)
	}
	if missingOut != "" {
		t.Fatalf("cache inspect missing stdout = %q, want empty", missingOut)
	}
	if !strings.Contains(missingErr, "inspect cache missing-cache") || !strings.Contains(missingErr, "not_found") {
		t.Fatalf("cache inspect missing stderr = %q", missingErr)
	}
	if calls != 4 {
		t.Fatalf("InspectCache calls = %d, want 4", calls)
	}
}

func TestIntegrationCLICachePruneDryRunForceAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			pruneCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
				calls++
				switch calls {
				case 1:
					if req.Msg.GetForce() {
						t.Fatalf("PruneCaches dry-run force = true")
					}
					filter := req.Msg.GetFilter()
					if filter.GetDriver() != "boxlite" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED {
						t.Fatalf("PruneCaches dry-run filter = %#v", filter)
					}
					return connect.NewResponse(&agentcomposev2.PruneCachesResponse{
						DryRun:   true,
						Matched:  []*agentcomposev2.CacheItem{testCLICache("cache-dry-run")},
						Skipped:  []*agentcomposev2.CacheItem{testCLICache("cache-protected")},
						Warnings: []string{"scan warning"},
					}), nil
				case 2:
					if !req.Msg.GetForce() {
						t.Fatalf("PruneCaches force = false")
					}
					filter := req.Msg.GetFilter()
					if filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED || filter.GetOlderThanSeconds() != 7*24*3600 {
						t.Fatalf("PruneCaches force filter = %#v", filter)
					}
					return connect.NewResponse(&agentcomposev2.PruneCachesResponse{
						DryRun:  false,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-removed")},
						Removed: []string{"cache-removed"},
					}), nil
				default:
					t.Fatalf("unexpected PruneCaches call %d", calls)
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "prune", "--host", server.URL, "--driver", "boxlite", "--unused")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache prune dry-run code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"Dry-run", "cache-dry-run", "cache-protected", "scan warning"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache prune dry-run stdout %q does not contain %q", textOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "prune", "--host", server.URL, "--json", "--force", "--orphaned", "--older-than", "7d")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache prune force JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheOperationOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache prune JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.DryRun || len(decoded.Removed) != 1 || decoded.Removed[0] != "cache-removed" {
		t.Fatalf("cache prune JSON = %#v", decoded)
	}
	if calls != 2 {
		t.Fatalf("PruneCaches calls = %d, want 2", calls)
	}
}

func TestIntegrationCLICachePruneFilterMappings(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		assert func(*testing.T, *agentcomposev2.PruneCachesRequest)
	}{
		{
			name: "unused",
			args: []string{"--unused"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED {
					t.Fatalf("status = %s, want unused", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "orphaned",
			args: []string{"--orphaned"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED {
					t.Fatalf("status = %s, want orphaned", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "expired",
			args: []string{"--expired"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED {
					t.Fatalf("status = %s, want expired", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "older than days",
			args: []string{"--older-than", "7d"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetOlderThanSeconds() != 7*24*3600 {
					t.Fatalf("older_than_seconds = %d, want 604800", req.GetFilter().GetOlderThanSeconds())
				}
			},
		},
		{
			name: "older than hours",
			args: []string{"--older-than", "168h"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetOlderThanSeconds() != 7*24*3600 {
					t.Fatalf("request = %#v", req)
				}
			},
		},
		{
			name: "common filters",
			args: []string{"--driver", "microsandbox", "--type", "skill", "--status", "unknown"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				filter := req.GetFilter()
				if filter.GetDriver() != "microsandbox" || filter.GetType() != "skill" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN {
					t.Fatalf("filter = %#v", filter)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := newComposeServiceStubServer(t, composeServiceStubs{
				cache: cacheServiceStub{
					pruneCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
						calls++
						tc.assert(t, req.Msg)
						return connect.NewResponse(&agentcomposev2.PruneCachesResponse{DryRun: true}), nil
					},
				},
			})
			defer server.Close()
			args := append([]string{"cache", "prune", "--host", server.URL}, tc.args...)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("cache prune %v code/stderr = %d / %q", tc.args, exitCode, stderr)
			}
			if !strings.Contains(stdout, "Dry-run") {
				t.Fatalf("cache prune stdout = %q", stdout)
			}
			if calls != 1 {
				t.Fatalf("PruneCaches calls = %d, want 1", calls)
			}
		})
	}
}

func TestIntegrationCLICacheRemoveDryRunForceProtectedAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			removeCache: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
				calls++
				switch req.Msg.GetCacheId() {
				case "cache-dry-run":
					if req.Msg.GetForce() {
						t.Fatalf("RemoveCache dry-run force = true")
					}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:  true,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-dry-run")},
					}), nil
				case "cache-remove":
					if !req.Msg.GetForce() {
						t.Fatalf("RemoveCache force = false")
					}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:  false,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-remove")},
						Removed: []string{"cache-remove"},
					}), nil
				case "cache-protected":
					protected := testCLICache("cache-protected")
					protected.Status = agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE
					protected.Removable = false
					protected.BlockedReasons = []string{"cache is active"}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:   false,
						Matched:  []*agentcomposev2.CacheItem{protected},
						Skipped:  []*agentcomposev2.CacheItem{protected},
						Warnings: []string{"cache is active"},
					}), nil
				case "cache-connect-protected":
					return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cache is protected"))
				case "cache-remove-failed":
					failed := testCLICache("cache-remove-failed")
					failed.Removable = false
					failed.BlockedReasons = []string{"remove failed"}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:   false,
						Matched:  []*agentcomposev2.CacheItem{failed},
						Skipped:  []*agentcomposev2.CacheItem{failed},
						Warnings: []string{"remove cache-remove-failed: permission denied"},
					}), nil
				default:
					t.Fatalf("unexpected RemoveCache cache_id = %q", req.Msg.GetCacheId())
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "rm", "--host", server.URL, "cache-dry-run")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache rm dry-run code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "Dry-run") || !strings.Contains(textOut, "cache-dry-run") {
		t.Fatalf("cache rm dry-run stdout = %q", textOut)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "rm", "--host", server.URL, "--json", "--force", "cache-remove")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache rm force JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheOperationOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache rm JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.DryRun || len(decoded.Removed) != 1 || decoded.Removed[0] != "cache-remove" {
		t.Fatalf("cache rm JSON = %#v", decoded)
	}

	protectedOut, protectedErr, _, protectedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-protected")
	if protectedCode != exitCodeUsage {
		t.Fatalf("cache rm protected exit code = %d, want usage; stderr=%q", protectedCode, protectedErr)
	}
	if !strings.Contains(protectedOut, "Skipped") || !strings.Contains(protectedOut, "cache-protected") {
		t.Fatalf("cache rm protected stdout = %q", protectedOut)
	}
	if !strings.Contains(protectedErr, "cache is active") {
		t.Fatalf("cache rm protected stderr = %q", protectedErr)
	}

	failedOut, failedErr, _, failedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-remove-failed")
	if failedCode != exitCodeUsage {
		t.Fatalf("cache rm remove-failed exit code = %d, want usage; stderr=%q", failedCode, failedErr)
	}
	if !strings.Contains(failedOut, "Skipped") || !strings.Contains(failedOut, "cache-remove-failed") {
		t.Fatalf("cache rm remove-failed stdout = %q", failedOut)
	}
	if !strings.Contains(failedErr, "permission denied") {
		t.Fatalf("cache rm remove-failed stderr = %q", failedErr)
	}

	connectOut, connectErr, _, connectCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-connect-protected")
	if connectCode != exitCodeUsage {
		t.Fatalf("cache rm connect protected exit code = %d, want usage; stderr=%q", connectCode, connectErr)
	}
	if connectOut != "" {
		t.Fatalf("cache rm connect protected stdout = %q, want empty", connectOut)
	}
	if !strings.Contains(connectErr, "failed_precondition") {
		t.Fatalf("cache rm connect protected stderr = %q", connectErr)
	}
	if calls != 5 {
		t.Fatalf("RemoveCache calls = %d, want 5", calls)
	}
}
