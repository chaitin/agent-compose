package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/runtimecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestCacheHandlerListCachesMapsFilterAndResponse(t *testing.T) {
	item := testRuntimeCacheItem(t, runtimecache.StatusOrphaned)
	controller := &fakeCacheController{
		listResult: runtimecache.ListResult{Items: []runtimecache.Item{item}, Warnings: []string{"top warning"}},
	}
	handler := NewCacheHandler(controller)

	resp, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{
		Driver:           runtimecache.DriverMicrosandbox,
		Domain:           agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE,
		Type:             string(runtimecache.CacheTypeSession),
		Status:           agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED,
		OlderThanSeconds: 7,
		CacheId:          item.CacheID,
	}}))
	if err != nil {
		t.Fatalf("ListCaches: %v", err)
	}
	if controller.listReq.Filter.Driver != runtimecache.DriverMicrosandbox ||
		controller.listReq.Filter.Domain != runtimecache.DomainSessionEphemeralState ||
		controller.listReq.Filter.Type != runtimecache.CacheTypeSession ||
		controller.listReq.Filter.Status != runtimecache.StatusOrphaned ||
		controller.listReq.Filter.OlderThan != 7*time.Second ||
		controller.listReq.Filter.CacheID != item.CacheID {
		t.Fatalf("mapped filter = %#v", controller.listReq.Filter)
	}
	if len(resp.Msg.GetCaches()) != 1 {
		t.Fatalf("cache count = %d, want 1", len(resp.Msg.GetCaches()))
	}
	got := resp.Msg.GetCaches()[0]
	if got.GetCacheId() != item.CacheID ||
		got.GetDomain() != agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE ||
		got.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED ||
		got.GetLastUsedAt() == "" ||
		len(got.GetReferences()) != 1 ||
		len(resp.Msg.GetWarnings()) != 1 {
		t.Fatalf("mapped response cache = %#v warnings=%#v", got, resp.Msg.GetWarnings())
	}
}

func TestCacheHandlerInspectCache(t *testing.T) {
	item := testRuntimeCacheItem(t, runtimecache.StatusUnknown)
	controller := &fakeCacheController{
		inspectResult: runtimecache.ListResult{Items: []runtimecache.Item{item}, Warnings: []string{"inspect warning"}},
	}
	handler := NewCacheHandler(controller)

	resp, err := handler.InspectCache(context.Background(), connect.NewRequest(&agentcomposev2.InspectCacheRequest{CacheId: item.CacheID}))
	if err != nil {
		t.Fatalf("InspectCache: %v", err)
	}
	if controller.inspectID != item.CacheID {
		t.Fatalf("inspectID = %q, want %q", controller.inspectID, item.CacheID)
	}
	if resp.Msg.GetCache().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN ||
		resp.Msg.GetCache().GetRemovable() ||
		len(resp.Msg.GetWarnings()) != 1 {
		t.Fatalf("InspectCache response = %#v warnings=%#v", resp.Msg.GetCache(), resp.Msg.GetWarnings())
	}
}

func TestCacheHandlerInspectCacheNotFound(t *testing.T) {
	handler := NewCacheHandler(&fakeCacheController{})
	_, err := handler.InspectCache(context.Background(), connect.NewRequest(&agentcomposev2.InspectCacheRequest{CacheId: testRuntimeCacheID(t, runtimecache.StatusOrphaned)}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("InspectCache code = %v, want NotFound (err=%v)", connect.CodeOf(err), err)
	}
}

func TestCacheHandlerPruneCachesMapsRequestAndResult(t *testing.T) {
	matched := testRuntimeCacheItem(t, runtimecache.StatusOrphaned)
	controller := &fakeCacheController{
		pruneResult: runtimecache.Result{
			DryRun:   false,
			Matched:  []runtimecache.Item{matched},
			Removed:  []string{matched.CacheID},
			Warnings: []string{"removed"},
		},
	}
	handler := NewCacheHandler(controller)

	resp, err := handler.PruneCaches(context.Background(), connect.NewRequest(&agentcomposev2.PruneCachesRequest{
		Filter:            &agentcomposev2.CacheFilter{Status: agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED},
		IncludeReferenced: true,
		Force:             true,
	}))
	if err != nil {
		t.Fatalf("PruneCaches: %v", err)
	}
	if controller.pruneReq.Filter.Status != runtimecache.StatusOrphaned || !controller.pruneReq.IncludeReferenced || !controller.pruneReq.Force {
		t.Fatalf("prune request = %#v", controller.pruneReq)
	}
	if resp.Msg.GetDryRun() || len(resp.Msg.GetMatched()) != 1 || len(resp.Msg.GetRemoved()) != 1 || len(resp.Msg.GetWarnings()) != 1 {
		t.Fatalf("PruneCaches response = %#v", resp.Msg)
	}
}

func TestCacheHandlerRemoveCacheProtectedSkipped(t *testing.T) {
	active := testRuntimeCacheItem(t, runtimecache.StatusActive)
	controller := &fakeCacheController{
		removeResult: runtimecache.Result{
			DryRun:  false,
			Matched: []runtimecache.Item{active},
			Skipped: []runtimecache.Item{active},
		},
	}
	handler := NewCacheHandler(controller)

	resp, err := handler.RemoveCache(context.Background(), connect.NewRequest(&agentcomposev2.RemoveCacheRequest{CacheId: active.CacheID, Force: true}))
	if err != nil {
		t.Fatalf("RemoveCache: %v", err)
	}
	if controller.removeReq.CacheID != active.CacheID || !controller.removeReq.Force {
		t.Fatalf("remove request = %#v", controller.removeReq)
	}
	if len(resp.Msg.GetSkipped()) != 1 || resp.Msg.GetSkipped()[0].GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE {
		t.Fatalf("RemoveCache response = %#v", resp.Msg)
	}
}

func TestCacheHandlerValidationErrors(t *testing.T) {
	handler := NewCacheHandler(&fakeCacheController{})
	for _, tc := range []struct {
		name string
		call func() error
		code connect.Code
	}{
		{
			name: "unknown domain",
			call: func() error {
				_, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{Domain: agentcomposev2.CacheDomain(99)}}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "unknown status",
			call: func() error {
				_, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{Status: agentcomposev2.CacheStatus(99)}}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "unknown driver",
			call: func() error {
				_, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{Driver: "bogus"}}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "unknown type",
			call: func() error {
				_, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{Type: "bogus"}}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "older than overflow",
			call: func() error {
				_, err := handler.ListCaches(context.Background(), connect.NewRequest(&agentcomposev2.ListCachesRequest{Filter: &agentcomposev2.CacheFilter{OlderThanSeconds: ^uint64(0)}}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "missing inspect id",
			call: func() error {
				_, err := handler.InspectCache(context.Background(), connect.NewRequest(&agentcomposev2.InspectCacheRequest{}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "invalid remove id",
			call: func() error {
				_, err := handler.RemoveCache(context.Background(), connect.NewRequest(&agentcomposev2.RemoveCacheRequest{CacheId: "not-valid"}))
				return err
			},
			code: connect.CodeInvalidArgument,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); connect.CodeOf(err) != tc.code {
				t.Fatalf("code = %v, want %v (err=%v)", connect.CodeOf(err), tc.code, err)
			}
		})
	}
}

func TestConnectErrorForRuntimeCache(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{runtimecache.ErrCacheNotFound, connect.CodeNotFound},
		{runtimecache.ErrInvalidFilter, connect.CodeInvalidArgument},
		{runtimecache.ErrInvalidCacheID, connect.CodeInvalidArgument},
		{runtimecache.ErrUnsafePath, connect.CodeFailedPrecondition},
		{runtimecache.ErrRemoveUnavailable, connect.CodeUnavailable},
		{errors.New("boom"), connect.CodeInternal},
	} {
		if got := connect.CodeOf(ConnectErrorForRuntimeCache(tc.err)); got != tc.code {
			t.Fatalf("ConnectErrorForRuntimeCache(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
}

type fakeCacheController struct {
	listReq       runtimecache.ListRequest
	listResult    runtimecache.ListResult
	listErr       error
	inspectID     string
	inspectResult runtimecache.ListResult
	inspectErr    error
	pruneReq      runtimecache.PruneRequest
	pruneResult   runtimecache.Result
	pruneErr      error
	removeReq     runtimecache.RemoveRequest
	removeResult  runtimecache.Result
	removeErr     error
}

func (f *fakeCacheController) ListCaches(_ context.Context, req runtimecache.ListRequest) (runtimecache.ListResult, error) {
	f.listReq = req
	return f.listResult, f.listErr
}

func (f *fakeCacheController) InspectCache(_ context.Context, cacheID string) (runtimecache.ListResult, error) {
	f.inspectID = cacheID
	return f.inspectResult, f.inspectErr
}

func (f *fakeCacheController) PruneCaches(_ context.Context, req runtimecache.PruneRequest) (runtimecache.Result, error) {
	f.pruneReq = req
	return f.pruneResult, f.pruneErr
}

func (f *fakeCacheController) RemoveCache(_ context.Context, req runtimecache.RemoveRequest) (runtimecache.Result, error) {
	f.removeReq = req
	return f.removeResult, f.removeErr
}

func testRuntimeCacheItem(t *testing.T, status runtimecache.Status) runtimecache.Item {
	t.Helper()
	item := runtimecache.EvaluateProtection(runtimecache.Item{
		Domain:         runtimecache.DomainSessionEphemeralState,
		Driver:         runtimecache.DriverMicrosandbox,
		Kind:           "microsandbox-docker-disk",
		Path:           "/tmp/microsandbox/docker-disks/session.raw",
		SizeBytes:      12,
		SessionID:      "session",
		Status:         status,
		LastUsedAt:     time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		LastUsedSource: "mtime",
		References: []runtimecache.Reference{{
			Type:        "session",
			ID:          "session",
			Name:        "Session",
			Path:        "/tmp/session.json",
			Status:      string(status),
			Description: "test reference",
		}},
		Warnings: []string{"item warning"},
	}, false)
	cacheID, err := runtimecache.GenerateCacheID(item)
	if err != nil {
		t.Fatalf("GenerateCacheID: %v", err)
	}
	item.CacheID = cacheID
	return item
}

func testRuntimeCacheID(t *testing.T, status runtimecache.Status) string {
	t.Helper()
	return testRuntimeCacheItem(t, status).CacheID
}
