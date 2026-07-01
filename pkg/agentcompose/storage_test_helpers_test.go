package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"testing"
)

func mustTestStore(t testing.TB, config *appconfig.Config) *Store {
	t.Helper()
	store, err := NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	return store
}

func mustTestConfigStore(t testing.TB, config *appconfig.Config) *ConfigStore {
	t.Helper()
	store, err := NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	return store
}

func sessionHasTag(session *Session, name, value string) bool {
	if session == nil {
		return false
	}
	for _, tag := range session.Summary.Tags {
		if tag.Name == name && tag.Value == value {
			return true
		}
	}
	return false
}
