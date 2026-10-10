package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// A provider write must hand the saved connection to the probe, so an operator
// learns the protocol verdict at configuration time rather than at the first
// request.
func TestProviderWriteRunsTheUpstreamProbe(t *testing.T) {
	ctx := context.Background()
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	recorder := &recordingUpstreamProber{}
	handler := NewLLMHandler(nil, store).WithUpstreamProbe(recorder)

	spec := &agentcomposev2.LLMProviderSpec{
		Id: "gateway", BaseUrl: "https://example.com/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret"),
	}
	if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	probed := recorder.last()
	if probed.ID != "gateway" || probed.DefaultWireAPI != llms.APIProtocolResponses {
		t.Fatalf("created connection probed as %#v", probed)
	}

	spec.ApiKey = nil
	spec.Protocol = "chat_completions"
	if _, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	probed = recorder.last()
	if probed.ID != "gateway" || probed.DefaultWireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("updated connection probed as %#v", probed)
	}
	if got := recorder.count(); got != 2 {
		t.Fatalf("probe calls = %d, want one per write", got)
	}
}

// A handler without a prober must still serve provider writes.
func TestProviderWriteWithoutProberSucceeds(t *testing.T) {
	ctx := context.Background()
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	handler := NewLLMHandler(nil, store)
	spec := &agentcomposev2.LLMProviderSpec{Id: "gateway", BaseUrl: "https://example.com/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret")}
	if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("CreateProvider without prober: %v", err)
	}
}

type recordingUpstreamProber struct {
	mu    sync.Mutex
	calls []llms.Provider
}

func (r *recordingUpstreamProber) ProbeConnection(_ context.Context, provider llms.Provider, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, provider)
}

func (r *recordingUpstreamProber) last() llms.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return llms.Provider{}
	}
	return r.calls[len(r.calls)-1]
}

func (r *recordingUpstreamProber) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}
