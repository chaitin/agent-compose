package capability

import "testing"

func TestParseCapsetDeclaration(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		serverName  string
		capsetID    string
		qualified   bool
		wantFailure bool
	}{
		{name: "global", value: "legacy-capset", capsetID: "legacy-capset"},
		{name: "project", value: "internal/dev", serverName: "internal", capsetID: "dev", qualified: true},
		{name: "empty server", value: "/dev", wantFailure: true},
		{name: "empty capset", value: "internal/", wantFailure: true},
		{name: "nested slash", value: "internal/team/dev", wantFailure: true},
		{name: "invalid capset start", value: "internal/1dev", wantFailure: true},
		{name: "too long", value: "internal/a123456789012345678901234567890123456789012345678901234567890123", wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declaration, err := ParseCapsetDeclaration(tt.value)
			if tt.wantFailure {
				if err == nil {
					t.Fatalf("ParseCapsetDeclaration(%q) succeeded", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if declaration.ServerName != tt.serverName || declaration.CapsetID != tt.capsetID || declaration.Qualified() != tt.qualified {
				t.Fatalf("ParseCapsetDeclaration(%q) = %#v", tt.value, declaration)
			}
		})
	}
}
