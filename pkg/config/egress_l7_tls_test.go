package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func clearEgressL7Env(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		EgressL7TLSMITMName,
		EgressL7CACertFileEnv,
		EgressL7CAKeyFileEnv,
		EgressL7CACertPEMEnv,
		EgressL7CAKeyPEMEnv,
		EgressL7CABundleGuestPathEnv,
	} {
		t.Setenv(name, "")
	}
}

func TestEgressL7TLSDefaultsToDisabled(t *testing.T) {
	clearEgressL7Env(t)
	got, err := loadEgressL7TLSConfig()
	if err != nil {
		t.Fatalf("loadEgressL7TLSConfig() error = %v", err)
	}
	if got.MITM || got.ConfiguredCA() {
		t.Fatalf("config = %+v, want the zero value", got)
	}
}

func TestEgressL7TLSParsesAConfiguredCA(t *testing.T) {
	clearEgressL7Env(t)
	dir := t.TempDir()
	cert := filepath.Join(dir, "ca.pem")
	key := filepath.Join(dir, "ca-key.pem")
	t.Setenv(EgressL7TLSMITMName, "true")
	t.Setenv(EgressL7CACertFileEnv, cert)
	t.Setenv(EgressL7CAKeyFileEnv, key)
	t.Setenv(EgressL7CABundleGuestPathEnv, "/etc/agent-compose/egress-ca.pem")
	got, err := loadEgressL7TLSConfig()
	if err != nil {
		t.Fatalf("loadEgressL7TLSConfig() error = %v", err)
	}
	if !got.MITM || !got.ConfiguredCA() {
		t.Fatalf("config = %+v, want interception with a configured CA", got)
	}
	if got.CACertFile != cert || got.CAKeyFile != key || got.CABundleGuestPath != "/etc/agent-compose/egress-ca.pem" {
		t.Fatalf("config = %+v, want the configured paths", got)
	}
}

func TestEgressL7TLSRejectsAPartialOrContradictoryConfiguration(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "cert without key",
			env:     map[string]string{EgressL7TLSMITMName: "true", EgressL7CACertFileEnv: filepath.Join(dir, "ca.pem")},
			wantErr: "must be set together",
		},
		{
			name:    "inline cert without key",
			env:     map[string]string{EgressL7TLSMITMName: "true", EgressL7CACertPEMEnv: "cert"},
			wantErr: "must be set together",
		},
		{
			name: "file and inline CA together",
			env: map[string]string{
				EgressL7TLSMITMName:   "true",
				EgressL7CACertFileEnv: filepath.Join(dir, "ca.pem"),
				EgressL7CAKeyFileEnv:  filepath.Join(dir, "ca-key.pem"),
				EgressL7CACertPEMEnv:  "cert",
				EgressL7CAKeyPEMEnv:   "key",
			},
			wantErr: "alternatives",
		},
		{
			name:    "relative CA file",
			env:     map[string]string{EgressL7TLSMITMName: "true", EgressL7CACertFileEnv: "ca.pem", EgressL7CAKeyFileEnv: filepath.Join(dir, "ca-key.pem")},
			wantErr: "absolute path",
		},
		{
			name:    "guest bundle path without interception",
			env:     map[string]string{EgressL7CABundleGuestPathEnv: "/etc/ca.pem"},
			wantErr: "has no effect",
		},
		{
			name:    "relative guest bundle path",
			env:     map[string]string{EgressL7TLSMITMName: "true", EgressL7CABundleGuestPathEnv: "etc/ca.pem"},
			wantErr: "absolute guest path",
		},
		{
			name: "CA configured without interception",
			env: map[string]string{
				EgressL7CACertFileEnv: filepath.Join(dir, "ca.pem"),
				EgressL7CAKeyFileEnv:  filepath.Join(dir, "ca-key.pem"),
			},
			wantErr: "is false",
		},
		{
			name:    "invalid boolean",
			env:     map[string]string{EgressL7TLSMITMName: "sometimes"},
			wantErr: "must be true or false",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEgressL7Env(t)
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			_, err := loadEgressL7TLSConfig()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("loadEgressL7TLSConfig() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}
