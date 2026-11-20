package adapters

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capproxy"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type CapabilitySandboxStore interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
}

type CapabilitySandboxResolver struct {
	store CapabilitySandboxStore

	mu              sync.RWMutex
	initialized     bool
	tokens          map[string]capproxy.SandboxBinding
	tokensBySandbox map[string]map[string]struct{}
}

func NewCapabilitySandboxResolver(store CapabilitySandboxStore) *CapabilitySandboxResolver {
	return &CapabilitySandboxResolver{
		store:           store,
		tokens:          map[string]capproxy.SandboxBinding{},
		tokensBySandbox: map[string]map[string]struct{}{},
	}
}

func (r *CapabilitySandboxResolver) Rebuild(ctx context.Context) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("capability sandbox store is required")
	}
	tokens := map[string]capproxy.SandboxBinding{}
	tokensBySandbox := map[string]map[string]struct{}{}
	offset := 0
	const pageSize = 200
	for {
		result, err := r.store.ListSandboxes(ctx, domain.SandboxListOptions{Offset: offset, Limit: pageSize})
		if err != nil {
			return err
		}
		for _, sandbox := range result.Sandboxes {
			indexCapabilitySandbox(tokens, tokensBySandbox, sandbox, nil)
		}
		if !result.HasMore {
			break
		}
		offset = result.NextOffset
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = tokens
	r.tokensBySandbox = tokensBySandbox
	r.initialized = true
	return nil
}

// IndexSandbox replaces a sandbox binding with request-scoped trusted headers.
// The headers live only in this resolver and are lost on rebuild or daemon
// restart. Passing nil explicitly clears headers from an earlier binding.
func (r *CapabilitySandboxResolver) IndexSandbox(sandbox *domain.Sandbox, headers []domain.TrustedHeader) {
	if r == nil || sandbox == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMapsLocked()
	r.revokeSandboxLocked(sandbox.Summary.ID)
	indexCapabilitySandbox(r.tokens, r.tokensBySandbox, sandbox, headers)
}

// TrustedHeadersConflict reports whether the sandbox's capability token is
// bound to trusted headers other than headers. A sandbox without a binding has
// no identity to conflict with.
func (r *CapabilitySandboxResolver) TrustedHeadersConflict(sandboxID string, headers []domain.TrustedHeader) bool {
	if r == nil {
		return false
	}
	candidate := translateTrustedHeaders(headers)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for token := range r.tokensBySandbox[strings.TrimSpace(sandboxID)] {
		if !slices.Equal(r.tokens[token].TrustedHeaders, candidate) {
			return true
		}
	}
	return false
}

func (r *CapabilitySandboxResolver) RevokeSandbox(sandboxID string) {
	if r == nil {
		return
	}
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMapsLocked()
	r.revokeSandboxLocked(sandboxID)
}

func (r *CapabilitySandboxResolver) ResolveCapabilitySandbox(ctx context.Context, token string) (capproxy.SandboxBinding, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return capproxy.SandboxBinding{}, fmt.Errorf("capability sandbox token is required")
	}
	if r == nil || r.store == nil {
		return capproxy.SandboxBinding{}, fmt.Errorf("capability sandbox store is required")
	}
	if err := r.ensureInitialized(ctx); err != nil {
		return capproxy.SandboxBinding{}, err
	}
	r.mu.RLock()
	binding, ok := r.tokens[token]
	r.mu.RUnlock()
	if !ok {
		return capproxy.SandboxBinding{}, domain.ClassifyError(domain.ErrNotFound, "capability sandbox token not found", nil)
	}
	return binding, nil
}

func (r *CapabilitySandboxResolver) ensureInitialized(ctx context.Context) error {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()
	if initialized {
		return nil
	}
	return r.Rebuild(ctx)
}

func (r *CapabilitySandboxResolver) ensureMapsLocked() {
	if r.tokens == nil {
		r.tokens = map[string]capproxy.SandboxBinding{}
	}
	if r.tokensBySandbox == nil {
		r.tokensBySandbox = map[string]map[string]struct{}{}
	}
}

func (r *CapabilitySandboxResolver) revokeSandboxLocked(sandboxID string) {
	tokenSet := r.tokensBySandbox[sandboxID]
	for token := range tokenSet {
		delete(r.tokens, token)
	}
	delete(r.tokensBySandbox, sandboxID)
}

func indexCapabilitySandbox(tokens map[string]capproxy.SandboxBinding, tokensBySandbox map[string]map[string]struct{}, sandbox *domain.Sandbox, headers []domain.TrustedHeader) {
	if sandbox == nil || sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return
	}
	token := capabilities.SandboxToken(sandbox)
	if token == "" {
		return
	}
	capsetIDs := capabilities.SandboxCapsets(sandbox)
	if len(capsetIDs) == 0 {
		return
	}
	scope := capabilities.GuideScopeFromSandbox(sandbox)
	binding := capproxy.SandboxBinding{
		SandboxID:      sandbox.Summary.ID,
		ProjectID:      scope.ProjectID,
		AgentID:        scope.AgentID,
		CapsetIDs:      capsetIDs,
		TrustedHeaders: translateTrustedHeaders(headers),
	}
	tokens[token] = binding
	tokenSet := tokensBySandbox[sandbox.Summary.ID]
	if tokenSet == nil {
		tokenSet = map[string]struct{}{}
		tokensBySandbox[sandbox.Summary.ID] = tokenSet
	}
	tokenSet[token] = struct{}{}
}

const (
	xMPIPrefix        = "x-mpi-"
	xOctobusExtPrefix = "x-octobus-ext-"
)

func translateTrustedHeaders(headers []domain.TrustedHeader) []domain.TrustedHeader {
	var out []domain.TrustedHeader
	for _, header := range headers {
		name := strings.ToLower(header.Name)
		if !strings.HasPrefix(name, xMPIPrefix) {
			continue
		}
		key := xOctobusExtPrefix + strings.TrimPrefix(name, xMPIPrefix)
		out = append(out, domain.TrustedHeader{Name: key, Value: header.Value})
	}
	return out
}
