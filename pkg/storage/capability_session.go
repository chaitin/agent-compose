package storage

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agent-compose/pkg/capproxy"
)

const (
	capabilitySessionTokenEnvName = "CAP_TOKEN"
	capabilityCapsetTagName       = "capset"
)

func (s *Store) ResolveCapabilitySession(ctx context.Context, token string) (capproxy.SessionBinding, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return capproxy.SessionBinding{}, fmt.Errorf("capability session token is required")
	}
	entries, err := os.ReadDir(s.config.SessionRoot)
	if err != nil {
		return capproxy.SessionBinding{}, fmt.Errorf("read session root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		session, err := s.GetSession(ctx, entry.Name())
		if err != nil {
			continue
		}
		if sessionCapabilityToken(session) != token {
			continue
		}
		if session.Summary.VMStatus != VMStatusRunning {
			return capproxy.SessionBinding{}, fmt.Errorf("capability session token is not active")
		}
		capsetIDs := sessionCapabilityCapsets(session)
		if len(capsetIDs) == 0 {
			return capproxy.SessionBinding{}, fmt.Errorf("session %s has no capability capset", session.Summary.ID)
		}
		return capproxy.SessionBinding{SessionID: session.Summary.ID, CapsetIDs: capsetIDs}, nil
	}
	return capproxy.SessionBinding{}, fmt.Errorf("capability session token not found")
}

func sessionCapabilityToken(session *Session) string {
	return sessionEnvValue(session, capabilitySessionTokenEnvName)
}

func sessionCapabilityCapsets(session *Session) []string {
	if session == nil {
		return nil
	}
	var ids []string
	for _, tag := range session.Summary.Tags {
		if tag.Name == capabilityCapsetTagName {
			if v := strings.TrimSpace(tag.Value); v != "" {
				ids = append(ids, v)
			}
		}
	}
	return normalizeCapsetIDs(ids)
}

func sessionEnvValue(session *Session, name string) string {
	if session == nil {
		return ""
	}
	for _, item := range session.EnvItems {
		if item.Name == name {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}
