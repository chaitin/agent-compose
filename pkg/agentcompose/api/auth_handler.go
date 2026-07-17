package api

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	controlauth "agent-compose/pkg/auth"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type AuthHandler struct{ service *controlauth.Service }

func NewAuthHandler(service *controlauth.Service) *AuthHandler { return &AuthHandler{service: service} }

func (h *AuthHandler) WhoAmI(ctx context.Context, _ *connect.Request[agentcomposev2.WhoAmIRequest]) (*connect.Response[agentcomposev2.WhoAmIResponse], error) {
	identity, ok := controlauth.IdentityFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, controlauth.ErrInvalidToken)
	}
	return connect.NewResponse(&agentcomposev2.WhoAmIResponse{
		Token:                     authTokenToProto(identity.TokenSummary()),
		Role:                      string(identity.Role),
		Origin:                    identity.Origin,
		AuthenticationInitialized: h.service.Initialized(),
	}), nil
}

func (h *AuthHandler) ListRoles(context.Context, *connect.Request[agentcomposev2.ListRolesRequest]) (*connect.Response[agentcomposev2.ListRolesResponse], error) {
	response := &agentcomposev2.ListRolesResponse{}
	for _, role := range controlauth.Roles() {
		response.Roles = append(response.Roles, &agentcomposev2.AuthRole{
			Name: string(role.Name), Description: role.Description, ReadOnly: role.ReadOnly, Builtin: role.Builtin,
		})
	}
	return connect.NewResponse(response), nil
}

func (h *AuthHandler) ListTokens(ctx context.Context, req *connect.Request[agentcomposev2.ListTokensRequest]) (*connect.Response[agentcomposev2.ListTokensResponse], error) {
	result, err := h.service.ListTokens(ctx, controlauth.TokenListOptions{
		IncludeRevoked: req.Msg.GetIncludeRevoked(), Limit: int(req.Msg.GetLimit()), BeforeID: strings.TrimSpace(req.Msg.GetCursor()),
	})
	if err != nil {
		return nil, authConnectError(err)
	}
	response := &agentcomposev2.ListTokensResponse{NextCursor: result.NextCursor}
	for _, item := range result.Tokens {
		response.Tokens = append(response.Tokens, authTokenToProto(item))
	}
	return connect.NewResponse(response), nil
}

func (h *AuthHandler) CreateToken(ctx context.Context, req *connect.Request[agentcomposev2.CreateTokenRequest]) (*connect.Response[agentcomposev2.CreateTokenResponse], error) {
	identity, ok := controlauth.IdentityFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, controlauth.ErrInvalidToken)
	}
	item, created, replay, err := h.service.CreateToken(ctx, identity, controlauth.CreateTokenInput{
		Name: req.Msg.GetName(), Description: req.Msg.GetDescription(), Role: controlauth.Role(req.Msg.GetRole()),
		Plaintext: req.Msg.GetToken(), ClientRequestID: req.Msg.GetClientRequestId(),
	}, req.Header().Get("X-Request-ID"))
	if err != nil {
		return nil, authConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.CreateTokenResponse{Item: authTokenToProto(item), Created: created, IdempotentReplay: replay}), nil
}

func (h *AuthHandler) RevokeToken(ctx context.Context, req *connect.Request[agentcomposev2.RevokeTokenRequest]) (*connect.Response[agentcomposev2.RevokeTokenResponse], error) {
	identity, ok := controlauth.IdentityFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, controlauth.ErrInvalidToken)
	}
	item, revoked, alreadyRevoked, err := h.service.RevokeToken(ctx, identity, req.Msg.GetToken(), req.Header().Get("X-Request-ID"))
	if err != nil {
		return nil, authConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.RevokeTokenResponse{Item: authTokenToProto(item), Revoked: revoked, AlreadyRevoked: alreadyRevoked}), nil
}

func (h *AuthHandler) ListOperationAudits(ctx context.Context, req *connect.Request[agentcomposev2.ListOperationAuditsRequest]) (*connect.Response[agentcomposev2.ListOperationAuditsResponse], error) {
	beforeSequence, err := parseAuditCursor(req.Msg.GetCursor())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	filter := controlauth.AuditFilter{
		Token: req.Msg.GetToken(), Action: req.Msg.GetAction(), ResourceType: req.Msg.GetResourceType(),
		ResourceID: req.Msg.GetResourceId(), ProjectID: req.Msg.GetProjectId(), Status: controlauth.AuditStatus(req.Msg.GetStatus()),
		Limit: int(req.Msg.GetLimit()), BeforeSequence: beforeSequence,
	}
	if value := req.Msg.GetStartedAfter(); value != nil {
		filter.StartedAfter = value.AsTime()
	}
	if value := req.Msg.GetStartedBefore(); value != nil {
		filter.StartedBefore = value.AsTime()
	}
	result, listErr := h.service.ListAudits(ctx, filter)
	if listErr != nil {
		return nil, authConnectError(listErr)
	}
	response := &agentcomposev2.ListOperationAuditsResponse{NextCursor: result.NextCursor}
	for _, item := range result.Audits {
		response.Audits = append(response.Audits, auditToProto(item))
	}
	return connect.NewResponse(response), nil
}

func authTokenToProto(item controlauth.Token) *agentcomposev2.AuthToken {
	result := &agentcomposev2.AuthToken{
		Id: item.ID, Name: item.Name, Description: item.Description, Role: string(item.Role), Origin: item.Origin,
		CreatedByTokenId: item.CreatedByTokenID, RevokedByTokenId: item.RevokedByTokenID,
	}
	if !item.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(item.CreatedAt)
	}
	if !item.RevokedAt.IsZero() {
		result.RevokedAt = timestamppb.New(item.RevokedAt)
	}
	return result
}

func auditToProto(item controlauth.Audit) *agentcomposev2.OperationAudit {
	result := &agentcomposev2.OperationAudit{
		Id: item.ID, Sequence: item.Sequence, RequestId: item.RequestID, TokenId: item.TokenID, TokenName: item.TokenName,
		Role: string(item.Role), Origin: item.Origin, Action: item.Action, ResourceType: item.Resource.Type,
		ResourceId: item.Resource.ID, ProjectId: item.Resource.ProjectID, Status: string(item.Status),
		ParamsJson: item.ParamsJSON, ChangesJson: item.ChangesJSON, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage,
	}
	if !item.StartedAt.IsZero() {
		result.StartedAt = timestamppb.New(item.StartedAt)
	}
	if !item.CompletedAt.IsZero() {
		result.CompletedAt = timestamppb.New(item.CompletedAt)
	}
	return result
}

func parseAuditCursor(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("audit cursor is invalid")
	}
	return parsed, nil
}

func authConnectError(err error) error {
	switch {
	case errors.Is(err, controlauth.ErrInvalidToken):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, controlauth.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, controlauth.ErrTokenNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, controlauth.ErrTokenConflict), errors.Is(err, controlauth.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, controlauth.ErrTokenSelfRevoke), errors.Is(err, controlauth.ErrEnvironmentToken), errors.Is(err, controlauth.ErrLastAdminToken):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
