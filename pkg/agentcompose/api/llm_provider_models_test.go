package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/llms"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// A caller configures a connection and the model names an operator typed, and
// the spec's presence rule decides whether an update replaces that set.
func TestProviderDeclaredModelsRoundTrip(t *testing.T) {
	ctx := context.Background()
	handler, recorder := newProbedLLMHandler(t)
	spec := &agentcomposev2.LLMProviderSpec{
		Id: "gateway", BaseUrl: "https://gateway.example/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret"),
		Models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{
			{Id: "gpt-4o", Name: "GPT-4o"},
			{Id: "gpt-4o-mini", Protocol: "chat_completions", MaxOutputTokens: proto.Int32(4096)},
		}},
	}
	created, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec}))
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	models := created.Msg.GetProvider().GetModels()
	if len(models) != 2 || models[0].GetId() != "gpt-4o" || models[0].GetProtocol() != "" || models[1].GetProtocol() != "chat_completions" || models[1].GetMaxOutputTokens() != 4096 {
		t.Fatalf("created models = %#v", models)
	}
	// A declared model is what the probe should name, so the verdict describes
	// the model the operator will actually use.
	if got := recorder.lastModel(); got != "gpt-4o" {
		t.Fatalf("probed model = %q, want the first declared model", got)
	}

	// An update that omits models preserves them, and the response says so.
	spec.ApiKey = nil
	spec.Models = nil
	updated, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec}))
	if err != nil {
		t.Fatalf("UpdateProvider without models: %v", err)
	}
	if got := updated.Msg.GetProvider().GetModels(); len(got) != 2 {
		t.Fatalf("models after an update without models = %#v, want them preserved", got)
	}

	// A present empty set clears them.
	spec.Models = &agentcomposev2.LLMProviderModels{}
	cleared, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec}))
	if err != nil {
		t.Fatalf("UpdateProvider with an empty set: %v", err)
	}
	if got := cleared.Msg.GetProvider().GetModels(); len(got) != 0 {
		t.Fatalf("models after clearing = %#v, want none", got)
	}

	// A model outside the connection's protocol family is rejected at the
	// transport boundary as an invalid argument.
	spec.Models = &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{{Id: "claude", Protocol: "anthropic_messages"}}}
	if _, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cross-family model error = %v, want an invalid-argument error", err)
	}
}

// Capabilities report what the daemon already knows; a connection it has not
// probed reports none rather than an empty or invented verdict.
func TestProviderCapabilitiesComeFromTheProbeCache(t *testing.T) {
	ctx := context.Background()
	handler, recorder := newProbedLLMHandler(t)
	spec := &agentcomposev2.LLMProviderSpec{
		Id: "gateway", BaseUrl: "https://gateway.example/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret"),
	}
	if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	created, err := handler.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: "gateway"}))
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if created.Msg.GetProvider().Capabilities != nil {
		t.Fatalf("unprobed capabilities = %#v, want absent", created.Msg.GetProvider().GetCapabilities())
	}

	probedAt := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	recorder.mu.Lock()
	recorder.cached = map[string]llms.UpstreamProbeResult{
		"gateway": {
			Endpoint: "https://gateway.example/v1", Model: "gpt-4o", Models: []string{"gpt-4o", "gpt-4o-mini"},
			ProbedAt: probedAt,
			Protocols: []llms.ProtocolProbe{
				{Protocol: llms.APIProtocolChatCompletions, Outcome: llms.ProbeSupported},
				{Protocol: llms.APIProtocolResponses, Outcome: llms.ProbeInconclusive, Detail: "endpoint answered 500"},
			},
		},
	}
	recorder.mu.Unlock()

	fetched, err := handler.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: "gateway"}))
	if err != nil {
		t.Fatalf("GetProvider with a cached verdict: %v", err)
	}
	capabilities := fetched.Msg.GetProvider().GetCapabilities()
	if capabilities.GetProbedModel() != "gpt-4o" || len(capabilities.GetModels()) != 2 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if !capabilities.GetProbedAt().AsTime().Equal(probedAt) {
		t.Fatalf("probed at = %v, want %v", capabilities.GetProbedAt().AsTime(), probedAt)
	}
	if len(capabilities.GetProbes()) != 2 || capabilities.GetProbes()[0].GetOutcome() != string(llms.ProbeSupported) ||
		capabilities.GetProbes()[1].GetOutcome() != string(llms.ProbeInconclusive) {
		t.Fatalf("probes = %#v", capabilities.GetProbes())
	}
}

// The default model is readable, settable, and clearable through the service,
// and a rejected reference leaves the stored one untouched.
func TestDefaultModelServiceRoundTrip(t *testing.T) {
	ctx := context.Background()
	handler := newLLMTestHandler(t)
	spec := &agentcomposev2.LLMProviderSpec{
		Id: "gateway", BaseUrl: "https://gateway.example/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret"),
	}
	if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	empty, err := handler.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("GetDefaultModel: %v", err)
	}
	if empty.Msg.GetModel() != nil {
		t.Fatalf("default before any write = %#v, want absent", empty.Msg.GetModel())
	}

	set, err := handler.SetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.SetDefaultModelRequest{
		Model: &agentcomposev2.LLMModelReference{ProviderId: "gateway", ModelId: "gpt-4o"},
	}))
	if err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	if set.Msg.GetModel().GetProviderId() != "gateway" || set.Msg.GetModel().GetModelId() != "gpt-4o" {
		t.Fatalf("set default = %#v", set.Msg.GetModel())
	}
	stored, err := handler.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("GetDefaultModel after set: %v", err)
	}
	if stored.Msg.GetModel().GetProviderId() != "gateway" || stored.Msg.GetModel().GetModelId() != "gpt-4o" {
		t.Fatalf("stored default = %#v", stored.Msg.GetModel())
	}

	// A reference the daemon cannot serve is rejected and does not overwrite the
	// working default.
	_, err = handler.SetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.SetDefaultModelRequest{
		Model: &agentcomposev2.LLMModelReference{ProviderId: "absent", ModelId: "gpt-4o"},
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown provider error = %v, want a not-found code", err)
	}
	if _, err := handler.SetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.SetDefaultModelRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing model error = %v, want an invalid-argument code", err)
	}
	stored, err = handler.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("GetDefaultModel after a rejected set: %v", err)
	}
	if stored.Msg.GetModel().GetModelId() != "gpt-4o" {
		t.Fatalf("default after a rejected set = %#v, want the working default", stored.Msg.GetModel())
	}

	if _, err := handler.ClearDefaultModel(ctx, connect.NewRequest(&agentcomposev2.ClearDefaultModelRequest{})); err != nil {
		t.Fatalf("ClearDefaultModel: %v", err)
	}
	cleared, err := handler.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("GetDefaultModel after clear: %v", err)
	}
	if cleared.Msg.GetModel() != nil {
		t.Fatalf("default after clearing = %#v, want absent", cleared.Msg.GetModel())
	}
}
