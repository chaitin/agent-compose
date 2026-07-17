// Package auth owns daemon control-plane token authentication, role
// authorization, token lifecycle, and operation audit records.
package auth

import (
	"context"
	"errors"
	"time"
)

type Role string

const (
	RoleAdmin         Role = "admin"
	RoleReadOnlyAdmin Role = "read-only-admin"
)

type Access string

const (
	AccessRead      Access = "read"
	AccessOperation Access = "operation"
)

const (
	OriginEnvironment = "environment"
	OriginIssued      = "issued"
	OriginLocal       = "local"
	OriginAnonymous   = "anonymous"
	OriginSystem      = "system"

	DefaultAdminName = "default-admin"
)

var (
	ErrInvalidToken        = errors.New("invalid daemon token")
	ErrPermissionDenied    = errors.New("daemon operation is not permitted")
	ErrPolicyMissing       = errors.New("daemon operation policy is missing")
	ErrTokenNotFound       = errors.New("daemon token not found")
	ErrTokenConflict       = errors.New("daemon token conflicts with existing data")
	ErrTokenSelfRevoke     = errors.New("a token cannot revoke itself")
	ErrEnvironmentToken    = errors.New("environment-managed token cannot be revoked")
	ErrLastAdminToken      = errors.New("last active admin token cannot be revoked remotely")
	ErrIdempotencyConflict = errors.New("token create request conflicts with an earlier request")
)

type Token struct {
	ID               string
	Name             string
	Description      string
	Role             Role
	Origin           string
	Fingerprint      string
	CreatedByTokenID string
	ClientRequestID  string
	CreatedAt        time.Time
	RevokedByTokenID string
	RevokedAt        time.Time
}

func (t Token) Active() bool { return t.RevokedAt.IsZero() }

type Identity struct {
	TokenID   string
	TokenName string
	Role      Role
	Origin    string
}

func (i Identity) IsLocal() bool { return i.Origin == OriginLocal }

func (i Identity) TokenSummary() Token {
	return Token{ID: i.TokenID, Name: i.TokenName, Role: i.Role, Origin: i.Origin}
}

type RoleInfo struct {
	Name        Role
	Description string
	ReadOnly    bool
	Builtin     bool
}

func Roles() []RoleInfo {
	return []RoleInfo{
		{Name: RoleAdmin, Description: "Full daemon control-plane access", Builtin: true},
		{Name: RoleReadOnlyAdmin, Description: "Read-only access to all daemon resources and logs", ReadOnly: true, Builtin: true},
	}
}

type CreateTokenInput struct {
	Name            string
	Description     string
	Role            Role
	Plaintext       string
	ClientRequestID string
}

type TokenListOptions struct {
	IncludeRevoked bool
	Limit          int
	BeforeID       string
}

type TokenListResult struct {
	Tokens     []Token
	NextCursor string
}

type AuditStatus string

const (
	AuditStatusStarted   AuditStatus = "started"
	AuditStatusSucceeded AuditStatus = "succeeded"
	AuditStatusFailed    AuditStatus = "failed"
	AuditStatusDenied    AuditStatus = "denied"
)

type Resource struct {
	Type      string
	ID        string
	ProjectID string
}

type Audit struct {
	Sequence     uint64
	ID           string
	RequestID    string
	TokenID      string
	TokenName    string
	Role         Role
	Origin       string
	Action       string
	Resource     Resource
	Status       AuditStatus
	ParamsJSON   string
	ChangesJSON  string
	ErrorCode    string
	ErrorMessage string
	StartedAt    time.Time
	CompletedAt  time.Time
}

type AuditFilter struct {
	Token          string
	Action         string
	ResourceType   string
	ResourceID     string
	ProjectID      string
	Status         AuditStatus
	StartedAfter   time.Time
	StartedBefore  time.Time
	Limit          int
	BeforeSequence uint64
}

type AuditListResult struct {
	Audits     []Audit
	NextCursor string
}

type Store interface {
	ReconcileEnvironmentToken(context.Context, string, time.Time) (Token, bool, error)
	AuthInitialized(context.Context) (bool, error)
	AuthenticateTokenHash(context.Context, string) (Token, error)
	ListTokens(context.Context, TokenListOptions) (TokenListResult, error)
	CreateToken(context.Context, Identity, CreateTokenInput, string, string, time.Time) (Token, bool, bool, error)
	RevokeToken(context.Context, Identity, string, string, string, time.Time) (Token, bool, bool, error)
	BeginAudit(context.Context, Audit) error
	FinishAudit(context.Context, Audit) error
	ListAudits(context.Context, AuditFilter) (AuditListResult, error)
}
