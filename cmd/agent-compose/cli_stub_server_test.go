package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

type composeServiceStubs struct {
	project  projectServiceStub
	run      runServiceStub
	exec     execServiceStub
	resource resourceServiceStub
	image    imageServiceStub
	cache    cacheServiceStub
	volume   volumeServiceStub
	sandbox  sandboxServiceStub
	session  sessionServiceStub
}

type resourceServiceStub struct {
	resolveID func(context.Context, *connect.Request[agentcomposev2.ResolveResourceIDRequest]) (*connect.Response[agentcomposev2.ResolveResourceIDResponse], error)

	agentcomposev2connect.UnimplementedResourceServiceHandler
}

func newComposeServiceStubServer(t *testing.T, stubs composeServiceStubs) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if stubs.project.applyProject != nil || stubs.project.getProject != nil || stubs.project.listProjects != nil || stubs.project.removeProject != nil || stubs.project.getScheduler != nil || stubs.project.listSchedulerEvents != nil || stubs.project.runScheduler != nil || stubs.project.startSchedulerRun != nil || stubs.project.getSchedulerRun != nil || stubs.project.listSchedulerRuns != nil || stubs.project.stopSchedulerRun != nil {
		path, handler := agentcomposev2connect.NewProjectServiceHandler(stubs.project)
		mux.Handle(path, handler)
	}
	if stubs.run.startRun != nil || stubs.run.runAgentStream != nil || stubs.run.getRun != nil || stubs.run.listRuns != nil || stubs.run.listRunEvents != nil || stubs.run.followRunLogs != nil {
		path, handler := agentcomposev2connect.NewRunServiceHandler(stubs.run)
		mux.Handle(path, handler)
	}
	if stubs.exec.exec != nil || stubs.exec.execStream != nil || stubs.exec.execAttach != nil {
		path, handler := agentcomposev2connect.NewExecServiceHandler(stubs.exec)
		mux.Handle(path, handler)
	}
	if stubs.resource.resolveID != nil {
		path, handler := agentcomposev2connect.NewResourceServiceHandler(stubs.resource)
		mux.Handle(path, handler)
	}
	if stubs.image.listImages != nil || stubs.image.pullImage != nil || stubs.image.inspectImage != nil || stubs.image.removeImage != nil || stubs.image.buildImage != nil {
		path, handler := agentcomposev2connect.NewImageServiceHandler(stubs.image)
		mux.Handle(path, handler)
	}
	if stubs.cache.listCaches != nil || stubs.cache.inspectCache != nil || stubs.cache.pruneCaches != nil || stubs.cache.removeCache != nil {
		path, handler := agentcomposev2connect.NewCacheServiceHandler(stubs.cache)
		mux.Handle(path, handler)
	}
	if stubs.volume.listVolumes != nil || stubs.volume.createVolume != nil || stubs.volume.inspectVolume != nil || stubs.volume.removeVolume != nil || stubs.volume.pruneVolumes != nil {
		path, handler := agentcomposev2connect.NewVolumeServiceHandler(stubs.volume)
		mux.Handle(path, handler)
	}
	effectiveSandbox := sandboxStubWithSessionCompatibility(stubs.sandbox, stubs.session)
	if effectiveSandbox.removeSandbox != nil || effectiveSandbox.getStats != nil || effectiveSandbox.getSandbox != nil || effectiveSandbox.listSandboxes != nil || effectiveSandbox.stopSandbox != nil || effectiveSandbox.resumeSandbox != nil {
		path, handler := agentcomposev2connect.NewSandboxServiceHandler(effectiveSandbox)
		mux.Handle(path, handler)
	}
	return httptest.NewServer(mux)
}
