package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	controlauth "agent-compose/pkg/auth"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type procedurePolicy struct {
	Access      controlauth.Access
	Action      string
	SelfAudited bool
}

type SecurityInterceptor struct{ service *controlauth.Service }

func NewSecurityInterceptor(service *controlauth.Service) *SecurityInterceptor {
	return &SecurityInterceptor{service: service}
}

func (i *SecurityInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		policy, identity, err := i.authorize(ctx, request.Spec().Procedure)
		if err != nil {
			return nil, err
		}
		if policy.Access == controlauth.AccessRead || policy.SelfAudited {
			return next(ctx, request)
		}
		audit, err := i.service.BeginAudit(ctx, identity, request.Header().Get("X-Request-ID"), policy.Action, resourceFromMessage(request.Any()), summarizeMessage(request.Any()))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("begin operation audit: %w", err))
		}
		response, callErr := next(ctx, request)
		status := controlauth.AuditStatusSucceeded
		changes := "{}"
		resource := resourceFromMessage(request.Any())
		code, message := "", ""
		if response != nil {
			changes = summarizeMessage(response.Any())
			if resolved := resourceFromMessage(response.Any()); resolved.ID != "" || resolved.ProjectID != "" {
				resource = resolved
			}
		}
		if callErr != nil {
			status = controlauth.AuditStatusFailed
			code = connect.CodeOf(callErr).String()
			message = callErr.Error()
		}
		if finishErr := i.service.FinishAudit(context.WithoutCancel(ctx), audit, status, resource, changes, code, message); finishErr != nil {
			if callErr != nil {
				return nil, errors.Join(callErr, finishErr)
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("finish operation audit: %w", finishErr))
		}
		return response, callErr
	}
}

func (i *SecurityInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *SecurityInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		policy, identity, err := i.authorize(ctx, conn.Spec().Procedure)
		if err != nil {
			return err
		}
		if policy.Access == controlauth.AccessRead {
			return next(ctx, conn)
		}
		audit, err := i.service.BeginAudit(ctx, identity, conn.RequestHeader().Get("X-Request-ID"), policy.Action, controlauth.Resource{}, "{}")
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("begin operation audit: %w", err))
		}
		observed := &auditStreamingConn{StreamingHandlerConn: conn}
		callErr := next(ctx, observed)
		status := controlauth.AuditStatusSucceeded
		code, message := "", ""
		if callErr != nil && !errors.Is(callErr, io.EOF) {
			status = controlauth.AuditStatusFailed
			code = connect.CodeOf(callErr).String()
			message = callErr.Error()
		}
		audit.ParamsJSON = observed.requestSummary()
		resource := observed.resource()
		if finishErr := i.service.FinishAudit(context.WithoutCancel(ctx), audit, status, resource, observed.responseSummary(), code, message); finishErr != nil {
			if callErr != nil {
				return errors.Join(callErr, finishErr)
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("finish operation audit: %w", finishErr))
		}
		return callErr
	}
}

func (i *SecurityInterceptor) authorize(ctx context.Context, procedure string) (procedurePolicy, controlauth.Identity, error) {
	policy, ok := procedurePolicies[procedure]
	if !ok {
		return procedurePolicy{}, controlauth.Identity{}, connect.NewError(connect.CodeInternal, fmt.Errorf("%w: %s", controlauth.ErrPolicyMissing, procedure))
	}
	identity, ok := controlauth.IdentityFromContext(ctx)
	if !ok {
		if i.service.Initialized() {
			return procedurePolicy{}, controlauth.Identity{}, connect.NewError(connect.CodeUnauthenticated, controlauth.ErrInvalidToken)
		}
		identity = controlauth.Identity{TokenName: "anonymous-admin", Role: controlauth.RoleAdmin, Origin: controlauth.OriginAnonymous}
	}
	if controlauth.Allowed(identity.Role, policy.Access) {
		return policy, identity, nil
	}
	if policy.Access == controlauth.AccessOperation {
		_ = i.service.DenyAudit(context.WithoutCancel(ctx), identity, "", policy.Action)
	}
	return procedurePolicy{}, controlauth.Identity{}, connect.NewError(connect.CodePermissionDenied, controlauth.ErrPermissionDenied)
}

var procedurePolicies = buildProcedurePolicies()

func buildProcedurePolicies() map[string]procedurePolicy {
	read := []string{
		agentcomposev2connect.ProjectServiceValidateProjectProcedure,
		agentcomposev2connect.ProjectServiceGetProjectProcedure,
		agentcomposev2connect.ProjectServiceListProjectsProcedure,
		agentcomposev2connect.ProjectServiceWatchProjectProcedure,
		agentcomposev2connect.ProjectServiceGetSchedulerProcedure,
		agentcomposev2connect.ProjectServiceListSchedulersProcedure,
		agentcomposev2connect.ProjectServiceListSchedulerEventsProcedure,
		agentcomposev2connect.ProjectServiceGetSchedulerRunProcedure,
		agentcomposev2connect.ProjectServiceListSchedulerRunsProcedure,
		agentcomposev2connect.RunServiceGetRunProcedure,
		agentcomposev2connect.RunServiceListRunsProcedure,
		agentcomposev2connect.RunServiceFollowRunLogsProcedure,
		agentcomposev2connect.RunServiceListRunEventsProcedure,
		agentcomposev2connect.RunServiceListSandboxRunEventsProcedure,
		agentcomposev2connect.ImageServiceListImagesProcedure,
		agentcomposev2connect.ImageServiceInspectImageProcedure,
		agentcomposev2connect.CacheServiceListCachesProcedure,
		agentcomposev2connect.CacheServiceInspectCacheProcedure,
		agentcomposev2connect.VolumeServiceListVolumesProcedure,
		agentcomposev2connect.VolumeServiceInspectVolumeProcedure,
		agentcomposev2connect.SandboxServiceGetSandboxStatsProcedure,
		agentcomposev2connect.SandboxServiceGetSandboxProcedure,
		agentcomposev2connect.SandboxServiceListSandboxesProcedure,
		agentcomposev2connect.SandboxServiceListSandboxHistoryProcedure,
		agentcomposev2connect.SandboxServiceWatchSandboxProcedure,
		agentcomposev2connect.DashboardServiceGetDashboardOverviewProcedure,
		agentcomposev2connect.DashboardServiceWatchDashboardOverviewProcedure,
		agentcomposev2connect.SettingsServiceGetGlobalEnvProcedure,
		agentcomposev2connect.SettingsServiceGetCapabilityGatewayConfigProcedure,
		agentcomposev2connect.SettingsServiceListWorkspacePresetsProcedure,
		agentcomposev2connect.CapabilityServiceGetCapabilityStatusProcedure,
		agentcomposev2connect.CapabilityServiceListCapabilitySetsProcedure,
		agentcomposev2connect.CapabilityServiceGetCapabilityCatalogProcedure,
		agentcomposev2connect.ResourceServiceResolveIDProcedure,
		agentcomposev2connect.AuthServiceWhoAmIProcedure,
		agentcomposev2connect.AuthServiceListRolesProcedure,
		agentcomposev2connect.AuthServiceListTokensProcedure,
		agentcomposev2connect.AuthServiceListOperationAuditsProcedure,
	}
	operations := map[string]string{
		agentcomposev2connect.ProjectServiceApplyProjectProcedure:                   "project.apply",
		agentcomposev2connect.ProjectServiceRemoveProjectProcedure:                  "project.remove",
		agentcomposev2connect.ProjectServiceRunSchedulerProcedure:                   "scheduler.run",
		agentcomposev2connect.ProjectServiceStartSchedulerRunProcedure:              "scheduler.start",
		agentcomposev2connect.ProjectServiceStopSchedulerRunProcedure:               "scheduler.stop",
		agentcomposev2connect.ProjectServiceSetSchedulerEnabledProcedure:            "scheduler.enable",
		agentcomposev2connect.ProjectServiceSetSchedulerTriggerEnabledProcedure:     "scheduler.trigger.enable",
		agentcomposev2connect.RunServiceRunAgentProcedure:                           "run.agent",
		agentcomposev2connect.RunServiceStartRunProcedure:                           "run.start",
		agentcomposev2connect.RunServiceRunAgentStreamProcedure:                     "run.agent.stream",
		agentcomposev2connect.RunServiceRunAttachProcedure:                          "run.attach",
		agentcomposev2connect.RunServiceStopRunProcedure:                            "run.stop",
		agentcomposev2connect.ExecServiceExecProcedure:                              "exec.execute",
		agentcomposev2connect.ExecServiceExecStreamProcedure:                        "exec.stream",
		agentcomposev2connect.ExecServiceExecAttachProcedure:                        "exec.attach",
		agentcomposev2connect.ImageServicePullImageProcedure:                        "image.pull",
		agentcomposev2connect.ImageServiceRemoveImageProcedure:                      "image.remove",
		agentcomposev2connect.ImageServiceBuildImageProcedure:                       "image.build",
		agentcomposev2connect.CacheServicePruneCachesProcedure:                      "cache.prune",
		agentcomposev2connect.CacheServiceRemoveCacheProcedure:                      "cache.remove",
		agentcomposev2connect.VolumeServiceCreateVolumeProcedure:                    "volume.create",
		agentcomposev2connect.VolumeServiceRemoveVolumeProcedure:                    "volume.remove",
		agentcomposev2connect.VolumeServicePruneVolumesProcedure:                    "volume.prune",
		agentcomposev2connect.SandboxServiceRemoveSandboxProcedure:                  "sandbox.remove",
		agentcomposev2connect.SandboxServicePruneSandboxesProcedure:                 "sandbox.prune",
		agentcomposev2connect.SandboxServiceStopSandboxProcedure:                    "sandbox.stop",
		agentcomposev2connect.SandboxServiceResumeSandboxProcedure:                  "sandbox.resume",
		agentcomposev2connect.SettingsServiceUpdateGlobalEnvProcedure:               "settings.global-env.update",
		agentcomposev2connect.SettingsServiceUpdateCapabilityGatewayConfigProcedure: "settings.capability-gateway.update",
		agentcomposev2connect.SettingsServiceCreateWorkspacePresetProcedure:         "workspace-preset.create",
		agentcomposev2connect.SettingsServiceUpdateWorkspacePresetProcedure:         "workspace-preset.update",
		agentcomposev2connect.SettingsServiceDeleteWorkspacePresetProcedure:         "workspace-preset.delete",
		agentcomposev2connect.LLMServiceGenerateProcedure:                           "llm.generate",
		agentcomposev2connect.AuthServiceCreateTokenProcedure:                       "auth.token.create",
		agentcomposev2connect.AuthServiceRevokeTokenProcedure:                       "auth.token.revoke",
	}
	result := make(map[string]procedurePolicy, len(read)+len(operations))
	for _, procedure := range read {
		result[procedure] = procedurePolicy{Access: controlauth.AccessRead}
	}
	for procedure, action := range operations {
		result[procedure] = procedurePolicy{Access: controlauth.AccessOperation, Action: action}
	}
	result[agentcomposev2connect.AuthServiceCreateTokenProcedure] = procedurePolicy{Access: controlauth.AccessOperation, Action: "auth.token.create", SelfAudited: true}
	result[agentcomposev2connect.AuthServiceRevokeTokenProcedure] = procedurePolicy{Access: controlauth.AccessOperation, Action: "auth.token.revoke", SelfAudited: true}
	return result
}

type auditStreamingConn struct {
	connect.StreamingHandlerConn
	mu          sync.Mutex
	requests    []string
	responses   []string
	resourceRef controlauth.Resource
}

func (c *auditStreamingConn) Receive(message any) error {
	if err := c.StreamingHandlerConn.Receive(message); err != nil {
		return err
	}
	c.mu.Lock()
	c.requests = appendLimited(c.requests, summarizeMessage(message))
	if resource := resourceFromMessage(message); resource.ID != "" || resource.ProjectID != "" {
		c.resourceRef = resource
	}
	c.mu.Unlock()
	return nil
}

func (c *auditStreamingConn) Send(message any) error {
	c.mu.Lock()
	c.responses = appendLimited(c.responses, summarizeMessage(message))
	if resource := resourceFromMessage(message); resource.ID != "" || resource.ProjectID != "" {
		c.resourceRef = resource
	}
	c.mu.Unlock()
	return c.StreamingHandlerConn.Send(message)
}

func (c *auditStreamingConn) requestSummary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return marshalSummary(map[string]any{"messages": c.requests})
}

func (c *auditStreamingConn) responseSummary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return marshalSummary(map[string]any{"messages": c.responses})
}

func (c *auditStreamingConn) resource() controlauth.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resourceRef
}

func appendLimited(items []string, value string) []string {
	if len(items) >= 20 {
		return items
	}
	return append(items, value)
}

var auditFieldAllowlist = map[protoreflect.Name]bool{
	"project_id": true, "name": true, "agent_name": true, "scheduler_id": true, "trigger_id": true,
	"run_id": true, "sandbox_id": true, "image_ref": true, "cache_id": true, "driver": true,
	"cwd": true, "command": true, "args": true, "force": true, "remove_history": true,
	"stop_running_sandboxes": true, "cleanup_policy": true, "client_request_id": true,
	"description": true, "role": true, "older_than_seconds": true, "include_orphans": true,
	"query": true, "status": true, "target": true, "tags": true, "no_cache": true, "pull": true,
	"preset_id": true, "enabled": true, "dry_run": true, "created": true, "removed": true,
	"revoked": true, "already_revoked": true, "started": true, "stop_requested": true,
	"resource_type": true, "resource_id": true, "action": true, "exit_code": true,
}

func summarizeMessage(value any) string {
	message, ok := value.(interface{ ProtoReflect() protoreflect.Message })
	if !ok {
		return "{}"
	}
	return marshalSummary(summarizeReflectMessage(message.ProtoReflect(), 0))
}

func summarizeReflectMessage(message protoreflect.Message, depth int) map[string]any {
	if !message.IsValid() || depth > 4 {
		return map[string]any{}
	}
	result := make(map[string]any)
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		name := field.Name()
		if name == "token" || name == "password" || name == "value" || name == "env" || name == "headers" || name == "build_args" || name == "options" {
			return true
		}
		if name == "prompt" || name == "human_message" || name == "payload_json" {
			text := value.String()
			digest := sha256.Sum256([]byte(text))
			result[string(name)+"_length"] = len(text)
			result[string(name)+"_sha256"] = hex.EncodeToString(digest[:])
			return true
		}
		if !auditFieldAllowlist[name] && field.Kind() != protoreflect.MessageKind {
			return true
		}
		if field.IsList() {
			list := value.List()
			items := make([]any, 0, min(list.Len(), 20))
			for index := 0; index < list.Len() && index < 20; index++ {
				items = append(items, summarizeReflectValue(field, list.Get(index), depth+1))
			}
			result[string(name)] = items
			return true
		}
		if field.IsMap() {
			return true
		}
		if field.Kind() == protoreflect.MessageKind {
			nested := summarizeReflectMessage(value.Message(), depth+1)
			if len(nested) > 0 {
				result[string(name)] = nested
			}
			return true
		}
		result[string(name)] = summarizeReflectValue(field, value, depth+1)
		return true
	})
	return result
}

func summarizeReflectValue(field protoreflect.FieldDescriptor, value protoreflect.Value, depth int) any {
	switch field.Kind() {
	case protoreflect.StringKind:
		text := value.String()
		if len(text) > 4096 {
			return text[:4096] + "…"
		}
		return text
	case protoreflect.BoolKind:
		return value.Bool()
	case protoreflect.EnumKind:
		return string(field.Enum().Values().ByNumber(value.Enum()).Name())
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return value.Int()
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return value.Uint()
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return value.Float()
	case protoreflect.MessageKind:
		return summarizeReflectMessage(value.Message(), depth)
	default:
		return nil
	}
}

func marshalSummary(value any) string {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 16*1024 {
		return `{"truncated":true}`
	}
	return string(data)
}

func resourceFromMessage(value any) controlauth.Resource {
	modern, ok := value.(interface{ ProtoReflect() protoreflect.Message })
	if !ok {
		return controlauth.Resource{}
	}
	return findResource(modern.ProtoReflect(), 0)
}

func findResource(message protoreflect.Message, depth int) controlauth.Resource {
	if !message.IsValid() || depth > 4 {
		return controlauth.Resource{}
	}
	resource := controlauth.Resource{}
	fields := message.Descriptor().Fields()
	for _, candidate := range []struct {
		name, resourceType string
	}{{"project_id", "project"}, {"run_id", "run"}, {"sandbox_id", "sandbox"}, {"image_ref", "image"}, {"cache_id", "cache"}, {"preset_id", "workspace-preset"}} {
		field := fields.ByName(protoreflect.Name(candidate.name))
		if field == nil || !message.Has(field) || field.Kind() != protoreflect.StringKind {
			continue
		}
		value := strings.TrimSpace(message.Get(field).String())
		if value == "" {
			continue
		}
		if candidate.name == "project_id" {
			resource.ProjectID = value
		}
		if resource.ID == "" {
			resource.Type, resource.ID = candidate.resourceType, value
		}
	}
	if resource.ID != "" {
		return resource
	}
	var nested controlauth.Resource
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind || field.IsList() || field.IsMap() {
			return true
		}
		nested = findResource(value.Message(), depth+1)
		return nested.ID == ""
	})
	return nested
}
