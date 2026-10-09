package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// EngineCapabilitiesV2Handler serves the engine capability snapshot over
// Connect. It is transport-only: the snapshot was built and validated once at
// startup, so this handler never probes a driver.
type EngineCapabilitiesV2Handler struct {
	snapshot capmatrix.Snapshot
}

// NewEngineCapabilitiesV2Handler injects the startup capability snapshot.
func NewEngineCapabilitiesV2Handler(snapshot capmatrix.Snapshot) *EngineCapabilitiesV2Handler {
	return &EngineCapabilitiesV2Handler{snapshot: snapshot}
}

// GetCapabilities returns the frozen matrix. The snapshot is re-validated
// defensively so an invalid enforcement claim can never reach a consumer; the
// composition root already rejects such a snapshot at startup.
func (h *EngineCapabilitiesV2Handler) GetCapabilities(_ context.Context, _ *connect.Request[agentcomposev2.GetEngineCapabilitiesRequest]) (*connect.Response[agentcomposev2.GetEngineCapabilitiesResponse], error) {
	if err := h.snapshot.Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("engine capability snapshot is invalid: %w", err))
	}
	response := &agentcomposev2.GetEngineCapabilitiesResponse{
		CompiledDrivers:     h.snapshot.CompiledDrivers(),
		CompiledDriversNote: capmatrix.CompiledDriversSemantics,
		CapturedAt:          timestamppb.New(h.snapshot.CapturedAt()),
	}
	for _, declaration := range h.snapshot.Drivers() {
		driver := &agentcomposev2.EngineDriverCapabilities{Driver: declaration.Driver}
		for _, capability := range declaration.Capabilities {
			driver.Capabilities = append(driver.Capabilities, engineCapabilityV2(capability))
		}
		response.Drivers = append(response.Drivers, driver)
	}
	for _, declaration := range h.snapshot.Providers() {
		response.Providers = append(response.Providers, engineProviderCapabilitiesV2(declaration))
	}
	return connect.NewResponse(response), nil
}

func engineCapabilityV2(capability capmatrix.Capability) *agentcomposev2.EngineCapability {
	return &agentcomposev2.EngineCapability{
		Dimension:       string(capability.Dimension),
		Enforced:        capability.Enforced,
		Mechanism:       capability.Mechanism,
		Preconditions:   append([]string(nil), capability.Preconditions...),
		Observed:        capability.Observed,
		DefaultBehavior: capability.DefaultBehavior,
	}
}

func engineProviderCapabilitiesV2(declaration capmatrix.ProviderCapabilities) *agentcomposev2.EngineProviderCapabilities {
	entry := &agentcomposev2.EngineProviderCapabilities{
		Provider:           declaration.Provider,
		PreferredProtocols: append([]string(nil), declaration.PreferredProtocols...),
	}
	for _, feature := range declaration.Features {
		entry.Features = append(entry.Features, &agentcomposev2.EngineProviderFeature{
			Feature:   string(feature.Feature),
			Supported: feature.Supported,
		})
	}
	return entry
}
