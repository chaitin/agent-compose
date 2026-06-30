package agentcompose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

const serviceInvocationRequestHashKey = "agentCompose.requestHash"

func (s *Service) InvokeService(ctx context.Context, req *connect.Request[agentcomposev2.InvokeServiceRequest]) (*connect.Response[agentcomposev2.InvokeServiceResponse], error) {
	run, _, err := s.invokeProjectService(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.InvokeServiceResponse{Run: runDetailResponse(run)}), nil
}

func (s *Service) InvokeServiceStream(ctx context.Context, req *connect.Request[agentcomposev2.InvokeServiceRequest], stream *connect.ServerStream[agentcomposev2.RunStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	run, execErr, err := s.invokeProjectService(ctx, req.Msg)
	if err != nil {
		return err
	}
	if execErr != nil || run.Status == ProjectRunStatusFailed {
		if sendErr := stream.Send(runStreamResponseFromRun(run, agentcomposev2.RunStreamEventType_RUN_STREAM_EVENT_TYPE_STATUS)); sendErr != nil {
			return connect.NewError(connect.CodeUnknown, sendErr)
		}
	}
	if sendErr := stream.Send(runStreamResponseFromRun(run, agentcomposev2.RunStreamEventType_RUN_STREAM_EVENT_TYPE_COMPLETED)); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	return nil
}

func (s *Service) invokeProjectService(ctx context.Context, msg *agentcomposev2.InvokeServiceRequest) (ProjectRunRecord, error, error) {
	if s == nil || s.configDB == nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	if s.executor == nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("executor is required"))
	}
	projectID := strings.TrimSpace(msg.GetProjectId())
	serviceName := strings.TrimSpace(msg.GetServiceName())
	if projectID == "" || serviceName == "" {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project id and service name are required"))
	}
	inputJSON := strings.TrimSpace(msg.GetInputJson())
	if inputJSON == "" {
		inputJSON = "{}"
	}
	if !json.Valid([]byte(inputJSON)) {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("input_json must be valid JSON"))
	}
	project, spec, service, err := s.resolveProjectService(ctx, projectID, serviceName)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateJSONSchemaDocument(inputJSON, service.GetInputSchemaJson(), "input"); err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	clientRequestID := strings.TrimSpace(msg.GetClientRequestId())
	if clientRequestID == "" {
		clientRequestID = uuid.NewString()
	}
	source := projectRunSourceFromProto(msg.GetSource())
	runID, err := StableProjectTargetRunID(project.ID, "service", service.GetName(), source, clientRequestID)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	runtimeContextJSON, err := encodeRuntimeContextJSON(msg.GetRuntimeContext())
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	requestHash, err := serviceInvocationRequestHash(serviceInvocationFingerprint{
		ProjectRevision:    project.CurrentRevision,
		ServiceName:        service.GetName(),
		Source:             source,
		ClientRequestID:    clientRequestID,
		InputJSON:          json.RawMessage(inputJSON),
		RuntimeContextJSON: json.RawMessage(runtimeContextJSON),
		RequestEnv:         msg.GetEnv(),
		SessionID:          strings.TrimSpace(msg.GetSessionId()),
		CleanupPolicy:      msg.GetCleanupPolicy(),
	})
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	runtimeContextJSON, err = serviceInvocationRuntimeContextJSON(runtimeContextJSON, requestHash)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	run := ProjectRunRecord{
		RunID:              runID,
		ProjectID:          project.ID,
		ProjectName:        project.Name,
		ProjectRevision:    project.CurrentRevision,
		TargetType:         "service",
		TargetName:         service.GetName(),
		Source:             source,
		SchedulerID:        msg.GetSchedulerId(),
		TriggerID:          msg.GetTriggerId(),
		ClientRequestID:    clientRequestID,
		Status:             ProjectRunStatusPending,
		InputJSON:          inputJSON,
		RuntimeContextJSON: runtimeContextJSON,
		Driver:             spec.GetRuntime().GetDriver(),
		ImageRef:           spec.GetRuntime().GetImage(),
		ResultJSON:         "{}",
	}
	coordinator := NewRunCoordinator(s.configDB)
	run, err = s.configDB.CreateProjectRun(ctx, run)
	if err != nil {
		if existing, loadErr := s.configDB.GetProjectRun(ctx, runID); loadErr == nil {
			if conflictErr := validateExistingServiceInvocationRequest(existing, requestHash); conflictErr != nil {
				return ProjectRunRecord{}, nil, conflictErr
			}
			return existing, nil, nil
		}
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	transitionCtx := context.WithoutCancel(ctx)
	prepared, err := s.prepareProjectRun(ctx, run, mergeServiceRequestEnv(service, msg.GetEnv()))
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{RunID: run.RunID, Error: fmt.Sprintf("workspace preparation failed: %v", err)})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	sessionResult, err := s.ensureProjectRunSession(ctx, run, prepared, msg.GetSessionId())
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{RunID: run.RunID, Error: fmt.Sprintf("session start failed: %v", err)})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sessionResult.Session.Summary.ID)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	commandRequest, err := serviceLoaderCommandRequest(service, inputJSON, runtimeContextJSON)
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("service request build failed: %v", err),
		})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
		return run, err, nil
	}
	result, execErr := s.executor.ExecuteLoaderCommand(ctx, sessionResult.Session, commandRequest)
	transition := serviceRunTransition(run, result, execErr)
	if execErr == nil && result.Success && service.GetOutputSchemaJson() != "" {
		if transition.OutputJSON == "" {
			execErr = fmt.Errorf("service output_json is required when output_schema_json is set")
		} else if validationErr := validateJSONSchemaDocument(transition.OutputJSON, service.GetOutputSchemaJson(), "output"); validationErr != nil {
			execErr = validationErr
		}
		if execErr != nil {
			transition.ExitCode = firstNonZeroInt(transition.ExitCode, 1)
			transition.Error = fmt.Sprintf("service output validation failed: %v", execErr)
		}
	}
	if execErr != nil || !result.Success {
		run, err = coordinator.MarkFailed(transitionCtx, transition)
	} else {
		run, err = coordinator.MarkSucceeded(transitionCtx, transition)
	}
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
	return run, execErr, nil
}

func (s *Service) resolveProjectService(ctx context.Context, projectID, serviceName string) (ProjectRecord, *agentcomposev2.ProjectSpec, *agentcomposev2.ServiceSpec, error) {
	project, err := s.configDB.GetProject(ctx, projectID)
	if err != nil {
		return ProjectRecord{}, nil, nil, fmt.Errorf("resolve project %s: %w", projectID, err)
	}
	revision, err := s.configDB.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
	if err != nil {
		return ProjectRecord{}, nil, nil, fmt.Errorf("resolve project revision %s/%d: %w", project.ID, project.CurrentRevision, err)
	}
	spec, err := decodeProjectRevisionSpec(revision.SpecJSON)
	if err != nil {
		return ProjectRecord{}, nil, nil, err
	}
	for _, service := range spec.GetServices() {
		if service.GetName() == serviceName {
			service = proto.Clone(service).(*agentcomposev2.ServiceSpec)
			if err := s.hydrateProjectServiceSchemaRefs(project, revision, service); err != nil {
				return ProjectRecord{}, nil, nil, err
			}
			return project, spec, service, nil
		}
	}
	return ProjectRecord{}, nil, nil, fmt.Errorf("project revision %s/%d missing service %s", project.ID, project.CurrentRevision, serviceName)
}

func (s *Service) hydrateProjectServiceSchemaRefs(project ProjectRecord, revision ProjectRevisionRecord, service *agentcomposev2.ServiceSpec) error {
	if service == nil {
		return nil
	}
	load := func(ref string) (string, error) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return "", nil
		}
		root := s.projectRevisionBundleDir(project.ID, revision.Revision)
		if strings.TrimSpace(revision.BundleHash) == "" {
			root = filepath.Dir(project.SourcePath)
		}
		path := filepath.Join(root, filepath.FromSlash(ref))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read service schema %s: %w", ref, err)
		}
		return string(data), nil
	}
	if service.GetInputSchemaJson() == "" && service.GetInputSchemaRef() != "" {
		value, err := load(service.GetInputSchemaRef())
		if err != nil {
			return err
		}
		service.InputSchemaJson = value
	}
	if service.GetOutputSchemaJson() == "" && service.GetOutputSchemaRef() != "" {
		value, err := load(service.GetOutputSchemaRef())
		if err != nil {
			return err
		}
		service.OutputSchemaJson = value
	}
	if service.GetErrorSchemaJson() == "" && service.GetErrorSchemaRef() != "" {
		value, err := load(service.GetErrorSchemaRef())
		if err != nil {
			return err
		}
		service.ErrorSchemaJson = value
	}
	return nil
}

func mergeServiceRequestEnv(service *agentcomposev2.ServiceSpec, requestEnv []*agentcomposev2.EnvVarSpec) []*agentcomposev2.EnvVarSpec {
	items := append([]*agentcomposev2.EnvVarSpec(nil), service.GetEnv()...)
	items = append(items, requestEnv...)
	return items
}

type serviceInvocationFingerprint struct {
	ProjectRevision    int64                                  `json:"projectRevision"`
	ServiceName        string                                 `json:"serviceName"`
	Source             string                                 `json:"source"`
	ClientRequestID    string                                 `json:"clientRequestId"`
	InputJSON          json.RawMessage                        `json:"inputJson"`
	RuntimeContextJSON json.RawMessage                        `json:"runtimeContextJson"`
	RequestEnv         []*agentcomposev2.EnvVarSpec           `json:"-"`
	CanonicalEnv       []serviceInvocationFingerprintEnvVar   `json:"requestEnv,omitempty"`
	SessionID          string                                 `json:"sessionId,omitempty"`
	CleanupPolicy      agentcomposev2.RunSessionCleanupPolicy `json:"cleanupPolicy"`
}

type serviceInvocationFingerprintEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

func serviceInvocationRequestHash(value serviceInvocationFingerprint) (string, error) {
	inputJSON, err := canonicalJSON(value.InputJSON)
	if err != nil {
		return "", fmt.Errorf("canonicalize service input: %w", err)
	}
	runtimeContextJSON, err := canonicalJSON(value.RuntimeContextJSON)
	if err != nil {
		return "", fmt.Errorf("canonicalize service runtime context: %w", err)
	}
	value.InputJSON = inputJSON
	value.RuntimeContextJSON = runtimeContextJSON
	value.CanonicalEnv = serviceInvocationFingerprintEnv(value.RequestEnv)
	value.RequestEnv = nil
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal service invocation fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func serviceInvocationFingerprintEnv(items []*agentcomposev2.EnvVarSpec) []serviceInvocationFingerprintEnvVar {
	normalized := sessionEnvItemsFromV2(items)
	if len(normalized) == 0 {
		return nil
	}
	result := make([]serviceInvocationFingerprintEnvVar, 0, len(normalized))
	for _, item := range normalized {
		result = append(result, serviceInvocationFingerprintEnvVar(item))
	}
	return result
}

func serviceInvocationRuntimeContextJSON(raw, requestHash string) (string, error) {
	var context agentcomposev2.RuntimeContext
	raw = strings.TrimSpace(raw)
	if raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &context); err != nil {
			return "", fmt.Errorf("decode runtime context: %w", err)
		}
	}
	if context.Metadata == nil {
		context.Metadata = map[string]string{}
	}
	context.Metadata[serviceInvocationRequestHashKey] = requestHash
	encoded, err := json.Marshal(&context)
	if err != nil {
		return "", fmt.Errorf("marshal runtime context: %w", err)
	}
	return string(encoded), nil
}

func validateExistingServiceInvocationRequest(existing ProjectRunRecord, requestHash string) error {
	existingHash, ok := serviceInvocationRequestHashFromRuntimeContext(existing.RuntimeContextJSON)
	if ok && existingHash == requestHash {
		return nil
	}
	if ok {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("service invocation already exists for client_request_id %q with different request fingerprint", existing.ClientRequestID))
	}
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("service invocation already exists for client_request_id %q without a request fingerprint", existing.ClientRequestID))
}

func serviceInvocationRequestHashFromRuntimeContext(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return "", false
	}
	var context agentcomposev2.RuntimeContext
	if err := json.Unmarshal([]byte(raw), &context); err != nil {
		return "", false
	}
	value := strings.TrimSpace(context.GetMetadata()[serviceInvocationRequestHashKey])
	return value, value != ""
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func serviceLoaderCommandRequest(service *agentcomposev2.ServiceSpec, inputJSON, runtimeContextJSON string) (LoaderCommandRequest, error) {
	request := map[string]any{
		"protocolVersion": "service.v1",
		"serviceName":     service.GetName(),
		"entry":           service.GetEntry(),
		"inputJson":       inputJSON,
		"inputSchema":     service.GetInputSchemaJson(),
		"contextJson":     runtimeContextJSON,
		"outputSchema":    service.GetOutputSchemaJson(),
		"artifactDirName": "service",
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("marshal service request: %w", err)
	}
	return LoaderCommandRequest{
		Mode:      "shell",
		Script:    "agent-compose-runtime service --request-json " + shellQuote(string(raw)),
		TimeoutMs: int64(service.GetTimeoutMs()),
	}, nil
}

func serviceRunTransition(run ProjectRunRecord, result LoaderCommandResult, execErr error) ProjectRunTransitionRequest {
	payload, hasPayload := servicePayloadFromResultFile(result)
	if !hasPayload {
		payload, hasPayload = servicePayloadFromStdout(result.Stdout)
	}
	transition := ProjectRunTransitionRequest{
		RunID:        run.RunID,
		SessionID:    result.SessionID,
		ExitCode:     result.ExitCode,
		Output:       result.Output,
		OutputJSON:   serviceOutputJSON(payload, hasPayload),
		ResultJSON:   serviceResultJSON(run, result, payload, hasPayload),
		LogsPath:     result.Artifacts["output"],
		ArtifactsDir: result.Artifacts["cellDir"],
	}
	if execErr != nil {
		transition.ExitCode = firstNonZeroInt(transition.ExitCode, 1)
		transition.Error = fmt.Sprintf("service execution failed: %v", execErr)
	} else if !result.Success {
		transition.ExitCode = firstNonZeroInt(transition.ExitCode, 1)
		transition.Error = firstNonEmpty(strings.TrimSpace(result.Stderr), fmt.Sprintf("service execution failed with exit code %d", transition.ExitCode))
	}
	return transition
}

func serviceOutputJSON(payload RuntimeServiceResult, ok bool) string {
	if ok && payload.OutputJSON != "" {
		return payload.OutputJSON
	}
	return ""
}

func serviceResultJSON(run ProjectRunRecord, result LoaderCommandResult, payload RuntimeServiceResult, hasPayload bool) string {
	response := map[string]any{
		"runId":      run.RunID,
		"targetType": "service",
		"targetName": run.TargetName,
		"status":     projectRunStatusFromSuccess(result.Success),
		"success":    result.Success,
		"exitCode":   result.ExitCode,
		"cellId":     result.CellID,
		"outputJson": serviceOutputJSON(payload, hasPayload),
		"artifacts":  result.Artifacts,
		"logsPath":   result.Artifacts["output"],
	}
	if hasPayload {
		response["serviceName"] = payload.ServiceName
		response["runtimeSuccess"] = payload.Success
		response["runtimeArtifacts"] = payload.Artifacts
		response["metrics"] = payload.Metrics
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func projectRunStatusFromSuccess(success bool) string {
	if success {
		return ProjectRunStatusSucceeded
	}
	return ProjectRunStatusFailed
}

type RuntimeServiceResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	ServiceName     string            `json:"serviceName"`
	OutputJSON      string            `json:"outputJson"`
	Success         bool              `json:"success"`
	Artifacts       map[string]string `json:"artifacts"`
	Metrics         map[string]string `json:"metrics"`
}

func servicePayloadFromStdout(stdout string) (RuntimeServiceResult, bool) {
	var payload RuntimeServiceResult
	return payload, findServicePayload(stdout, &payload)
}

func servicePayloadFromResultFile(result LoaderCommandResult) (RuntimeServiceResult, bool) {
	for _, path := range serviceResultFileCandidates(result) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if payload, ok := servicePayloadFromJSON(data); ok {
			return payload, true
		}
	}
	return RuntimeServiceResult{}, false
}

func serviceResultFileCandidates(result LoaderCommandResult) []string {
	if len(result.Artifacts) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var candidates []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		candidates = append(candidates, path)
	}
	if cellDir := strings.TrimSpace(result.Artifacts["cellDir"]); cellDir != "" {
		add(filepath.Join(cellDir, "service-result.json"))
	}
	for _, key := range []string{"serviceResult", "service-result", "service_result", "resultPath", "result_path", "result"} {
		path := strings.TrimSpace(result.Artifacts[key])
		if filepath.Base(path) == "service-result.json" {
			add(path)
		}
	}
	return candidates
}

func servicePayloadFromJSON(data []byte) (RuntimeServiceResult, bool) {
	var payload RuntimeServiceResult
	if json.Unmarshal(data, &payload) != nil {
		return RuntimeServiceResult{}, false
	}
	if strings.TrimSpace(payload.ProtocolVersion) == "" {
		payload.ProtocolVersion = "service.v1"
	}
	if !runtimeServiceProtocolSupported(payload.ProtocolVersion) || strings.TrimSpace(payload.ServiceName) == "" {
		return RuntimeServiceResult{}, false
	}
	return payload, true
}

func findServicePayload(raw string, payload *RuntimeServiceResult) bool {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "__SERVICE_RESULT__") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "__SERVICE_RESULT__"))
		if strings.HasPrefix(line, "{") {
			candidate, ok := servicePayloadFromJSON([]byte(line))
			if !ok {
				continue
			}
			*payload = candidate
			return true
		}
	}
	return false
}

func runtimeServiceProtocolSupported(version string) bool {
	version = strings.TrimSpace(version)
	return version == "service.v1"
}
