package chat

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// transport carries this package's calls to one daemon. The wire format,
// framing and procedure paths belong to the generated Connect client; what is
// left here is the per-request credential and the naming of failures.
type transport struct {
	runs     agentcomposev2connect.RunServiceClient
	projects agentcomposev2connect.ProjectServiceClient
}

func newTransport(baseURL string, client *http.Client, token TokenSource, userAgent string) *transport {
	identify := connect.WithInterceptors(&credentials{token: token, userAgent: userAgent})
	return &transport{
		runs:     agentcomposev2connect.NewRunServiceClient(client, baseURL, identify),
		projects: agentcomposev2connect.NewProjectServiceClient(client, baseURL, identify),
	}
}

// credentials puts the caller's identity on every request, unary and streaming
// alike. The token is resolved per request rather than once, so a short-lived
// credential can be refreshed underneath a long conversation.
type credentials struct {
	token     TokenSource
	userAgent string
}

func (c *credentials) apply(ctx context.Context, header http.Header) error {
	header.Set("User-Agent", c.userAgent)
	if c.token == nil {
		return nil
	}
	token, err := c.token(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	if token = strings.TrimSpace(token); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

func (c *credentials) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if err := c.apply(ctx, request.Header()); err != nil {
			return nil, err
		}
		return next(ctx, request)
	}
}

func (c *credentials) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		// A failure here cannot be returned: the conn is the only value this
		// hook may yield. Leaving the header unset makes the daemon reject the
		// stream, which surfaces as an authentication error on the first
		// receive rather than being swallowed.
		_ = c.apply(ctx, conn.RequestHeader())
		return conn
	}
}

func (c *credentials) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// attachStream is one live conversation with the daemon.
type attachStream = connect.BidiStreamForClient[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]

// defaultHTTPClient returns a client able to carry Connect bidirectional
// streams to baseURL: plaintext bases need h2c, since Go's standard transport
// only negotiates HTTP/2 over TLS.
func defaultHTTPClient(base *url.URL) *http.Client {
	if base.Scheme == "https" {
		return &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true}}
	}
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}
