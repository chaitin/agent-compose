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
	handler, recorder := newProbedLLMHandler(t)

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
	handler := newLLMTestHandler(t)
	spec := &agentcomposev2.LLMProviderSpec{Id: "gateway", BaseUrl: "https://example.com/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret")}
	if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("CreateProvider without prober: %v", err)
	}
}

// newLLMTestHandler builds a handler over an in-memory configuration store,
// which is the shape every provider write test needs.
func newLLMTestHandler(t *testing.T) *LLMHandler {
	t.Helper()
	return NewLLMHandler(nil, newLLMTestStore(t))
}

// newProbedLLMHandler adds a recording prober so a test can observe what the
// write path handed to the probe.
func newProbedLLMHandler(t *testing.T) (*LLMHandler, *recordingUpstreamProber) {
	t.Helper()
	recorder := &recordingUpstreamProber{}
	return NewLLMHandler(nil, newLLMTestStore(t)).WithUpstreamProbe(recorder), recorder
}

func newLLMTestStore(t *testing.T) *configstore.ConfigStore {
	t.Helper()
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return configstore.FromDB(db.DB())
}

type probeCall struct {
	provider llms.Provider
	model    string
}

type recordingUpstreamProber struct {
	mu     sync.Mutex
	calls  []probeCall
	cached map[string]llms.UpstreamProbeResult
}

func (r *recordingUpstreamProber) ProbeConnection(_ context.Context, provider llms.Provider, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, probeCall{provider: provider, model: model})
}

func (r *recordingUpstreamProber) Capabilities(provider llms.Provider) (llms.UpstreamProbeResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, ok := r.cached[provider.ID]
	return result, ok
}

func (r *recordingUpstreamProber) last() llms.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return llms.Provider{}
	}
	return r.calls[len(r.calls)-1].provider
}

func (r *recordingUpstreamProber) lastModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return ""
	}
	return r.calls[len(r.calls)-1].model
}

func (r *recordingUpstreamProber) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}
