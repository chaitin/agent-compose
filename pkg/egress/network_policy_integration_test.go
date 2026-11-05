package egress

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func runNetworkPolicyIntegration(t *testing.T) {
	t.Helper()
	endpoints := []EngineEndpoint{
		{Purpose: PurposeLLMFacade, Endpoint: Endpoint{Host: "facade.example", Port: 443, Protocol: ProtocolHTTPS}, Source: "runtime"},
		{Purpose: PurposeTelemetry, Endpoint: Endpoint{Host: "telemetry.example", Port: 4318, Protocol: ProtocolHTTP}, Source: "telemetry"},
	}
	declaration := NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{{
			Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS,
			AllowedIPs:    []string{"203.0.113.0/24"},
			Binaries:      []string{"/usr/bin/curl"},
			L7:            L7Constraint{Methods: []string{"GET"}, Paths: []string{"/v1/*"}},
			InjectHeaders: map[string]string{"Authorization": "Bearer ${NETWORK_POLICY_TEST_TOKEN}"},
		}, {
			Host: "plain.example.com", Port: 80, Protocol: ProtocolHTTP,
		}},
		Deny: []DenyEntry{{Host: "blocked.example.com", Port: 22, Protocol: ProtocolTCP}},
	}
	policy, err := CompileNetworkPolicy(declaration, endpoints)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy() error = %v", err)
	}
	request := NetworkRequest("sandbox-1", Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS})
	request.Binary = "/usr/bin/curl"
	request.Layer = LayerL7
	request.Method = "GET"
	request.Path = "/v1/items"
	request.ResolvedIPs = []string{"203.0.113.10"}
	result := Decide(policy, request)
	if !result.Allowed() || result.Enforcement != EnforcementEnforce {
		t.Fatalf("allowed result = %+v, want allowed enforce", result)
	}
	if !reflect.DeepEqual(result.InjectHeaders, declaration.Allow[0].InjectHeaders) {
		t.Fatalf("inject headers = %+v, want the declaration templates", result.InjectHeaders)
	}
	blocked := NetworkRequest("sandbox-1", Endpoint{Host: "blocked.example.com", Port: 22, Protocol: ProtocolTCP})
	if got := Decide(policy, blocked); got.Allowed() {
		t.Fatalf("blocked decision = %+v, want deny", got)
	}
	report, err := ReportNetworkPolicy(&declaration, endpoints)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy() error = %v", err)
	}
	if !report.Declared || len(report.Allow) != 2 || len(report.Allow[0].InjectedHeaders) != 1 {
		t.Fatalf("report = %+v, want two allowances and injected header names", report)
	}
	t.Setenv("NETWORK_POLICY_TEST_TOKEN", "secret")
	resolved, err := ExpandInjectHeaders(result.InjectHeaders, os.LookupEnv)
	if err != nil || resolved["Authorization"] != "Bearer secret" {
		t.Fatalf("ExpandInjectHeaders() = %+v, %v", resolved, err)
	}
	record := NewRecord(request, result).WithStatus(200)
	audit := AuditRecordFrom(record, time.Now())
	if len(audit.InjectedHeaders) != 1 || audit.InjectedHeaders[0] != "Authorization" {
		t.Fatalf("audit injected headers = %+v", audit.InjectedHeaders)
	}
}

func TestNetworkPolicyIntegration(t *testing.T) { runNetworkPolicyIntegration(t) }
func TestNetworkPolicyE2E(t *testing.T)         { runNetworkPolicyIntegration(t) }
