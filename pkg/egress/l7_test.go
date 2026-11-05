package egress

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeL7Constraint(t *testing.T) {
	methodsOverLimit := make([]string, MaxL7Methods+1)
	for i := range methodsOverLimit {
		methodsOverLimit[i] = "METHOD" + strings.Repeat("X", i%3) + string(rune('A'+i))
	}
	pathsOverLimit := make([]string, MaxL7Paths+1)
	for i := range pathsOverLimit {
		pathsOverLimit[i] = "/path/" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	tests := []struct {
		name        string
		methods     []string
		paths       []string
		wantMethods []string
		wantPaths   []string
		wantErr     string
	}{
		{
			name:        "canonicalizes and sorts",
			methods:     []string{"post", "GET"},
			paths:       []string{"/b", "/a/*"},
			wantMethods: []string{"GET", "POST"},
			wantPaths:   []string{"/a/*", "/b"},
		},
		{
			name:    "accepts an empty constraint",
			methods: nil,
			paths:   nil,
		},
		{
			name:    "rejects an invalid method token",
			methods: []string{"GE T"},
			wantErr: "not a valid HTTP method",
		},
		{
			name:    "rejects an empty method",
			methods: []string{" "},
			wantErr: "is empty",
		},
		{
			name:    "rejects a duplicate method after normalization",
			methods: []string{"GET", "get"},
			wantErr: "duplicate method",
		},
		{
			name:    "rejects a relative path",
			paths:   []string{"v1/items"},
			wantErr: "must start with",
		},
		{
			name:    "rejects a query",
			paths:   []string{"/v1/items?debug=1"},
			wantErr: "query or fragment",
		},
		{
			name:    "rejects a wildcard that is not final",
			paths:   []string{"/v1/*/items"},
			wantErr: "only as its final character",
		},
		{
			name:    "rejects a duplicate path",
			paths:   []string{"/v1", "/v1"},
			wantErr: "duplicate path",
		},
		{
			name:    "bounds the method count",
			methods: methodsOverLimit,
			wantErr: "at most",
		},
		{
			name:    "bounds the path count",
			paths:   pathsOverLimit,
			wantErr: "at most",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			constraint, err := NormalizeL7Constraint(test.methods, test.paths)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("NormalizeL7Constraint() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeL7Constraint() error = %v", err)
			}
			if strings.Join(constraint.Methods, ",") != strings.Join(test.wantMethods, ",") {
				t.Fatalf("methods = %v, want %v", constraint.Methods, test.wantMethods)
			}
			if strings.Join(constraint.Paths, ",") != strings.Join(test.wantPaths, ",") {
				t.Fatalf("paths = %v, want %v", constraint.Paths, test.wantPaths)
			}
		})
	}
}

func TestL7ConstraintMatches(t *testing.T) {
	constraint := L7Constraint{Methods: []string{"GET", "HEAD"}, Paths: []string{"/v1/*", "/health"}}
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "allowed method and prefix", method: "GET", path: "/v1/items", want: true},
		{name: "allowed method is case-insensitive", method: "get", path: "/health", want: true},
		{name: "denied method", method: "POST", path: "/v1/items", want: false},
		{name: "path outside the prefix", method: "GET", path: "/v2/items", want: false},
		{name: "prefix does not leak across a segment", method: "GET", path: "/v10/items", want: false},
		{name: "empty path is the root", method: "GET", path: "", want: false},
		{name: "empty constraint matches anything", method: "DELETE", path: "/anything", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := constraint
			if test.name == "empty constraint matches anything" {
				candidate = L7Constraint{}
			}
			if got := candidate.Matches(test.method, test.path); got != test.want {
				t.Fatalf("Matches(%q, %q) = %v, want %v", test.method, test.path, got, test.want)
			}
		})
	}
}

func TestDecideEvaluatesL7Constraints(t *testing.T) {
	policy := NewPolicy(Deny, Rule{
		ID:     "network.allow",
		Match:  MatchEndpoint,
		Names:  []string{"api.example:443/https"},
		Action: Allow,
		L7:     L7Constraint{Methods: []string{"GET"}, Paths: []string{"/v1/*"}},
	})
	endpoint, err := NewEndpoint("api.example", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	request := NetworkRequest("sandbox-1", endpoint)
	request.Layer = LayerL7
	request.Method = "GET"
	request.Path = "/v1/items"
	if result := Decide(policy, request); !result.Allowed() {
		t.Fatalf("GET /v1/items = %+v, want allow", result)
	}

	deniedMethod := request
	deniedMethod.Method = "POST"
	if result := Decide(policy, deniedMethod); result.Allowed() {
		t.Fatalf("POST /v1/items = %+v, want deny", result)
	}

	deniedPath := request
	deniedPath.Path = "/v2/items"
	if result := Decide(policy, deniedPath); result.Allowed() {
		t.Fatalf("GET /v2/items = %+v, want deny", result)
	}

	// A CONNECT the engine did not inspect cannot satisfy an L7 declaration.
	uninspected := request
	uninspected.Layer = LayerL4
	uninspected.Method = ""
	uninspected.Path = ""
	if result := Decide(policy, uninspected); result.Allowed() {
		t.Fatalf("uninspected CONNECT = %+v, want deny", result)
	}

	// A caller that will terminate TLS defers the check, and the result says so
	// so the caller knows it must inspect the payload.
	deferred := uninspected
	deferred.DeferL7 = true
	result := Decide(policy, deferred)
	if !result.Allowed() || !result.L7Deferred {
		t.Fatalf("deferred CONNECT = %+v, want an allow with the L7 check deferred", result)
	}
}

func TestDecideAuditsAnL7MismatchWithoutDenying(t *testing.T) {
	policy := NewPolicy(Deny, Rule{
		ID:          "network.allow",
		Match:       MatchEndpoint,
		Names:       []string{"api.example:443/https"},
		Action:      Allow,
		L7:          L7Constraint{Methods: []string{"GET"}},
		Enforcement: EnforcementAudit,
	})
	endpoint, err := NewEndpoint("api.example", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	request := NetworkRequest("sandbox-1", endpoint)
	request.Layer = LayerL7
	request.Method = "POST"
	request.Path = "/v1/items"
	result := Decide(policy, request)
	if !result.Allowed() {
		t.Fatalf("audit-only POST = %+v, want allow", result)
	}
	if result.Enforcement != EnforcementAudit {
		t.Fatalf("enforcement = %q, want %q", result.Enforcement, EnforcementAudit)
	}
	if !strings.Contains(result.Reason, "audit") {
		t.Fatalf("reason = %q, want it to name the audit mode", result.Reason)
	}
}

func TestPolicyGenerationIncludesL7AndAuditMode(t *testing.T) {
	base := NewPolicy(Deny, Rule{
		ID: "network.allow", Match: MatchEndpoint, Names: []string{"api.example:443/https"}, Action: Allow,
	})
	withL7 := NewPolicy(Deny, Rule{
		ID: "network.allow", Match: MatchEndpoint, Names: []string{"api.example:443/https"}, Action: Allow,
		L7: L7Constraint{Methods: []string{"GET"}},
	})
	if base.Generation() == withL7.Generation() {
		t.Fatal("adding an L7 constraint did not change the policy generation")
	}
	audited := NewPolicy(Deny, Rule{
		ID: "network.allow", Match: MatchEndpoint, Names: []string{"api.example:443/https"}, Action: Allow,
		L7:          L7Constraint{Methods: []string{"GET"}},
		Enforcement: EnforcementAudit,
	})
	if withL7.Generation() == audited.Generation() {
		t.Fatal("switching an L7 rule to audit did not change the policy generation")
	}
}

func TestNormalizeEnforcementDefaultsToEnforce(t *testing.T) {
	tests := map[string]Enforcement{
		"":        EnforcementEnforce,
		"enforce": EnforcementEnforce,
		"ENFORCE": EnforcementEnforce,
		"audit":   EnforcementAudit,
		" AUDIT ": EnforcementAudit,
	}
	for raw, want := range tests {
		got, err := ParseEnforcement(raw)
		if err != nil {
			t.Fatalf("ParseEnforcement(%q) error = %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseEnforcement(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseEnforcement("monitor"); err == nil {
		t.Fatal("ParseEnforcement accepted an unknown value")
	}
}

func TestAuditRecordFromCarriesTheL7Decision(t *testing.T) {
	request := Request{
		Consumer:    "sandbox-1",
		Kind:        KindNetworkEndpoint,
		Name:        "api.example:443/https",
		ResolvedIPs: []string{"93.184.216.34"},
		Layer:       LayerL7,
		Method:      "POST",
		Path:        "/v1/items",
	}
	result := Result{
		Action:      Allow,
		RuleID:      "network.allow",
		Reason:      "the endpoint is declared with enforcement: audit",
		Generation:  9,
		Enforcement: EnforcementAudit,
	}
	record := AuditRecordFrom(NewRecord(request, result).WithStatus(200), time.Unix(0, 0))
	if record.Layer != string(LayerL7) || record.Method != "POST" || record.Path != "/v1/items" {
		t.Fatalf("audit L7 fields = %+v, want layer/method/path", record)
	}
	if record.Status != 200 || record.Enforcement != string(EnforcementAudit) || record.RuleID != "network.allow" {
		t.Fatalf("audit decision fields = %+v, want status, enforcement, and rule", record)
	}
}
