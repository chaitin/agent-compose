package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// conversationLabel is the run label carrying a conversation's identity. It is
// how a conversation is found again from another process.
const conversationLabel = "chat.conversation"

// defaultUserAgent identifies this client to the daemon.
const defaultUserAgent = "agent-compose-chat-go/0.1"

// TokenSource resolves a bearer token for one request. It is called per
// request, so it can refresh a short-lived credential.
type TokenSource func(context.Context) (string, error)

// StaticToken returns a TokenSource that always yields token.
func StaticToken(token string) TokenSource {
	return func(context.Context) (string, error) { return token, nil }
}

// Config configures a [Client].
type Config struct {
	// BaseURL is the daemon's HTTP address, such as "http://127.0.0.1:7410".
	BaseURL string
	// Token authenticates each request. It may be nil for an unauthenticated
	// daemon.
	Token TokenSource
	// HTTPClient replaces the default. It must reach the daemon over HTTP/2,
	// which conversations require; the default handles both h2c and TLS.
	HTTPClient *http.Client
	// UserAgent is appended to this package's own User-Agent.
	UserAgent string
}

// Client connects to one agent-compose daemon. It is safe for concurrent use.
type Client struct {
	transport *transport
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	raw := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" {
		return nil, invalidArgument("New", "base URL %q is not an absolute HTTP URL", cfg.BaseURL)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, invalidArgument("New", "base URL scheme must be http or https, got %q", base.Scheme)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = defaultHTTPClient(base)
	}
	agent := defaultUserAgent
	if extra := strings.TrimSpace(cfg.UserAgent); extra != "" {
		agent += " " + extra
	}
	return &Client{transport: newTransport(raw, client, cfg.Token, agent)}, nil
}

// Agent returns a handle for conversing with one Agent of one Project.
func (c *Client) Agent(projectID, agentName string) *Agent {
	return &Agent{
		client:    c,
		projectID: strings.TrimSpace(projectID),
		name:      strings.TrimSpace(agentName),
	}
}

// Agent is one conversational counterpart. It is safe for concurrent use.
type Agent struct {
	client    *Client
	projectID string
	name      string
}

// ProjectID reports the Project this Agent belongs to.
func (a *Agent) ProjectID() string { return a.projectID }

// Name reports the Agent's name.
func (a *Agent) Name() string { return a.name }

// Option adjusts how a conversation is created or resumed.
type Option func(*options)

type options struct {
	id            string
	labels        map[string]string
	restartIfGone bool
}

// WithID sets the conversation's identity instead of generating one. Pass a
// value the product already owns, such as its own thread ID.
func WithID(id string) Option {
	return func(o *options) { o.id = strings.TrimSpace(id) }
}

// WithLabels attaches additional labels, which [Agent.Open] and the daemon's
// own tooling can filter on. The key naming this package uses for identity is
// reserved and ignored here.
func WithLabels(labels map[string]string) Option {
	return func(o *options) {
		o.labels = maps.Clone(labels)
		delete(o.labels, conversationLabel)
	}
}

// WithRestartIfGone controls what [Agent.Open] does when the conversation's
// environment no longer exists. The default rebuilds it and reports
// [Restarted]; false makes Open return [ErrNotFound] instead.
func WithRestartIfGone(restart bool) Option {
	return func(o *options) { o.restartIfGone = restart }
}

func newOptions(opts []Option) *options {
	resolved := &options{restartIfGone: true}
	for _, opt := range opts {
		if opt != nil {
			opt(resolved)
		}
	}
	if resolved.id == "" {
		resolved.id = newConversationID()
	}
	return resolved
}

// newConversationID returns an identifier unlikely to collide with one another
// product chose, since the daemon's label space is shared.
func newConversationID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "conv_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "conv_" + hex.EncodeToString(raw[:])
}
