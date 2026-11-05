package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// egressProxyCredentialSource marks the credential the engine issues for its
// forward proxy. The credential is stored with the sandbox's other facade
// tokens, so it is revoked with the sandbox, but only a credential carrying this
// source authenticates a proxy request: the sandbox's LLM facade token is a
// different authorization and is not accepted in its place.
const egressProxyCredentialSource = "egress-proxy"

// EgressProxyCredentialStore is the sandbox-scoped credential store the proxy
// credential is issued into. It is already the store the daemon revokes per
// sandbox, so a retired sandbox cannot keep proxy access.
type EgressProxyCredentialStore interface {
	SaveLLMFacadeToken(context.Context, llms.FacadeToken) error
	GetLLMFacadeToken(context.Context, string) (llms.FacadeToken, error)
}

// SandboxByIDLoader loads a sandbox so the proxy can decide against the
// declaration that sandbox has *now*, not the one it had when it started.
type SandboxByIDLoader interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
}

// SandboxEgressProxy is the engine-side mediation between a sandbox and the
// internet: it issues the credential the guest presents to the forward proxy,
// builds the environment that routes the guest through it, answers the proxy's
// authentication, and hands the proxy the policy to decide with. It is what the
// daemon passes to the proxy as both its authorizer and its policy source.
//
// Its zero value is inert, and it is inert whenever the deployment cannot offer
// a proxy the sandbox can reach (no configured runtime base URL, or a daemon
// listening only on the host loopback while the sandbox runs in its own network
// namespace). An inert value adds no environment and issues no credential, which
// is what keeps an undeclared or unenforceable sandbox on its existing behavior.
type SandboxEgressProxy struct {
	config          *appconfig.Config
	credentials     EgressProxyCredentialStore
	sandboxes       SandboxByIDLoader
	networkPolicies driverSandboxNetworkPolicy
	// revisions is the process-local read-through cache of the persisted policy
	// revision. It is a pointer because the mediation is copied by value into
	// the runtime provider and the proxy; the copies must agree about what they
	// have already observed.
	revisions *egressPolicyRevisionCache
}

// NewSandboxEgressProxy builds the mediation from the daemon's own dependencies.
// It is pure: whether a sandbox actually gets a proxy is decided per sandbox by
// available, because reachability depends on that sandbox's address.
func NewSandboxEgressProxy(
	config *appconfig.Config,
	credentials EgressProxyCredentialStore,
	sandboxes SandboxByIDLoader,
	declarations SandboxNetworkDeclarationResolver,
) SandboxEgressProxy {
	return SandboxEgressProxy{
		config:          config,
		credentials:     credentials,
		sandboxes:       sandboxes,
		networkPolicies: driverSandboxNetworkPolicy{config: config, declarations: declarations},
		revisions:       newEgressPolicyRevisionCache(),
	}
}

// available reports whether this deployment can route a sandbox through the
// proxy. It is the single condition that decides whether the engine may claim
// mediation, so the environment, the credential, and the enforcement report
// cannot disagree.
func (p SandboxEgressProxy) available(session *domain.Sandbox) bool {
	if p.credentials == nil || p.sandboxes == nil || session == nil {
		return false
	}
	if strings.TrimSpace(session.Summary.ID) == "" {
		return false
	}
	return egressProxyAddress(p.config, session) != ""
}

// MediatesDriver reports whether a sandbox on driver would be routed through the
// engine's forward proxy. It answers the same address-resolution question
// available asks, with the per-sandbox inputs an admission report cannot know
// yet (the sandbox ID and the environment a project declares for it), so an
// admission-time report cannot claim mediation the creation seam would not set.
func (p SandboxEgressProxy) MediatesDriver(driver string) bool {
	if p.credentials == nil || p.sandboxes == nil {
		return false
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{Driver: driverpkg.ResolveRuntimeDriver(driver)}}
	return egressProxyAddress(p.config, session) != ""
}

// enable issues the sandbox's proxy credential and returns the environment that
// routes its HTTP traffic through the engine's forward proxy. It returns no
// environment and no error when this deployment cannot offer a reachable proxy,
// because a sandbox that cannot use the proxy must keep running rather than be
// given a proxy address it cannot reach.
//
// It is not called when a sandbox is handed to a driver, because a guest reads
// its environment once, when its runtime is created. It is wrapped by
// proxyEnvProvider and invoked from the driver's creation path, so exactly one
// credential is issued per runtime creation instead of one per ensure.
//
// A credential is minted per call rather than reused. The store keeps only the
// credential's hash, which is what makes a leaked store entry useless, so an
// issued credential cannot be read back to hand out again; and a start that
// reuses a runtime keeps the environment it was created with, so revoking this
// sandbox's older credentials here could cut off the one that runtime is
// actually presenting. Each credential is scoped to this sandbox and revoked
// with it.
func (p SandboxEgressProxy) enable(ctx context.Context, session *domain.Sandbox) ([]driverpkg.SandboxEnvVar, error) {
	if !p.available(session) {
		return nil, nil
	}
	rawCredential, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
		SandboxID: session.Summary.ID,
		Source:    egressProxyCredentialSource,
	})
	if err != nil {
		return nil, fmt.Errorf("mint egress proxy credential for sandbox %s: %w", session.Summary.ID, err)
	}
	if err := p.credentials.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, fmt.Errorf("store egress proxy credential for sandbox %s: %w", session.Summary.ID, err)
	}
	proxyURL := (&url.URL{
		Scheme: "http",
		Host:   egressProxyAddress(p.config, session),
		User:   url.UserPassword(session.Summary.ID, rawCredential),
	}).String()
	items := egressProxyEnvItems(proxyURL, egressProxyBypass(p.config, session))
	items = append(items, egressL7CAEnvItems(p.config)...)
	return items, nil
}

// proxyEnvProvider returns the driver-boundary hook that issues one sandbox's
// proxy credential at the moment a driver builds the environment of a runtime
// it is creating. A driver that reuses a runtime never calls it, so a start
// that reuses a runtime neither mints nor drops a credential.
func (p SandboxEgressProxy) proxyEnvProvider(session *domain.Sandbox) func(context.Context) ([]driverpkg.SandboxEnvVar, error) {
	return func(ctx context.Context) ([]driverpkg.SandboxEnvVar, error) {
		return p.enable(ctx, session)
	}
}

// AuthorizeEgressProxy answers the proxy's authentication: which sandbox does
// this credential belong to. It rejects a credential that was not issued for the
// proxy, one that has been revoked or expired, and one presented under a
// different sandbox's name.
func (p SandboxEgressProxy) AuthorizeEgressProxy(ctx context.Context, claimedSandboxID, credential string) (string, error) {
	if p.credentials == nil {
		return "", errors.New("the egress proxy credential store is not configured")
	}
	if strings.TrimSpace(credential) == "" {
		return "", errors.New("no egress proxy credential was presented")
	}
	token, err := p.credentials.GetLLMFacadeToken(ctx, credential)
	if err != nil {
		return "", fmt.Errorf("resolve egress proxy credential: %w", err)
	}
	if token.Source != egressProxyCredentialSource {
		return "", fmt.Errorf("credential source %q is not %q", token.Source, egressProxyCredentialSource)
	}
	if strings.TrimSpace(token.SandboxID) == "" {
		return "", errors.New("egress proxy credential names no sandbox")
	}
	if !token.RevokedAt.IsZero() {
		return "", errors.New("egress proxy credential was revoked")
	}
	if !token.ExpiresAt.IsZero() && !time.Now().UTC().Before(token.ExpiresAt.UTC()) {
		return "", errors.New("egress proxy credential expired")
	}
	claimed := strings.TrimSpace(claimedSandboxID)
	if claimed != "" && claimed != token.SandboxID {
		return "", errors.New("egress proxy credential belongs to another sandbox")
	}
	return token.SandboxID, nil
}

// EgressProxyPolicy returns the policy the proxy decides one sandbox's
// connections with. It loads the sandbox per request so the decision follows the
// declaration the project has now, which is what lets a policy change reach a
// sandbox that is already running.
func (p SandboxEgressProxy) EgressProxyPolicy(ctx context.Context, sandboxID string) (egress.Policy, error) {
	if p.sandboxes == nil {
		return egress.Policy{}, errors.New("the egress proxy cannot load sandboxes")
	}
	session, err := p.sandboxes.GetSandbox(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return egress.Policy{}, fmt.Errorf("load sandbox %s for the egress decision: %w", sandboxID, err)
	}
	policy, err := p.policyForSession(ctx, session)
	if err != nil {
		return egress.Policy{}, err
	}
	// The proxy is the enforcement point for a mediated sandbox, so reading the
	// policy here is what acknowledges the revision. A failure to persist the
	// acknowledgement is reported and does not change the decision: the policy
	// is still the one this request is evaluated against.
	if _, err := p.observePolicyRevision(ctx, session, policy, true); err != nil {
		slog.WarnContext(ctx, "egress policy revision was not acknowledged by the proxy",
			"sandbox_id", sandboxID, "error", err)
	}
	return policy, nil
}

// policyForSession compiles the policy the proxy would decide this sandbox's
// traffic with, given a session it already holds.
func (p SandboxEgressProxy) policyForSession(ctx context.Context, session *domain.Sandbox) (egress.Policy, error) {
	return p.networkPolicies.effectivePolicy(ctx, session)
}

// egressProxyAddress is the host:port the sandbox must use to reach the proxy:
// the same guest-reachable address the engine hands the sandbox for its runtime
// LLM facade, because that is the address the engine already knows works from
// inside the sandbox. An empty result means no proxy is reachable.
//
// The guest-reachable address must not be a loopback or unspecified one. Every
// sandbox this daemon runs is a container, a microVM, or a Pod with its own
// network namespace, so the host's loopback is its own loopback: handing it to
// the guest would set a proxy address that cannot work, skip the unenforced
// gate, and report a mediation that does not exist. An operator that needs the
// proxy has to give the daemon an address the sandbox can actually dial
// (AGENT_COMPOSE_RUNTIME_BASE_URL), which is also what the LLM facade needs.
func egressProxyAddress(config *appconfig.Config, session *domain.Sandbox) string {
	base := strings.TrimSpace(llms.GuestRuntimeBaseURL(config, session))
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if egressProxyHostIsGuestLocal(parsed.Hostname()) {
		return ""
	}
	return parsed.Host
}

// egressProxyHostIsGuestLocal reports whether a host names the guest's own
// loopback rather than the host the daemon runs on.
func egressProxyHostIsGuestLocal(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" || host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	return address.IsLoopback() || address.IsUnspecified()
}

// egressProxyBypass lists the hosts the guest must not send through the proxy:
// the engine's own endpoints, which the policy auto-allows anyway, so routing
// them through the proxy would add a hop and a failure mode for no decision.
func egressProxyBypass(config *appconfig.Config, session *domain.Sandbox) []string {
	hosts := []string{"127.0.0.1", "localhost"}
	if address := egressProxyAddress(config, session); address != "" {
		if host, _, err := net.SplitHostPort(address); err == nil {
			hosts = append(hosts, host)
		}
	}
	if config != nil {
		if telemetry := strings.TrimSpace(config.AgentTelemetry.Endpoint); telemetry != "" {
			if parsed, err := url.Parse(telemetry); err == nil && parsed.Hostname() != "" {
				hosts = append(hosts, parsed.Hostname())
			}
		}
	}
	return hosts
}

// egressProxyEnvItems renders the proxy environment. Both cases are set because
// clients disagree about which they read, and the value is marked secret so a
// credential embedded in it is never echoed by a listing or a log.
func egressProxyEnvItems(proxyURL string, bypass []string) []driverpkg.SandboxEnvVar {
	noProxy := strings.Join(bypass, ",")
	items := []driverpkg.SandboxEnvVar{
		{Name: "HTTP_PROXY", Value: proxyURL, Secret: true},
		{Name: "HTTPS_PROXY", Value: proxyURL, Secret: true},
		{Name: "NO_PROXY", Value: noProxy, Secret: true},
		{Name: "http_proxy", Value: proxyURL, Secret: true},
		{Name: "https_proxy", Value: proxyURL, Secret: true},
		{Name: "no_proxy", Value: noProxy, Secret: true},
	}
	return items
}

// egressL7CAEnvItems names the guest CA bundle the sandbox must trust for the
// intercepted TLS handshake. The engine cannot install the certificate file in
// the guest from here, so the deployment must place it at the configured guest
// path (an image layer or a mounted volume); the variables point every common
// HTTP client at that file. Without the path configured no variables are added,
// which keeps interception deployments that only rely on the proxy environment
// unchanged.
func egressL7CAEnvItems(config *appconfig.Config) []driverpkg.SandboxEnvVar {
	if config == nil || !config.EgressL7TLS.MITM {
		return nil
	}
	path := strings.TrimSpace(config.EgressL7TLS.CABundleGuestPath)
	if path == "" {
		return nil
	}
	// The certificate is public material, so these values are not marked
	// secret; the path itself is not sensitive either.
	return []driverpkg.SandboxEnvVar{
		{Name: "SSL_CERT_FILE", Value: path},
		{Name: "CURL_CA_BUNDLE", Value: path},
		{Name: "REQUESTS_CA_BUNDLE", Value: path},
		{Name: "NODE_EXTRA_CA_CERTS", Value: path},
		{Name: "GIT_SSL_CAINFO", Value: path},
	}
}

// EgressProxyPolicyForSession is the per-run policy hook. No per-run policy
// store exists yet, so it returns the sandbox-wide policy; the proxy calls this
// only when a session identity is present, which makes the future per-run
// storage a change here instead of a change in the proxy.
func (p *SandboxEgressProxy) EgressProxyPolicyForSession(ctx context.Context, sandboxID, sessionID, marker string) (egress.Policy, error) {
	return p.EgressProxyPolicy(ctx, sandboxID)
}
