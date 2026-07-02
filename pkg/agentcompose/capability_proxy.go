package agentcompose

import (
	"context"
	"strings"

	"github.com/samber/do/v2"

	domaincap "agent-compose/internal/agentcompose/capability"
	"agent-compose/internal/agentcompose/transport/httpapi"
	"agent-compose/internal/capproxy"
	appconfig "agent-compose/internal/config"
)

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*ConfigStore](di)
	return httpapi.NewCapabilityProxyServer(httpapi.CapabilityProxyConfig{
		Listen: strings.TrimSpace(conf.CapGRPCListen),
		OctoBus: func(ctx context.Context) (string, string, bool) {
			settings, err := configDB.GetCapabilityGateway(ctx)
			if err != nil || strings.TrimSpace(settings.Addr) == "" {
				return "", "", false
			}
			return settings.Addr, settings.Token, true
		},
		SessionResolver: do.MustInvoke[*Store](di),
	}), nil
}

func (s *Store) ResolveCapabilitySession(ctx context.Context, token string) (capproxy.SessionBinding, error) {
	return (httpapi.CapabilitySessionResolver{
		SessionRoot: s.config.SessionRoot,
		Store:       s,
	}).ResolveCapabilitySession(ctx, token)
}

func sessionCapabilityToken(session *Session) string {
	return sessionEnvValue(session, domaincap.SessionTokenEnvName)
}

// sessionCapabilityCapsets reads the allowed capset set from the session's
// capset tags (server-side binding; the guest never sees this list).
func sessionCapabilityCapsets(session *Session) []string {
	if session == nil {
		return nil
	}
	tags := make([]domaincap.SessionTag, 0, len(session.Summary.Tags))
	for _, tag := range session.Summary.Tags {
		tags = append(tags, domaincap.SessionTag{Name: tag.Name, Value: tag.Value})
	}
	return domaincap.SessionCapabilityCapsets(tags)
}

func sessionEnvValue(session *Session, name string) string {
	if session == nil {
		return ""
	}
	env := make([]domaincap.SessionEnvVar, 0, len(session.EnvItems))
	for _, item := range session.EnvItems {
		env = append(env, domaincap.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return domaincap.SessionEnvValue(env, name)
}
