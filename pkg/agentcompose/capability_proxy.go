package agentcompose

import (
	"context"
	"strings"

	"github.com/samber/do/v2"

	"agent-compose/pkg/capproxy"
	appconfig "agent-compose/pkg/config"
)

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*ConfigStore](di)
	return capproxy.NewServer(capproxy.Config{
		Listen: strings.TrimSpace(conf.CapGRPCListen),
		OctoBus: func(ctx context.Context) (string, string, bool) {
			settings, err := configDB.GetCapabilityGateway(ctx)
			if err != nil || strings.TrimSpace(settings.Addr) == "" {
				return "", "", false
			}
			return settings.Addr, settings.Token, true
		},
	}, do.MustInvoke[*Store](di)), nil
}
