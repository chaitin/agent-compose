package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// EngineCapabilitiesV2Handler serves the engine capability snapshot over
// Connect. It is transport-only: the snapshot was built and validated once at
// startup, so this handler never probes a driver.
type EngineCapabilitiesV2Handler struct {
	snapshot capmatrix.Snapshot
	config   *appconfig.Config
}

// NewEngineCapabilitiesV2Handler injects the startup capability snapshot and
// the daemon configuration that supplies the input limits.
func NewEngineCapabilitiesV2Handler(snapshot capmatrix.Snapshot, config *appconfig.Config) *EngineCapabilitiesV2Handler {
	return &EngineCapabilitiesV2Handler{snapshot: snapshot, config: config}
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
		Observations:        engineObservedCapabilitiesV2(h.snapshot.Observations()),
		InputLimits:         engineInputLimitsV2(h.config),
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
		State:           string(capability.State),
		Source:          string(capability.Source),
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

func engineInputLimitsV2(config *appconfig.Config) *agentcomposev2.EngineInputLimits {
	limits := config.AgentInputLimits()
	out := &agentcomposev2.EngineInputLimits{
		MaxFileBytes:       limits.MaxFileBytes,
		MaxTurnBytes:       limits.MaxTurnBytes,
		MaxFiles:           limits.MaxFiles,
		MaxUploadFileBytes: limits.MaxUploadFileBytes,
		MaxUploadTurnBytes: limits.MaxUploadTurnBytes,
	}
	if limits.MaxUploadFileBytes > 0 {
		out.MaxUploadChunkBytes = MaxRunInputUploadChunkBytes
	}
	return out
}
