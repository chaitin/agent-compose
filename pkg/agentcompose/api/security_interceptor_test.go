package api

import (
	"strings"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
)

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
