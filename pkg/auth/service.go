package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"agent-compose/pkg/identity"
)

var (
	tokenNamePattern               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	issuedTokenPattern             = regexp.MustCompile(`^ac_[A-Za-z0-9_-]{43}$`)
	authorizationCredentialPattern = regexp.MustCompile(`(?i)\bauthorization:[ \t]*(?:"[^"\r\n]*"|'[^'\r\n]*'|\[[^\r\n]*\]|[^,;\r\n()\[\]{}]+)`)
	bearerCredentialPattern        = regexp.MustCompile(`(?i)\bbearer[ \t]+(?:"[^"\r\n]*"|'[^'\r\n]*'|\[[^\r\n]*\]|[^[:space:],;'"()\[\]{}]+)`)
)

type Service struct {
	store       Store
	initialized atomic.Bool
	now         func() time.Time
	requests    requestRegistry
}

func NewService(ctx context.Context, store Store, environmentToken string) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("auth store is required")
	}
	service := &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
	if _, _, err := store.ReconcileEnvironmentToken(ctx, strings.TrimSpace(environmentToken), service.now()); err != nil {
		return nil, fmt.Errorf("reconcile AGENT_COMPOSE_AUTH_TOKEN: %w", err)
	}
	initialized, err := store.AuthInitialized(ctx)
	if err != nil {
		return nil, fmt.Errorf("check daemon authentication state: %w", err)
	}
	service.initialized.Store(initialized)
	return service, nil
}

func (s *Service) Initialized() bool {
	return s != nil && s.initialized.Load()
}

func (s *Service) Authenticate(ctx context.Context, plaintext string) (Identity, error) {
	if s == nil || s.store == nil {
		return Identity{}, fmt.Errorf("auth service is unavailable")
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" || strings.ContainsAny(plaintext, " \t\r\n") {
		return Identity{}, ErrInvalidToken
	}
	token, err := s.store.AuthenticateTokenHash(ctx, TokenHash(plaintext))
	if err != nil {
		return Identity{}, err
	}
	return Identity{TokenID: token.ID, TokenName: token.Name, Role: token.Role, Origin: token.Origin}, nil
}

func (s *Service) CreateToken(ctx context.Context, actor Identity, input CreateTokenInput, requestID string) (Token, bool, bool, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Plaintext = strings.TrimSpace(input.Plaintext)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	if actor.Role != RoleAdmin {
		return Token{}, false, false, ErrPermissionDenied
	}
	if !tokenNamePattern.MatchString(input.Name) || strings.EqualFold(input.Name, DefaultAdminName) {
		return Token{}, false, false, fmt.Errorf("%w: token name must match %s and must not be %q", ErrInvalidToken, tokenNamePattern.String(), DefaultAdminName)
	}
	if utf8.RuneCountInString(input.Description) > 512 {
		return Token{}, false, false, fmt.Errorf("%w: token description exceeds 512 characters", ErrInvalidToken)
	}
	if input.Role != RoleAdmin && input.Role != RoleReadOnlyAdmin {
		return Token{}, false, false, fmt.Errorf("%w: unsupported role %q", ErrInvalidToken, input.Role)
	}
	if !issuedTokenPattern.MatchString(input.Plaintext) {
		return Token{}, false, false, fmt.Errorf("%w: client-generated token has an invalid format", ErrInvalidToken)
	}
	if input.ClientRequestID == "" || len(input.ClientRequestID) > 128 || strings.ContainsAny(input.ClientRequestID, " \t\r\n") {
		return Token{}, false, false, fmt.Errorf("%w: client_request_id is required and must not contain whitespace", ErrInvalidToken)
	}
	params := marshalAuditJSON(map[string]any{
		"name": input.Name, "description": input.Description, "role": input.Role,
		"client_request_id": input.ClientRequestID,
	})
	item, created, replay, err := s.store.CreateToken(ctx, actor, input, normalizeRequestID(requestID), params, s.now())
	if err == nil {
		s.initialized.Store(true)
	}
	return item, created, replay, err
}

func (s *Service) RevokeToken(ctx context.Context, actor Identity, ref, requestID string) (Token, bool, bool, error) {
	if actor.Role != RoleAdmin {
		return Token{}, false, false, ErrPermissionDenied
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Token{}, false, false, fmt.Errorf("%w: token id or name is required", ErrInvalidToken)
	}
	params := marshalAuditJSON(map[string]any{"token": ref})
	item, revoked, alreadyRevoked, err := s.store.RevokeToken(ctx, actor, ref, normalizeRequestID(requestID), params, s.now())
	if err == nil && revoked {
		s.requests.cancelToken(item.ID)
	}
	return item, revoked, alreadyRevoked, err
}

func (s *Service) ListTokens(ctx context.Context, options TokenListOptions) (TokenListResult, error) {
	return s.store.ListTokens(ctx, options)
}

func (s *Service) ListAudits(ctx context.Context, filter AuditFilter) (AuditListResult, error) {
	return s.store.ListAudits(ctx, filter)
}

func (s *Service) BeginAudit(ctx context.Context, actor Identity, requestID, action string, resource Resource, paramsJSON string) (Audit, error) {
	now := s.now()
	audit := Audit{
		ID: identity.NewRandomID(identity.ResourceAudit), RequestID: normalizeRequestID(requestID),
		TokenID: actor.TokenID, TokenName: actor.TokenName, Role: actor.Role, Origin: actor.Origin,
		Action: strings.TrimSpace(action), Resource: resource, Status: AuditStatusStarted,
		ParamsJSON: normalizeJSONObject(paramsJSON), ChangesJSON: "{}", StartedAt: now,
	}
	if err := s.store.BeginAudit(ctx, audit); err != nil {
		return Audit{}, err
	}
	return audit, nil
}

func (s *Service) DenyAudit(ctx context.Context, actor Identity, requestID, action string) error {
	now := s.now()
	return s.store.BeginAudit(ctx, Audit{
		ID: identity.NewRandomID(identity.ResourceAudit), RequestID: normalizeRequestID(requestID),
		TokenID: actor.TokenID, TokenName: actor.TokenName, Role: actor.Role, Origin: actor.Origin,
		Action: strings.TrimSpace(action), Status: AuditStatusDenied, ParamsJSON: "{}", ChangesJSON: "{}",
		ErrorCode: "permission_denied", ErrorMessage: "operation is not permitted for this role",
		StartedAt: now, CompletedAt: now,
	})
}

func (s *Service) FinishAudit(ctx context.Context, audit Audit, status AuditStatus, resource Resource, changesJSON, errorCode, errorMessage string) error {
	audit.Status = status
	audit.Resource = resource
	audit.ChangesJSON = normalizeJSONObject(changesJSON)
	audit.ErrorCode = strings.TrimSpace(errorCode)
	audit.ErrorMessage = SanitizeError(errorMessage)
	audit.CompletedAt = s.now()
	return s.store.FinishAudit(ctx, audit)
}

func (s *Service) RegisterRequest(tokenID string, cancel context.CancelFunc) func() {
	return s.requests.register(strings.TrimSpace(tokenID), cancel)
}

func TokenHash(plaintext string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plaintext)))
	return hex.EncodeToString(sum[:])
}

func TokenFingerprint(plaintext string) string {
	hash := TokenHash(plaintext)
	if len(hash) < 12 {
		return hash
	}
	return hash[:12]
}

func ValidRole(role Role) bool { return role == RoleAdmin || role == RoleReadOnlyAdmin }

func Allowed(role Role, access Access) bool {
	return role == RoleAdmin || role == RoleReadOnlyAdmin && access == AccessRead
}

func normalizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n") {
		return value
	}
	return identity.NewRandomID(identity.ResourceAudit)
}

func marshalAuditJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 16*1024 {
		return `{"truncated":true}`
	}
	return string(data)
}

func normalizeJSONObject(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 16*1024 || !json.Valid([]byte(value)) {
		return "{}"
	}
	return value
}

func SanitizeError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = authorizationCredentialPattern.ReplaceAllString(value, "Authorization: [REDACTED]")
	value = bearerCredentialPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	if len(value) > 2048 {
		value = value[:2048] + "…"
	}
	return value
}

type requestRegistry struct {
	mu     sync.Mutex
	nextID uint64
	items  map[string]map[uint64]context.CancelFunc
}

func (r *requestRegistry) register(tokenID string, cancel context.CancelFunc) func() {
	if tokenID == "" || cancel == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.items == nil {
		r.items = make(map[string]map[uint64]context.CancelFunc)
	}
	r.nextID++
	id := r.nextID
	if r.items[tokenID] == nil {
		r.items[tokenID] = make(map[uint64]context.CancelFunc)
	}
	r.items[tokenID][id] = cancel
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.items[tokenID], id)
		if len(r.items[tokenID]) == 0 {
			delete(r.items, tokenID)
		}
		r.mu.Unlock()
	}
}

func (r *requestRegistry) cancelToken(tokenID string) {
	r.mu.Lock()
	items := r.items[tokenID]
	delete(r.items, tokenID)
	r.mu.Unlock()
	for _, cancel := range items {
		cancel()
	}
}
