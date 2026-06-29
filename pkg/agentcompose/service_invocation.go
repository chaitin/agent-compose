package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) InvokeService(ctx context.Context, req *connect.Request[agentcomposev2.InvokeServiceRequest]) (*connect.Response[agentcomposev2.InvokeServiceResponse], error) {
	run, _, err := s.invokeProjectService(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.InvokeServiceResponse{Run: runDetailResponse(run)}), nil
}

func (s *Service) InvokeServiceStream(ctx context.Context, req *connect.Request[agentcomposev2.InvokeServiceRequest], stream *connect.ServerStream[agentcomposev2.RunStreamResponse]) error {
	run, execErr, err := s.invokeProjectService(ctx, req.Msg)
	if err != nil {
		return err
	}
	if execErr != nil {
		return nil
	}
	return stream.Send(&agentcomposev2.RunStreamResponse{
		EventType: agentcomposev2.RunStreamEventType_RUN_STREAM_EVENT_TYPE_COMPLETED,
		Run:       runSummaryResponse(run),
		RunId:     run.RunID,
		CreatedAt: formatProjectTime(run.UpdatedAt),
	})
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
		Driver:             firstNonEmpty(service.GetRuntime(), spec.GetRuntime().GetDriver()),
		ImageRef:           spec.GetRuntime().GetImage(),
		ResultJSON:         "{}",
	}
	coordinator := NewRunCoordinator(s.configDB)
	run, err = s.configDB.CreateProjectRun(ctx, run)
	if err != nil {
		if existing, loadErr := s.configDB.GetProjectRun(ctx, runID); loadErr == nil {
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
	result, execErr := s.executor.ExecuteLoaderCommand(ctx, sessionResult.Session, serviceLoaderCommandRequest(service, inputJSON, runtimeContextJSON))
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
			return project, spec, service, nil
		}
	}
	return ProjectRecord{}, nil, nil, fmt.Errorf("project revision %s/%d missing service %s", project.ID, project.CurrentRevision, serviceName)
}

func mergeServiceRequestEnv(service *agentcomposev2.ServiceSpec, requestEnv []*agentcomposev2.EnvVarSpec) []*agentcomposev2.EnvVarSpec {
	items := append([]*agentcomposev2.EnvVarSpec(nil), service.GetEnv()...)
	items = append(items, requestEnv...)
	return items
}

func serviceLoaderCommandRequest(service *agentcomposev2.ServiceSpec, inputJSON, runtimeContextJSON string) LoaderCommandRequest {
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
	raw, _ := json.Marshal(request)
	return LoaderCommandRequest{
		Mode:      "shell",
		Script:    "agent-compose-runtime service --request-json " + shellQuote(string(raw)),
		TimeoutMs: int64(service.GetTimeoutMs()),
	}
}

func serviceRunTransition(run ProjectRunRecord, result LoaderCommandResult, execErr error) ProjectRunTransitionRequest {
	payload, hasPayload := servicePayloadFromStdout(result.Stdout)
	transition := ProjectRunTransitionRequest{
		RunID:        run.RunID,
		SessionID:    result.SessionID,
		ExitCode:     result.ExitCode,
		Output:       result.Output,
		OutputJSON:   serviceOutputJSON(payload, hasPayload),
		ResultJSON:   serviceResultJSON(result, payload, hasPayload),
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

func serviceResultJSON(result LoaderCommandResult, payload RuntimeServiceResult, hasPayload bool) string {
	response := map[string]any{
		"success":   result.Success,
		"exitCode":  result.ExitCode,
		"cellId":    result.CellID,
		"artifacts": result.Artifacts,
	}
	if hasPayload {
		response["serviceName"] = payload.ServiceName
		response["outputJson"] = payload.OutputJSON
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

func findServicePayload(raw string, payload *RuntimeServiceResult) bool {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "__SERVICE_RESULT__")
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), payload) == nil && runtimeServiceProtocolSupported(payload.ProtocolVersion) {
			return true
		}
	}
	return false
}

func runtimeServiceProtocolSupported(version string) bool {
	version = strings.TrimSpace(version)
	return version == "" || version == "service.v1"
}
