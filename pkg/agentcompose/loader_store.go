package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"
	"context"
)

func (s *ConfigStore) ensureLoaderSchema(ctx context.Context) error {
	s.bindDomainStores()
	return s.LoaderStore.EnsureSchema(ctx)
}

func normalizeLoader(item Loader, assignID bool) (Loader, error) {
	return loaderdomain.NormalizeLoader(item, assignID)
}

func normalizeLoaderTrigger(loaderID string, trigger LoaderTrigger) (LoaderTrigger, error) {
	return loaderdomain.NormalizeLoaderTrigger(loaderID, trigger)
}

func encodeCapsetIDs(ids []string) (string, error) {
	return loaderdomain.EncodeCapsetIDs(ids)
}

func decodeCapsetIDs(raw string) []string {
	return loaderdomain.DecodeCapsetIDs(raw)
}
