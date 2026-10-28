package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 5 * time.Second

type Client struct {
	addr       string
	token      string
	scope      Scope
	httpClient *http.Client
}

func NewClient(config Config) *Client {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		addr:  strings.TrimRight(strings.TrimSpace(config.Addr), "/"),
		token: strings.TrimSpace(config.Token),
		scope: Scope{
			ProjectID: strings.TrimSpace(config.Scope.ProjectID),
			Username:  strings.TrimSpace(config.Scope.Username),
		},
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.addr != ""
}

func (c *Client) Status(ctx context.Context) Status {
	if !c.Configured() {
		return Status{Configured: false, OK: false, Status: "not_configured"}
	}
	var body octobusStatusResponse
	if err := c.getJSON(ctx, "/admin/v1/status", &body); err != nil {
		return Status{Configured: true, OK: false, Status: "error", Error: err.Error()}
	}
	status := body.Status
	if status == "" {
		status = "ok"
	}
	return Status{Configured: true, OK: true, Status: status, ServiceCount: uint32(body.Services)}
}

func (c *Client) ListCapsets(ctx context.Context) ([]Capset, error) {
	if !c.Configured() {
		return []Capset{}, nil
	}
	var body octobusCapsetsResponse
	if err := c.getJSON(ctx, "/admin/v1/capsets", &body); err != nil {
		return nil, err
	}
	out := make([]Capset, 0, len(body.Capsets))
	for _, item := range body.Capsets {
		out = append(out, Capset(item))
	}
	return out, nil
}

func (c *Client) Catalog(ctx context.Context, capsetID string) (Catalog, error) {
	if !c.Configured() {
		return Catalog{}, ErrNotConfigured
	}
	capsetID = strings.TrimSpace(capsetID)
	if capsetID == "" {
		return Catalog{}, fmt.Errorf("%w: capset_id is required", ErrInvalidCatalog)
	}
	var raw octobusCatalogResponse
	path := "/admin/v1/catalog/" + url.PathEscape(capsetID) + "?all=true"
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return Catalog{}, err
	}
	return NormalizeCatalog(raw)
}

// CatalogMarkdown renders the gRPC capability guide for a capset as markdown,
// used as the injected guest CAPABILITIES.md.
func (c *Client) CatalogMarkdown(ctx context.Context, capsetID string) ([]byte, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	capsetID = strings.TrimSpace(capsetID)
	if capsetID == "" {
		return nil, fmt.Errorf("%w: capset_id is required", ErrInvalidCatalog)
	}
	path := "/admin/v1/catalog/" + url.PathEscape(capsetID) + "?format=md&grpc=true"
	req, err := c.newRequest(ctx, path, "text/markdown")
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, octobusHTTPError(resp.StatusCode, body)
	}
	return body, nil
}

// newRequest builds a directory-read request with the configured credential
// and, when set, the project and user context headers. A zero Scope adds no
// headers at all, so existing deployments send exactly what they sent before.
func (c *Client) newRequest(ctx context.Context, path, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	setScopeHeader(req.Header, ProjectScopeHeader, c.scope.ProjectID)
	setScopeHeader(req.Header, ExtUsernameHeader, c.scope.Username)
	return req, nil
}

// setScopeHeader sets a context header unless the value is missing or is not a
// valid HTTP field value. Skipping an invalid value degrades to the gateway's
// project-owner fallback instead of failing guide rendering.
func setScopeHeader(header http.Header, name, value string) {
	if value == "" || !validHeaderFieldValue(value) {
		return
	}
	header.Set(name, value)
}

// validHeaderFieldValue reports whether value contains only bytes that are safe
// to send in an HTTP field value: printable ASCII (0x20-0x7e) and obs-text
// (0x80-0xff). Control bytes and DEL are rejected so setScopeHeader can skip
// the value instead of letting net/http fail the whole request.
func validHeaderFieldValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if b := value[i]; b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	req, err := c.newRequest(ctx, path, "application/json")
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return octobusHTTPError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

var (
	ErrNotConfigured  = errors.New("octobus is not configured")
	ErrInvalidCatalog = errors.New("invalid capability catalog")
)
