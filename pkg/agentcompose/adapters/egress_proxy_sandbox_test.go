package adapters

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// egressProxyTestStore is the credential store seen from the boundary: it keeps
// the raw value the sandbox would present mapped to the token that was issued.
type egressProxyTestStore struct {
	tokens map[string]llms.FacadeToken
}

func newEgressProxyTestStore() *egressProxyTestStore {
	return &egressProxyTestStore{tokens: map[string]llms.FacadeToken{}}
}

func (s *egressProxyTestStore) SaveLLMFacadeToken(_ context.Context, token llms.FacadeToken) error {
	s.tokens[token.TokenHash] = token
	return nil
}

func (s *egressProxyTestStore) GetLLMFacadeToken(_ context.Context, rawToken string) (llms.FacadeToken, error) {
	hash, _ := llms.HashFacadeToken(rawToken)
	token, ok := s.tokens[hash]
	if !ok {
		return llms.FacadeToken{}, errTestCredentialNotFound
	}
	return token, nil
}

func (s *egressProxyTestStore) issue(t *testing.T, request llms.NewFacadeTokenRequest) string {
	t.Helper()
	raw, token, err := llms.NewFacadeToken(request)
	if err != nil {
		t.Fatalf("NewFacadeToken() error = %v", err)
	}
	if err := s.SaveLLMFacadeToken(context.Background(), token); err != nil {
		t.Fatalf("SaveLLMFacadeToken() error = %v", err)
	}
	return raw
}

var errTestCredentialNotFound = errTestSentinel("credential not found")

type errTestSentinel string

func (e errTestSentinel) Error() string { return string(e) }

type egressProxyTestSandboxes struct {
	session *domain.Sandbox
	err     error
}

func (s egressProxyTestSandboxes) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.session, nil
}

func denyDeclaration() *egress.NetworkDeclaration {
	return &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
}

func egressProxyTestMediation(store *egressProxyTestStore, sandboxes egressProxyTestSandboxes, declaration *egress.NetworkDeclaration) SandboxEgressProxy {
	return NewSandboxEgressProxy(
		networkPolicyTestConfig(),
		store,
		sandboxes,
		stubNetworkDeclarationResolver{declaration: declaration},
	)
}

func TestSandboxEgressProxyProviderIssuesAReachableCredential(t *testing.T) {
	store := newEgressProxyTestStore()
	session := networkPolicyTestSession()
	mediation := egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, denyDeclaration())

	env, err := mediation.proxyEnvProvider(session)(context.Background())
	if err != nil {
		t.Fatalf("proxy environment error = %v", err)
	}
	values := envValues(env)
	proxy := values["HTTPS_PROXY"]
	if proxy == "" {
		t.Fatalf("no HTTPS_PROXY in %v", env)
	}
	if !strings.HasPrefix(proxy, "http://sandbox-1:ac_llm_") {
		t.Fatalf("HTTPS_PROXY = %q, want the sandbox and its credential", proxy)
	}
	if !strings.Contains(proxy, "@"+strings.TrimPrefix(networkPolicyTestBaseURL, "http://")) {
		t.Fatalf("HTTPS_PROXY = %q, want the guest-reachable address", proxy)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if values[name] == "" {
			t.Fatalf("environment is missing %s: %v", name, values)
		}
	}
	if !strings.Contains(values["NO_PROXY"], "127.0.0.1") {
		t.Fatalf("NO_PROXY = %q, want the engine endpoints bypassed", values["NO_PROXY"])
	}
	for _, item := range env {
		if !item.Secret {
			t.Fatalf("environment item %s is not marked secret, so its credential could be echoed", item.Name)
		}
	}
	if len(store.tokens) != 1 {
		t.Fatalf("stored credentials = %d, want exactly one", len(store.tokens))
	}
	for _, token := range store.tokens {
		if token.SandboxID != "sandbox-1" || token.Source != egressProxyCredentialSource {
			t.Fatalf("stored token = %+v, want sandbox-1 with source %q", token, egressProxyCredentialSource)
		}
	}
}

// TestSandboxEgressProxyProviderIsInertWithoutAReachableAddress pins the
// condition the whole mediation rests on: if the sandbox has no address for the
// proxy, the engine must not hand it one, and no credential is issued for a proxy
// the guest cannot reach.
func TestSandboxEgressProxyProviderIsInertWithoutAReachableAddress(t *testing.T) {
	store := newEgressProxyTestStore()
	session := networkPolicyTestSession()
	mediation := NewSandboxEgressProxy(
		&appconfig.Config{HttpListen: "127.0.0.1:7410"},
		store,
		egressProxyTestSandboxes{session: session},
		stubNetworkDeclarationResolver{declaration: denyDeclaration()},
	)

	env, err := mediation.proxyEnvProvider(session)(context.Background())
	if err != nil {
		t.Fatalf("proxy environment error = %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("proxy environment = %v, want none when no proxy is reachable", env)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("credentials issued = %d, want 0", len(store.tokens))
	}
}

func TestSandboxEgressProxyAuthorizeEgressProxy(t *testing.T) {
	store := newEgressProxyTestStore()
	valid := store.issue(t, llms.NewFacadeTokenRequest{SandboxID: "sandbox-1", Source: egressProxyCredentialSource})
	revoked := store.issue(t, llms.NewFacadeTokenRequest{SandboxID: "sandbox-1", Source: egressProxyCredentialSource})
	for hash, token := range store.tokens {
		raw, _ := llms.HashFacadeToken(revoked)
		if hash == raw {
			token.RevokedAt = time.Now().UTC()
			store.tokens[hash] = token
		}
	}
	llmOnly := store.issue(t, llms.NewFacadeTokenRequest{SandboxID: "sandbox-1", Source: "session"})
	mediation := egressProxyTestMediation(store, egressProxyTestSandboxes{session: networkPolicyTestSession()}, denyDeclaration())

	tests := []struct {
		name    string
		claimed string
		token   string
		want    string
		wantErr bool
	}{
		{name: "valid credential", claimed: "sandbox-1", token: valid, want: "sandbox-1"},
		{name: "valid credential without a claimed name", token: valid, want: "sandbox-1"},
		{name: "claimed name belongs to another sandbox", claimed: "sandbox-2", token: valid, wantErr: true},
		{name: "unknown credential", claimed: "sandbox-1", token: "ac_llm_deadbeef", wantErr: true},
		{name: "no credential", claimed: "sandbox-1", wantErr: true},
		{name: "revoked credential", claimed: "sandbox-1", token: revoked, wantErr: true},
		{name: "credential issued for another purpose", claimed: "sandbox-1", token: llmOnly, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mediation.AuthorizeEgressProxy(context.Background(), test.claimed, test.token)
			if test.wantErr {
				if err == nil {
					t.Fatalf("AuthorizeEgressProxy() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AuthorizeEgressProxy() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("AuthorizeEgressProxy() = %q, want %q", got, test.want)
			}
		})
	}

	expired := store.issue(t, llms.NewFacadeTokenRequest{SandboxID: "sandbox-1", Source: egressProxyCredentialSource})
	for hash, token := range store.tokens {
		wanted, _ := llms.HashFacadeToken(expired)
		if hash == wanted {
			token.ExpiresAt = time.Now().UTC().Add(-time.Minute)
			store.tokens[hash] = token
		}
	}
	if _, err := mediation.AuthorizeEgressProxy(context.Background(), "sandbox-1", expired); err == nil {
		t.Fatal("AuthorizeEgressProxy() accepted an expired credential")
	}
}

func TestSandboxEgressProxyPolicyFollowsTheDeclaration(t *testing.T) {
	session := networkPolicyTestSession()
	store := newEgressProxyTestStore()

	declaring := egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, denyDeclaration())
	policy, err := declaring.EgressProxyPolicy(context.Background(), "sandbox-1")
	if err != nil {
		t.Fatalf("EgressProxyPolicy() error = %v", err)
	}
	allowed := egress.Decide(policy, egress.NetworkRequest("sandbox-1", egress.Endpoint{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolHTTPS}))
	if !allowed.Allowed() {
		t.Fatalf("declared allowance was denied: %+v", allowed)
	}
	denied := egress.Decide(policy, egress.NetworkRequest("sandbox-1", egress.Endpoint{Host: "example.com", Port: 443, Protocol: egress.ProtocolHTTPS}))
	if denied.Allowed() {
		t.Fatalf("undeclared destination was allowed: %+v", denied)
	}
	engineHost, enginePort, err := net.SplitHostPort(strings.TrimPrefix(networkPolicyTestBaseURL, "http://"))
	if err != nil {
		t.Fatalf("test base URL %q has no usable address: %v", networkPolicyTestBaseURL, err)
	}
	enginePortNumber, err := strconv.Atoi(enginePort)
	if err != nil {
		t.Fatalf("test base URL %q has no usable port: %v", networkPolicyTestBaseURL, err)
	}
	engineEndpoint := egress.Decide(policy, egress.NetworkRequest("sandbox-1", egress.Endpoint{Host: engineHost, Port: enginePortNumber, Protocol: egress.ProtocolHTTP}))
	if !engineEndpoint.Allowed() {
		t.Fatalf("engine endpoint was denied under a declared policy: %+v", engineEndpoint)
	}

	undeclared := egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, nil)
	policy, err = undeclared.EgressProxyPolicy(context.Background(), "sandbox-1")
	if err != nil {
		t.Fatalf("EgressProxyPolicy() error = %v", err)
	}
	if !egress.Decide(policy, egress.NetworkRequest("sandbox-1", egress.Endpoint{Host: "example.com", Port: 443, Protocol: egress.ProtocolHTTPS})).Allowed() {
		t.Fatal("an undeclared sandbox was restricted; D3 says undeclared is not deny")
	}
}

func TestSandboxEgressProxyPolicyReportsAnUnloadableSandbox(t *testing.T) {
	mediation := egressProxyTestMediation(newEgressProxyTestStore(), egressProxyTestSandboxes{err: errTestCredentialNotFound}, denyDeclaration())
	if _, err := mediation.EgressProxyPolicy(context.Background(), "gone"); err == nil {
		t.Fatal("EgressProxyPolicy() returned a policy for a sandbox that could not be loaded")
	}
}

// TestDriverSandboxDefersProxyCredentialIssuance pins the create-time contract
// that keeps issuance to one credential per runtime: handing a sandbox to a
// driver must not itself issue a credential, because a guest reads its
// environment once, when its runtime is created. Only the driver's creation path
// resolves the provider, so an ensure that reuses a runtime — every run, every
// scheduler tick, and every guest-file operation — cannot accumulate live
// credentials no guest could ever present.
func TestDriverSandboxDefersProxyCredentialIssuance(t *testing.T) {
	store := newEgressProxyTestStore()
	session := networkPolicyTestSession()
	adapter := driverRuntimeAdapter{
		networkPolicies: driverSandboxNetworkPolicy{config: networkPolicyTestConfig(), declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
		egressProxy:     egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, denyDeclaration()),
	}
	driverSandbox, _, err := adapter.driverSandbox(context.Background(), session)
	if err != nil {
		t.Fatalf("driverSandbox() error = %v", err)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("credentials issued by handing the sandbox to the driver = %d, want 0 until a runtime is created", len(store.tokens))
	}
	if driverSandbox.ProxyEnv == nil {
		t.Fatal("a mediated sandbox carries no create-time proxy environment provider")
	}
	env, err := driverSandbox.ProxyEnv(context.Background())
	if err != nil {
		t.Fatalf("resolving the create-time proxy environment: %v", err)
	}
	if envValues(env)["HTTPS_PROXY"] == "" {
		t.Fatalf("environment = %v, want the proxy", envValues(env))
	}
	if len(store.tokens) != 1 {
		t.Fatalf("credentials issued for a runtime creation = %d, want exactly 1", len(store.tokens))
	}
}

// TestDriverSandboxMediatesOnlyWhenTheProxyIsAvailable is the wiring pin: the
// driver-boundary policy records mediation exactly when the environment that
// routes the guest through the proxy was attached, so a driver can never report
// enforcement the engine did not wire.
func TestDriverSandboxMediatesOnlyWhenTheProxyIsAvailable(t *testing.T) {
	session := networkPolicyTestSession()

	t.Run("available proxy mediates", func(t *testing.T) {
		store := newEgressProxyTestStore()
		adapter := driverRuntimeAdapter{
			networkPolicies: driverSandboxNetworkPolicy{config: networkPolicyTestConfig(), declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
			egressProxy:     egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, denyDeclaration()),
		}
		driverSandbox, record, err := adapter.driverSandbox(context.Background(), session)
		if err != nil {
			t.Fatalf("driverSandbox() error = %v", err)
		}
		if driverSandbox.NetworkPolicy == nil || !driverSandbox.NetworkPolicy.MediatedByForwardProxy {
			t.Fatalf("policy = %+v, want a mediated policy", driverSandbox.NetworkPolicy)
		}
		if driverSandbox.ProxyEnv == nil {
			t.Fatal("the sandbox carries no create-time proxy environment provider")
		}
		env, err := driverSandbox.ProxyEnv(context.Background())
		if err != nil {
			t.Fatalf("proxy environment error = %v", err)
		}
		if envValues(env)["HTTPS_PROXY"] == "" {
			t.Fatalf("environment = %v, want the proxy", envValues(env))
		}
		if record.Applied != string(egress.ApplicationMediated) {
			t.Fatalf("record = %+v, want the runtime recorded as mediated so a later start knows its traffic is decided per request", record)
		}
		// The seat is real: the driver's own gate must not warn or refuse.
		if err := driverpkg.RequireSandboxNetworkEnforcement(driverpkg.RuntimeDriverDocker, session.Summary.ID, driverSandbox.NetworkPolicy, appconfig.EgressUnenforceableRefuse); err != nil {
			t.Fatalf("RequireSandboxNetworkEnforcement() error = %v, want nil for a mediated policy", err)
		}
	})

	t.Run("unreachable proxy leaves the declaration unenforced", func(t *testing.T) {
		adapter := driverRuntimeAdapter{
			networkPolicies: driverSandboxNetworkPolicy{config: networkPolicyTestConfig(), declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
			egressProxy: NewSandboxEgressProxy(
				&appconfig.Config{HttpListen: "127.0.0.1:7410"},
				newEgressProxyTestStore(),
				egressProxyTestSandboxes{session: session},
				stubNetworkDeclarationResolver{declaration: denyDeclaration()},
			),
		}
		driverSandbox, record, err := adapter.driverSandbox(context.Background(), session)
		if err != nil {
			t.Fatalf("driverSandbox() error = %v", err)
		}
		if driverSandbox.NetworkPolicy == nil || driverSandbox.NetworkPolicy.MediatedByForwardProxy {
			t.Fatalf("policy = %+v, want an unmediated policy", driverSandbox.NetworkPolicy)
		}
		if driverSandbox.ProxyEnv != nil {
			t.Fatal("an unreachable proxy must not install a create-time proxy environment")
		}
		if len(driverSandbox.EnvItems) != 0 {
			t.Fatalf("environment = %v, want none", envValues(driverSandbox.EnvItems))
		}
		// The declaration denies by default and nothing applied it, which is a
		// different fact from "no declaration" and must not be recorded as one.
		if record.Applied != string(egress.ApplicationUnenforced) {
			t.Fatalf("record = %+v, want the runtime recorded as declared but unenforced", record)
		}
	})

	t.Run("a loopback proxy address is not reachable from a sandbox", func(t *testing.T) {
		// The daemon may be told to hand guests a loopback address (an explicit
		// AGENT_COMPOSE_RUNTIME_BASE_URL, or a listen address on 127.0.0.1 for a
		// driver without the docker reachability rule). The guest runs in its own
		// network namespace, so that address reaches the guest itself: claiming
		// mediation there would set a proxy the guest cannot use and skip the
		// gate that keeps an unenforced declaration visible.
		// An IPv6 loopback is left out of this table because deriving the engine
		// endpoints from it fails earlier, for reasons this test does not own.
		for _, address := range []string{"http://127.0.0.1:7410", "http://localhost:7410", "http://0.0.0.0:7410"} {
			config := networkPolicyTestConfig()
			config.RuntimeBaseURL = address
			adapter := driverRuntimeAdapter{
				networkPolicies: driverSandboxNetworkPolicy{config: config, declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
				egressProxy: NewSandboxEgressProxy(
					config,
					newEgressProxyTestStore(),
					egressProxyTestSandboxes{session: session},
					stubNetworkDeclarationResolver{declaration: denyDeclaration()},
				),
			}
			driverSandbox, _, err := adapter.driverSandbox(context.Background(), session)
			if err != nil {
				t.Fatalf("driverSandbox() error = %v", err)
			}
			if driverSandbox.NetworkPolicy == nil || driverSandbox.NetworkPolicy.MediatedByForwardProxy {
				t.Fatalf("address %q: policy = %+v, want an unmediated policy", address, driverSandbox.NetworkPolicy)
			}
			if driverSandbox.ProxyEnv != nil {
				t.Fatalf("address %q: a guest-local proxy must not install a create-time proxy environment", address)
			}
			if len(driverSandbox.EnvItems) != 0 {
				t.Fatalf("address %q: environment = %v, want none", address, envValues(driverSandbox.EnvItems))
			}
		}
	})

	t.Run("a loopback listen leaves a VM driver unenforced too", func(t *testing.T) {
		boxlite := networkPolicyTestSession()
		boxlite.Summary.Driver = driverpkg.RuntimeDriverBoxlite
		adapter := driverRuntimeAdapter{
			networkPolicies: driverSandboxNetworkPolicy{config: &appconfig.Config{HttpListen: "127.0.0.1:7410"}, declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
			egressProxy: NewSandboxEgressProxy(
				&appconfig.Config{HttpListen: "127.0.0.1:7410"},
				newEgressProxyTestStore(),
				egressProxyTestSandboxes{session: boxlite},
				stubNetworkDeclarationResolver{declaration: denyDeclaration()},
			),
		}
		driverSandbox, _, err := adapter.driverSandbox(context.Background(), boxlite)
		if err != nil {
			t.Fatalf("driverSandbox() error = %v", err)
		}
		if driverSandbox.NetworkPolicy == nil || driverSandbox.NetworkPolicy.MediatedByForwardProxy {
			t.Fatalf("policy = %+v, want an unmediated policy", driverSandbox.NetworkPolicy)
		}
		if driverSandbox.ProxyEnv != nil {
			t.Fatal("an unreachable proxy must not install a create-time proxy environment")
		}
		if len(driverSandbox.EnvItems) != 0 {
			t.Fatalf("environment = %v, want none", envValues(driverSandbox.EnvItems))
		}
	})

	t.Run("a driver that applies the declaration records that it did", func(t *testing.T) {
		applying := networkPolicyTestSession()
		applying.Summary.Driver = driverpkg.RuntimeDriverMicrosandbox
		adapter := driverRuntimeAdapter{
			networkPolicies: driverSandboxNetworkPolicy{config: networkPolicyTestConfig(), declarations: stubNetworkDeclarationResolver{declaration: denyDeclaration()}},
			egressProxy:     NewSandboxEgressProxy(nil, nil, nil, stubNetworkDeclarationResolver{declaration: denyDeclaration()}),
		}
		_, record, err := adapter.driverSandbox(context.Background(), applying)
		if err != nil {
			t.Fatalf("driverSandbox() error = %v", err)
		}
		if record.Applied != string(egress.ApplicationDriverApplied) {
			t.Fatalf("record = %+v, want the runtime recorded as driver-applied so a retained runtime is reported as keeping it", record)
		}
	})

	t.Run("undeclared sandbox is untouched", func(t *testing.T) {
		store := newEgressProxyTestStore()
		adapter := driverRuntimeAdapter{
			networkPolicies: driverSandboxNetworkPolicy{config: networkPolicyTestConfig(), declarations: stubNetworkDeclarationResolver{}},
			egressProxy:     egressProxyTestMediation(store, egressProxyTestSandboxes{session: session}, nil),
		}
		driverSandbox, record, err := adapter.driverSandbox(context.Background(), session)
		if err != nil {
			t.Fatalf("driverSandbox() error = %v", err)
		}
		if driverSandbox.NetworkPolicy != nil {
			t.Fatalf("policy = %+v, want nil for an undeclared project", driverSandbox.NetworkPolicy)
		}
		if len(driverSandbox.EnvItems) != 0 || len(store.tokens) != 0 {
			t.Fatalf("an undeclared sandbox was given a proxy: env=%v credentials=%d", envValues(driverSandbox.EnvItems), len(store.tokens))
		}
		// An open policy is still recorded: it is the fact that makes a
		// declaration added later visible as a change this runtime does not have.
		if record.Applied != string(egress.ApplicationOpen) || record.Generation == 0 {
			t.Fatalf("record = %+v, want an open policy recorded with its generation", record)
		}
	})
}

func envValues(items []driverpkg.SandboxEnvVar) map[string]string {
	values := make(map[string]string, len(items))
	for _, item := range items {
		values[item.Name] = item.Value
	}
	return values
}

func TestDriverSandboxCarriesTheDaemonSandboxInitEgress(t *testing.T) {
	for _, mode := range []string{"off", "audit", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			adapter := driverRuntimeAdapter{sandboxInitEgress: mode}
			driverSandbox, _, err := adapter.driverSandbox(context.Background(), networkPolicyTestSession())
			if err != nil {
				t.Fatalf("driverSandbox() error = %v", err)
			}
			found := ""
			for _, item := range driverSandbox.RuntimeEnvItems {
				if item.Name == "SANDBOX_INIT_EGRESS" {
					found = item.Value
				}
			}
			if found != mode {
				t.Fatalf("RuntimeEnvItems SANDBOX_INIT_EGRESS = %q, want %q", found, mode)
			}
		})
	}
}
