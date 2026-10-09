package egress

import "testing"

func TestReportNetworkPolicy(t *testing.T) {
	engine, err := EngineEndpoints("http://host.docker.internal:7410", "https://otel.example.com")
	if err != nil {
		t.Fatalf("EngineEndpoints returned error: %v", err)
	}

	undeclared, err := ReportNetworkPolicy(nil, engine)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy(nil) returned error: %v", err)
	}
	if undeclared.Declared {
		t.Fatalf("undeclared report = %+v, want Declared false", undeclared)
	}
	if undeclared.Default != Allow {
		t.Fatalf("undeclared default = %q, want %q", undeclared.Default, Allow)
	}
	if len(undeclared.EngineAutoAllowed) != 0 {
		t.Fatalf("undeclared report lists engine exemptions: %+v", undeclared.EngineAutoAllowed)
	}

	declaration := &NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{
			{Host: "api.github.com", Port: 443, Protocol: ProtocolHTTPS},
			{Host: "cache.internal", Port: 6379, Protocol: ProtocolTCP},
		},
	}
	report, err := ReportNetworkPolicy(declaration, engine)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy returned error: %v", err)
	}
	if !report.Declared || report.Default != Deny {
		t.Fatalf("report = %+v, want a declared deny policy", report)
	}
	if len(report.Allow) != 2 {
		t.Fatalf("allow = %+v, want two entries", report.Allow)
	}
	if !report.Allow[0].Inspectable || report.Allow[0].Endpoint != "api.github.com:443/https" {
		t.Fatalf("allow[0] = %+v, want an inspectable HTTPS allowance", report.Allow[0])
	}
	if report.Allow[1].Inspectable || report.Allow[1].Endpoint != "cache.internal:6379/tcp" {
		t.Fatalf("allow[1] = %+v, want a non-inspectable TCP allowance", report.Allow[1])
	}
	if len(report.EngineAutoAllowed) != 2 {
		t.Fatalf("engine auto-allowed = %+v, want the facade and telemetry endpoints", report.EngineAutoAllowed)
	}
	if report.EngineAutoAllowed[0].Purpose != PurposeLLMFacade || report.EngineAutoAllowed[0].Endpoint != "host.docker.internal:7410/http" {
		t.Fatalf("engine auto-allowed[0] = %+v, want the LLM facade", report.EngineAutoAllowed[0])
	}
	if report.EngineAutoAllowed[0].Source != SourceRuntimeBaseURL || !report.EngineAutoAllowed[0].Inspectable {
		t.Fatalf("engine auto-allowed[0] = %+v, want a sourced inspectable endpoint", report.EngineAutoAllowed[0])
	}
	if report.EngineAutoAllowed[1].Purpose != PurposeTelemetry || report.EngineAutoAllowed[1].Endpoint != "otel.example.com:443/https" {
		t.Fatalf("engine auto-allowed[1] = %+v, want the telemetry endpoint", report.EngineAutoAllowed[1])
	}

	// A permissive declaration does not rely on the engine exemption, so the
	// report must not present one.
	allowAll := &NetworkDeclaration{Default: Allow}
	permissive, err := ReportNetworkPolicy(allowAll, engine)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy(allow-all) returned error: %v", err)
	}
	if len(permissive.EngineAutoAllowed) != 0 {
		t.Fatalf("allow-all report lists engine exemptions: %+v", permissive.EngineAutoAllowed)
	}

	if _, err := ReportNetworkPolicy(&NetworkDeclaration{Default: Action("")}, engine); err == nil {
		t.Fatal("ReportNetworkPolicy accepted an invalid declaration")
	}
}
