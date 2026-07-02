package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"

	domaincap "agent-compose/internal/agentcompose/capability"
	sessiondomain "agent-compose/internal/agentcompose/session"
	"agent-compose/internal/capproxy"
)

type CapabilitySessionStore interface {
	GetSession(ctx context.Context, sessionID string) (*sessiondomain.Session, error)
}

type CapabilitySessionResolver struct {
	SessionRoot string
	Store       CapabilitySessionStore
}

func (r CapabilitySessionResolver) ResolveCapabilitySession(ctx context.Context, token string) (capproxy.SessionBinding, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return capproxy.SessionBinding{}, fmt.Errorf("capability session token is required")
	}
	if r.Store == nil {
		return capproxy.SessionBinding{}, fmt.Errorf("capability session store is required")
	}
	entries, err := os.ReadDir(r.SessionRoot)
	if err != nil {
		return capproxy.SessionBinding{}, fmt.Errorf("read session root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		session, err := r.Store.GetSession(ctx, entry.Name())
		if err != nil {
			continue
		}
		if sessionCapabilityToken(session) != token {
			continue
		}
		if session.Summary.VMStatus != sessiondomain.VMStatusRunning {
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

func sessionCapabilityToken(session *sessiondomain.Session) string {
	return domaincap.SessionEnvValue(sessionEnv(session), domaincap.SessionTokenEnvName)
}

func sessionCapabilityCapsets(session *sessiondomain.Session) []string {
	if session == nil {
		return nil
	}
	tags := make([]domaincap.SessionTag, 0, len(session.Summary.Tags))
	for _, tag := range session.Summary.Tags {
		tags = append(tags, domaincap.SessionTag{Name: tag.Name, Value: tag.Value})
	}
	return domaincap.SessionCapabilityCapsets(tags)
}

func sessionEnv(session *sessiondomain.Session) []domaincap.SessionEnvVar {
	if session == nil {
		return nil
	}
	env := make([]domaincap.SessionEnvVar, 0, len(session.EnvItems))
	for _, item := range session.EnvItems {
		env = append(env, domaincap.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return env
}
