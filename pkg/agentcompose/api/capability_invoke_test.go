package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type capabilityGatewaySource struct {
	settings domain.CapabilityGatewaySettings
}

func (s capabilityGatewaySource) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return s.settings, nil
}

func TestInvokeCapabilityForwardsOverConnect(t *testing.T) {
	var (
		gotAuthorization string
		gotPath          string
		gotBody          map[string]any
	)
	octobus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotPath = r.URL.EscapedPath()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"response_code":0,"data":{"kind":"ip"}}`))
	}))
	defer octobus.Close()

	provider := capabilities.NewDynamicProvider(capabilityGatewaySource{
		settings: domain.CapabilityGatewaySettings{
			Addr:  octobus.URL,
			Token: "octobus-secret",
		},
	}, "")
	path, handler := agentcomposev2connect.NewCapabilityServiceHandler(NewCapabilityV2Handler(provider, nil))
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"capsetId":"threat-intel","instanceId":"cloud","serviceId":"ThreatBook.Cloud","method":"IpReputation","payloadJson":"{\"resource\":\"8.8.8.8\"}"}`
	response, err := http.Post(server.URL+path+"InvokeCapability", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var decoded struct {
		ResultJSON string `json:"resultJson"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ResultJSON != `{"response_code":0,"data":{"kind":"ip"}}` {
		t.Fatalf("resultJson = %s", decoded.ResultJSON)
	}
	if gotAuthorization != "Bearer octobus-secret" {
		t.Fatalf("authorization = %q", gotAuthorization)
	}
	if gotPath != "/capsets/threat-intel/connect/cloud/ThreatBook.Cloud/IpReputation" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["resource"] != "8.8.8.8" {
		t.Fatalf("body = %+v", gotBody)
	}
}
