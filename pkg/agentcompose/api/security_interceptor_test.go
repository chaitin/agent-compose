package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	controlauth "agent-compose/pkg/auth"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type securityAuditStoreFake struct{ audits []controlauth.Audit }

func (*securityAuditStoreFake) ReconcileEnvironmentToken(context.Context, string, time.Time) (controlauth.Token, bool, error) {
	return controlauth.Token{}, false, nil
}
func (*securityAuditStoreFake) AuthInitialized(context.Context) (bool, error) { return false, nil }
func (*securityAuditStoreFake) AuthenticateTokenHash(context.Context, string) (controlauth.Token, error) {
	return controlauth.Token{}, controlauth.ErrInvalidToken
}
func (*securityAuditStoreFake) ListTokens(context.Context, controlauth.TokenListOptions) (controlauth.TokenListResult, error) {
	return controlauth.TokenListResult{}, nil
}
func (*securityAuditStoreFake) CreateToken(context.Context, controlauth.Identity, controlauth.CreateTokenInput, string, string, time.Time) (controlauth.Token, bool, bool, error) {
	return controlauth.Token{}, false, false, nil
}
func (*securityAuditStoreFake) RevokeToken(context.Context, controlauth.Identity, string, string, string, time.Time) (controlauth.Token, bool, bool, error) {
	return controlauth.Token{}, false, false, nil
}
func (s *securityAuditStoreFake) BeginAudit(_ context.Context, audit controlauth.Audit) error {
	s.audits = append(s.audits, audit)
	return nil
}
func (*securityAuditStoreFake) FinishAudit(context.Context, controlauth.Audit) error { return nil }
func (*securityAuditStoreFake) ListAudits(context.Context, controlauth.AuditFilter) (controlauth.AuditListResult, error) {
	return controlauth.AuditListResult{}, nil
}

func newSecurityTestService(t *testing.T) (*controlauth.Service, *securityAuditStoreFake) {
	t.Helper()
	store := &securityAuditStoreFake{}
	service, err := controlauth.NewService(t.Context(), store, "")
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func assertDeniedAudit(t *testing.T, store *securityAuditStoreFake, requestID, action string) {
	t.Helper()
	if len(store.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(store.audits))
	}
	audit := store.audits[0]
	if audit.RequestID != requestID || audit.Action != action || audit.Status != controlauth.AuditStatusDenied {
		t.Fatalf("denied audit = %#v", audit)
	}
}

func TestDeniedUnaryAuditPreservesRequestID(t *testing.T) {
	service, store := newSecurityTestService(t)
	interceptor := NewSecurityInterceptor(service)
	_, authHandler := agentcomposev2connect.NewAuthServiceHandler(NewAuthHandler(service), connect.WithInterceptors(interceptor))
	identity := controlauth.Identity{TokenID: "reader-1", TokenName: "reader", Role: controlauth.RoleReadOnlyAdmin, Origin: controlauth.OriginIssued}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authHandler.ServeHTTP(response, request.WithContext(controlauth.WithIdentity(request.Context(), identity)))
	}))
	t.Cleanup(server.Close)

	client := agentcomposev2connect.NewAuthServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&agentcomposev2.CreateTokenRequest{})
	request.Header().Set("X-Request-ID", "unary-request-1")
	_, err := client.CreateToken(t.Context(), request)
	if !errors.Is(err, controlauth.ErrPermissionDenied) && connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateToken error = %v, want permission denied", err)
	}
	assertDeniedAudit(t, store, "unary-request-1", "auth.token.create")
}

type deniedStreamingConn struct{ requestHeader http.Header }

func (c *deniedStreamingConn) Spec() connect.Spec {
	return connect.Spec{Procedure: agentcomposev2connect.RunServiceRunAgentStreamProcedure}
}
func (*deniedStreamingConn) Peer() connect.Peer           { return connect.Peer{} }
func (*deniedStreamingConn) Receive(any) error            { return io.EOF }
func (c *deniedStreamingConn) RequestHeader() http.Header { return c.requestHeader }
func (*deniedStreamingConn) Send(any) error               { return nil }
func (*deniedStreamingConn) ResponseHeader() http.Header  { return make(http.Header) }
func (*deniedStreamingConn) ResponseTrailer() http.Header { return make(http.Header) }

func TestDeniedStreamingAuditPreservesRequestID(t *testing.T) {
	service, store := newSecurityTestService(t)
	interceptor := NewSecurityInterceptor(service)
	nextCalled := false
	handler := interceptor.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		nextCalled = true
		return nil
	})
	identity := controlauth.Identity{TokenID: "reader-1", TokenName: "reader", Role: controlauth.RoleReadOnlyAdmin, Origin: controlauth.OriginIssued}
	requestHeader := make(http.Header)
	requestHeader.Set("X-Request-ID", "stream-request-1")
	conn := &deniedStreamingConn{requestHeader: requestHeader}
	err := handler(controlauth.WithIdentity(t.Context(), identity), conn)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("stream error = %v, want permission denied", err)
	}
	if nextCalled {
		t.Fatal("denied stream called next handler")
	}
	assertDeniedAudit(t, store, "stream-request-1", "run.agent.stream")
}

func TestEveryV2ProcedureHasSecurityPolicy(t *testing.T) {
	services := agentcomposev2.File_agentcompose_v2_agentcompose_proto.Services()
	for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
		service := services.Get(serviceIndex)
		methods := service.Methods()
		for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
			procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(methodIndex).Name())
			if _, ok := procedurePolicies[procedure]; !ok {
				t.Errorf("procedure %s has no security policy", procedure)
			}
		}
	}
}

func TestAuditSummaryRedactsTokenAndSensitiveContent(t *testing.T) {
	request := &agentcomposev2.CreateTokenRequest{
		Name: "reader", Description: "safe", Role: "read-only-admin", Token: "ac_plaintext_must_not_appear", ClientRequestId: "request-1",
	}
	summary := summarizeMessage(request)
	if strings.Contains(summary, request.GetToken()) || strings.Contains(summary, `"token"`) {
		t.Fatalf("summary leaked token: %s", summary)
	}
	if !strings.Contains(summary, `"name":"reader"`) || !strings.Contains(summary, `"client_request_id":"request-1"`) {
		t.Fatalf("summary omitted safe fields: %s", summary)
	}
}

func TestTokenAPIResponseDescriptorsCannotCarrySecretOrHash(t *testing.T) {
	for _, message := range []protoreflect.MessageDescriptor{
		(&agentcomposev2.AuthToken{}).ProtoReflect().Descriptor(),
		(&agentcomposev2.CreateTokenResponse{}).ProtoReflect().Descriptor(),
		(&agentcomposev2.RevokeTokenResponse{}).ProtoReflect().Descriptor(),
		(&agentcomposev2.ListTokensResponse{}).ProtoReflect().Descriptor(),
	} {
		fields := message.Fields()
		for index := 0; index < fields.Len(); index++ {
			name := strings.ToLower(string(fields.Get(index).Name()))
			if name == "token" || strings.Contains(name, "secret") || strings.Contains(name, "hash") || strings.Contains(name, "fingerprint") {
				t.Errorf("response message %s exposes forbidden field %s", message.FullName(), name)
			}
		}
	}
}
