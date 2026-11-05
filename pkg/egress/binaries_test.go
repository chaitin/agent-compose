package egress

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBinaryPath(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{name: "canonical absolute path", value: "/usr/bin/curl", want: "/usr/bin/curl"},
		{name: "redundant separators are cleaned", value: "/usr//bin/./curl", want: "/usr/bin/curl"},
		{name: "empty path", value: "", wantErr: "must not be empty"},
		{name: "blank path", value: "   ", wantErr: "must not be empty"},
		{name: "relative path", value: "usr/bin/curl", wantErr: "must be absolute"},
		{name: "dotdot", value: "/usr/bin/../bin/curl", wantErr: "must not contain"},
		{name: "carriage return injection", value: "/usr/bin/cu\rrl", wantErr: "must not contain"},
		{name: "newline injection", value: "/usr/bin/cu\nrl", wantErr: "must not contain"},
		{name: "NUL injection", value: "/usr/bin/cu\x00rl", wantErr: "must not contain"},
		{name: "leading whitespace", value: " /usr/bin/curl", wantErr: "leading or trailing whitespace"},
		{name: "trailing whitespace", value: "/usr/bin/curl ", wantErr: "leading or trailing whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeBinaryPath(test.value)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("NormalizeBinaryPath(%q) = %q, %v; want error containing %q", test.value, got, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBinaryPath(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeBinaryPath(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeBinariesDeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizeBinaries([]string{"/usr/bin/python3.12", "/usr/bin/curl", "/usr/bin/curl"})
	if err != nil {
		t.Fatalf("NormalizeBinaries() error = %v", err)
	}
	want := []string{"/usr/bin/curl", "/usr/bin/python3.12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeBinaries() = %v, want %v", got, want)
	}
	empty, err := NormalizeBinaries(nil)
	if err != nil || empty != nil {
		t.Fatalf("NormalizeBinaries(nil) = %v, %v; want nil, nil", empty, err)
	}

	tooMany := make([]string, 0, MaxBinaryPathsPerEntry+1)
	for index := 0; index <= MaxBinaryPathsPerEntry; index++ {
		tooMany = append(tooMany, "/usr/bin/binary-"+strings.Repeat("x", index%3)+string(rune('a'+index%26)))
	}
	if _, err := NormalizeBinaries(tooMany); err == nil {
		t.Fatal("NormalizeBinaries accepted more paths than the bound")
	}
}

func TestDecideScopesAnAllowanceToDeclaredBinaries(t *testing.T) {
	endpoint := Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS}
	allow := NewPolicy(Deny, Rule{
		ID:       "network.allow",
		Match:    MatchEndpoint,
		Names:    []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:   Allow,
		Binaries: []string{"/usr/bin/python3", "/usr/local/bin/node"},
	})
	allowByDefault := NewPolicy(Allow, Rule{
		ID:       "network.allow",
		Match:    MatchEndpoint,
		Names:    []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:   Allow,
		Binaries: []string{"/usr/bin/python3"},
	})

	tests := []struct {
		name       string
		policy     Policy
		binary     string
		wantAction Action
		wantRuleID string
		wantReason string
	}{
		{
			name:       "a declared binary matches",
			policy:     allow,
			binary:     "/usr/bin/python3",
			wantAction: Allow,
			wantRuleID: "network.allow",
		},
		{
			name:       "another declared binary matches",
			policy:     allow,
			binary:     "/usr/local/bin/node",
			wantAction: Allow,
			wantRuleID: "network.allow",
		},
		{
			name:       "a different known binary falls through to the deny default",
			policy:     allow,
			binary:     "/usr/bin/curl",
			wantAction: Deny,
		},
		{
			name:       "an unknown binary cannot satisfy a binary-scoped allowance under a deny default",
			policy:     allow,
			wantAction: Deny,
			wantReason: binaryUnknownDenialReason,
		},
		{
			name:       "an unknown binary cannot fall through to an allow default",
			policy:     allowByDefault,
			wantAction: Deny,
			wantReason: binaryUnknownDenialReason,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := NetworkRequest("sandbox-1", endpoint)
			request.Binary = test.binary
			result := Decide(test.policy, request)
			if result.Action != test.wantAction {
				t.Fatalf("Action = %q, want %q (%s)", result.Action, test.wantAction, result.Reason)
			}
			if result.RuleID != test.wantRuleID {
				t.Fatalf("RuleID = %q, want %q", result.RuleID, test.wantRuleID)
			}
			if test.wantReason != "" && result.Reason != test.wantReason {
				t.Fatalf("Reason = %q, want %q", result.Reason, test.wantReason)
			}
		})
	}
}

func TestDecideDoesNotLetAnUnknownBinaryBypassABinaryScopedDenial(t *testing.T) {
	endpoint := Endpoint{Host: "metadata.example.com", Port: 80, Protocol: ProtocolAny}
	policy := NewPolicy(Allow, Rule{
		ID:       "network.deny",
		Match:    MatchEndpoint,
		Names:    []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:   Deny,
		Binaries: []string{"/usr/bin/curl"},
	})

	tests := []struct {
		name       string
		binary     string
		wantAction Action
	}{
		{name: "the declared binary is denied", binary: "/usr/bin/curl", wantAction: Deny},
		{name: "an unknown binary is not a bypass", wantAction: Deny},
		{name: "another known binary is not denied", binary: "/usr/bin/python3", wantAction: Allow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := NetworkRequest("sandbox-1", endpoint)
			request.Binary = test.binary
			result := Decide(policy, request)
			if result.Action != test.wantAction {
				t.Fatalf("Action = %q, want %q (%s)", result.Action, test.wantAction, result.Reason)
			}
		})
	}
}

func TestDecideRecordsAnAuditAllowanceWithoutEnforcingIt(t *testing.T) {
	endpoint := Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS}
	policy := NewPolicy(Deny, Rule{
		ID:          "network.allow",
		Match:       MatchEndpoint,
		Names:       []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:      Allow,
		Enforcement: EnforcementAudit,
	})
	result := Decide(policy, NetworkRequest("sandbox-1", endpoint))
	if !result.Allowed() {
		t.Fatalf("audit allowance denied the request: %+v", result)
	}
	if !result.Audit {
		t.Fatalf("Result.Audit = false, want an audit decision: %+v", result)
	}
	if result.RuleID != "network.allow" {
		t.Fatalf("RuleID = %q, want the matched allowance", result.RuleID)
	}
	if result.Reason != auditAllowanceReason {
		t.Fatalf("Reason = %q, want the distinct audit reason %q", result.Reason, auditAllowanceReason)
	}
	record := AuditRecordFrom(NewRecord(NetworkRequest("sandbox-1", endpoint), result), time.Unix(0, 0))
	if record.Action != string(Allow) || !record.Audit || record.Reason != auditAllowanceReason {
		t.Fatalf("audit record = %+v, want an allow with its audit reason", record)
	}
}

func TestAuditRecordCarriesTheBinary(t *testing.T) {
	request := NetworkRequest("sandbox-1", Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS})
	request.Binary = "/usr/bin/python3"
	result := Decide(NewPolicy(Deny, Rule{
		ID:       "network.allow",
		Match:    MatchEndpoint,
		Names:    []string{FormatEndpointPattern("api.example.com", 443, ProtocolHTTPS)},
		Action:   Allow,
		Binaries: []string{"/usr/bin/python3"},
	}), request)
	record := AuditRecordFrom(NewRecord(request, result), time.Unix(0, 0))
	if record.Binary != "/usr/bin/python3" {
		t.Fatalf("audit record binary = %q, want the request binary", record.Binary)
	}
}

func TestCompileNetworkPolicyCarriesBinariesAndEnforcement(t *testing.T) {
	declaration := NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{{
			Host:        "api.example.com",
			Port:        443,
			Protocol:    ProtocolAny,
			Binaries:    []string{"/usr/bin/curl"},
			Enforcement: EnforcementAudit,
		}},
		Deny: []DenyEntry{{
			Host:     "metadata.example.com",
			Port:     80,
			Protocol: ProtocolAny,
			Binaries: []string{"/usr/bin/curl"},
		}},
	}
	policy, err := CompileNetworkPolicy(declaration, nil)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy() error = %v", err)
	}
	rules := policy.Rules()
	if len(rules) != 2 {
		t.Fatalf("rules = %+v, want the denial and the allowance", rules)
	}
	if !reflect.DeepEqual(rules[0].Binaries, []string{"/usr/bin/curl"}) {
		t.Fatalf("deny binaries = %v, want the declared path", rules[0].Binaries)
	}
	if !reflect.DeepEqual(rules[1].Binaries, []string{"/usr/bin/curl"}) {
		t.Fatalf("allow binaries = %v, want the declared path", rules[1].Binaries)
	}
	if rules[1].Enforcement != EnforcementAudit {
		t.Fatalf("allow enforcement = %q, want %q", rules[1].Enforcement, EnforcementAudit)
	}
	report, err := ReportNetworkPolicy(&declaration, nil)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy() error = %v", err)
	}
	if len(report.Allow) != 1 || !reflect.DeepEqual(report.Allow[0].Binaries, []string{"/usr/bin/curl"}) || report.Allow[0].Enforcement != EnforcementAudit {
		t.Fatalf("report allowance = %+v, want the declared scope and enforcement", report.Allow)
	}
	if len(report.Deny) != 1 || !reflect.DeepEqual(report.Deny[0].Binaries, []string{"/usr/bin/curl"}) {
		t.Fatalf("report denial = %+v, want the declared scope", report.Deny)
	}

	for name, invalid := range map[string]NetworkDeclaration{
		"relative binary":     {Default: Deny, Allow: []AllowEntry{{Host: "api.example.com", Port: 443, Protocol: ProtocolAny, Binaries: []string{"bin/curl"}}}},
		"unknown enforcement": {Default: Deny, Allow: []AllowEntry{{Host: "api.example.com", Port: 443, Protocol: ProtocolAny, Enforcement: "observe"}}},
	} {
		if _, err := CompileNetworkPolicy(invalid, nil); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestLayerNetworkDeclarationsScopesBinaries(t *testing.T) {
	outer := &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{
		Host: "api.example.com", Port: 443, Protocol: ProtocolAny,
		Binaries: []string{"/usr/bin/curl", "/usr/bin/python3"},
	}}}
	inner := &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{
		Host: "api.example.com", Port: 443, Protocol: ProtocolAny,
		Binaries: []string{"/usr/bin/python3"},
	}}}
	layered, err := LayerNetworkDeclarations(outer, inner)
	if err != nil {
		t.Fatalf("LayerNetworkDeclarations() error = %v", err)
	}
	if len(layered.Allow) != 1 || !reflect.DeepEqual(layered.Allow[0].Binaries, []string{"/usr/bin/python3"}) {
		t.Fatalf("layered allowance = %+v, want the narrower binary scope", layered.Allow)
	}

	// An unscoped inner allowance is wider than the daemon's scoped one and is
	// refused rather than silently widening the daemon boundary.
	unscoped := &NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{Host: "api.example.com", Port: 443, Protocol: ProtocolAny}}}
	if _, err := LayerNetworkDeclarations(outer, unscoped); err == nil {
		t.Fatal("an unscoped agent allowance was accepted under a scoped daemon allowance")
	}

	// Two denials for the same endpoint with different scopes both survive.
	outerDeny := &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{
		Host: "metadata.example.com", Port: 80, Protocol: ProtocolAny, Binaries: []string{"/usr/bin/curl"},
	}}}
	innerDeny := &NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{
		Host: "metadata.example.com", Port: 80, Protocol: ProtocolAny, Binaries: []string{"/usr/bin/python3"},
	}}}
	layeredDeny, err := LayerNetworkDeclarations(outerDeny, innerDeny)
	if err != nil {
		t.Fatalf("LayerNetworkDeclarations() error = %v", err)
	}
	if len(layeredDeny.Deny) != 1 {
		t.Fatalf("layered denials = %+v, want one merged entry", layeredDeny.Deny)
	}
	want := []string{"/usr/bin/curl", "/usr/bin/python3"}
	if !reflect.DeepEqual(layeredDeny.Deny[0].Binaries, want) {
		t.Fatalf("layered denial binaries = %v, want %v", layeredDeny.Deny[0].Binaries, want)
	}
}
