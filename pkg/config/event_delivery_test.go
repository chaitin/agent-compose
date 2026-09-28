package config

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestNewConfigEventDeliveryScope(t *testing.T) {
	for _, test := range []struct {
		raw     string
		want    domain.EventDeliveryScope
		wantErr bool
	}{
		{raw: "", want: domain.EventDeliveryScopeProject},
		{raw: "daemon", want: domain.EventDeliveryScopeDaemon},
		{raw: "tenant", wantErr: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv(eventDeliveryScopeName, test.raw)
			di := do.New()
			do.ProvideValue(di, slog.Default())
			config, err := NewConfig(di)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NewConfig accepted %s=%q", eventDeliveryScopeName, test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConfig: %v", err)
			}
			if config.EventDeliveryScope != test.want {
				t.Fatalf("EventDeliveryScope = %q, want %q", config.EventDeliveryScope, test.want)
			}
		})
	}
}
