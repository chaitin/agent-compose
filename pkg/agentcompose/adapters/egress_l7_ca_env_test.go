package adapters

import (
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func TestEgressL7CAEnvItemsNameTheGuestBundle(t *testing.T) {
	config := &appconfig.Config{EgressL7TLS: appconfig.EgressL7TLSConfig{
		MITM:              true,
		CABundleGuestPath: "/etc/agent-compose/egress-ca.pem",
	}}
	items := egressL7CAEnvItems(config)
	want := map[string]string{
		"SSL_CERT_FILE":       "/etc/agent-compose/egress-ca.pem",
		"CURL_CA_BUNDLE":      "/etc/agent-compose/egress-ca.pem",
		"REQUESTS_CA_BUNDLE":  "/etc/agent-compose/egress-ca.pem",
		"NODE_EXTRA_CA_CERTS": "/etc/agent-compose/egress-ca.pem",
		"GIT_SSL_CAINFO":      "/etc/agent-compose/egress-ca.pem",
	}
	if len(items) != len(want) {
		t.Fatalf("items = %+v, want %d variables", items, len(want))
	}
	for _, item := range items {
		value, ok := want[item.Name]
		if !ok {
			t.Fatalf("unexpected variable %q", item.Name)
		}
		if item.Value != value || item.Secret {
			t.Fatalf("item = %+v, want %q and not secret", item, value)
		}
	}
}

func TestEgressL7CAEnvItemsAreAbsentWithoutInterceptionOrAPath(t *testing.T) {
	tests := map[string]*appconfig.Config{
		"no config":                 nil,
		"interception disabled":     {EgressL7TLS: appconfig.EgressL7TLSConfig{CABundleGuestPath: "/etc/ca.pem"}},
		"no guest path":             {EgressL7TLS: appconfig.EgressL7TLSConfig{MITM: true}},
		"only the CA is configured": {EgressL7TLS: appconfig.EgressL7TLSConfig{MITM: true, CACertFile: "/ca.pem"}},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if items := egressL7CAEnvItems(config); len(items) != 0 {
				t.Fatalf("items = %+v, want none", items)
			}
		})
	}
}
