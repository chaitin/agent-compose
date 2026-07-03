package llms

import (
	"context"
	"os"
	"strings"

	domain "agent-compose/pkg/model"
)

type GlobalEnvStore interface {
	ListGlobalEnv(ctx context.Context) ([]domain.SessionEnvVar, error)
}

type ClientConfig struct {
	Endpoint string
	Protocol string
}

func ResolveSetting(ctx context.Context, store GlobalEnvStore, fallback string, keys ...string) string {
	if value := strings.TrimSpace(LookupGlobalEnv(ctx, store, keys...)); value != "" {
		return value
	}
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(fallback); value != "" {
		return value
	}
	return ""
}

func ResolveEndpoint(ctx context.Context, store GlobalEnvStore, config ClientConfig) string {
	return ResolveEndpointForProtocol(ctx, store, config, ResolveProtocol(ctx, store, config))
}

func ResolveEndpointForProtocol(ctx context.Context, store GlobalEnvStore, config ClientConfig, protocol string) string {
	if value := strings.TrimSpace(LookupGlobalEnv(ctx, store, "LLM_API_ENDPOINT")); value != "" {
		return NormalizeAPIEndpointForProtocol(value, protocol)
	}
	if value := strings.TrimSpace(os.Getenv("LLM_API_ENDPOINT")); value != "" {
		return NormalizeAPIEndpointForProtocol(value, protocol)
	}
	if value := strings.TrimSpace(config.Endpoint); value != "" {
		return NormalizeAPIEndpointForProtocol(value, protocol)
	}
	return NormalizeAPIEndpointForProtocol("https://api.openai.com", protocol)
}

func ResolveProtocol(ctx context.Context, store GlobalEnvStore, config ClientConfig) string {
	protocol := strings.ToLower(strings.TrimSpace(LookupGlobalEnv(ctx, store, "LLM_API_PROTOCOL")))
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(os.Getenv("LLM_API_PROTOCOL")))
	}
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(config.Protocol))
	}
	return NormalizeWireAPI(protocol)
}

func LookupGlobalEnv(ctx context.Context, store GlobalEnvStore, keys ...string) string {
	if store == nil || len(keys) == 0 {
		return ""
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		for _, item := range items {
			if !strings.EqualFold(strings.TrimSpace(item.Name), key) {
				continue
			}
			if value := strings.TrimSpace(item.Value); value != "" {
				return value
			}
		}
	}
	return ""
}
