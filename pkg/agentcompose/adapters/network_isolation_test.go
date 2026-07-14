package adapters

import (
	"context"
	"errors"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/networks"
)

func TestRuntimeIsolationPolicy(t *testing.T) {
	state := &domain.SandboxNetworkState{Attachments: []domain.SandboxNetworkEndpoint{{Name: "frontend"}}}
	tests := []struct {
		name    string
		driver  string
		enforce bool
		want    string
		wantErr bool
	}{
		{name: "microsandbox enforced", driver: driverpkg.RuntimeDriverMicrosandbox, want: networks.IsolationEnforced},
		{name: "docker reported unprotected", driver: driverpkg.RuntimeDriverDocker, want: networks.IsolationUnprotected},
		{name: "boxlite reported unprotected", driver: driverpkg.RuntimeDriverBoxlite, want: networks.IsolationUnprotected},
		{name: "docker strict rejected", driver: driverpkg.RuntimeDriverDocker, enforce: true, wantErr: true},
		{name: "boxlite strict rejected", driver: driverpkg.RuntimeDriverBoxlite, enforce: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := &domain.Sandbox{
				Summary:       domain.SandboxSummary{Driver: tt.driver},
				NetworkIntent: &domain.SandboxNetworkIntent{Attachments: []domain.SandboxNetworkAttachment{{Name: "frontend"}}},
			}
			policy := RuntimeIsolationPolicy{Enforce: tt.enforce}
			if err := policy.Validate(context.Background(), sandbox); err != nil {
				if tt.wantErr && errors.Is(err, networks.ErrUnsupported) {
					return
				}
				t.Fatalf("Validate() error = %v", err)
			}
			got, err := policy.Evaluate(context.Background(), sandbox, state)
			if tt.wantErr {
				t.Fatal("Validate() accepted unsupported strict isolation")
			}
			if err != nil || got != tt.want {
				t.Fatalf("Evaluate() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}
