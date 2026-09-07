package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestJupyterProxyRouteCoverage(t *testing.T) {
	e := echo.New()
	store := &fakeJupyterStore{state: domain.ProxyState{ProxyPath: "/agent-compose/session/session-1", Token: "token value"}}
	var ensureCalls int
	RegisterJupyterRoutes(e, JupyterOptions{
		BasePath: "/jupyter/",
		Store:    store,
		EnsureReady: func(context.Context, string) (domain.ProxyState, error) {
			ensureCalls++
			return store.state, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/jupyter/session-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect || !strings.Contains(rec.Header().Get("Location"), "token=token+value") || ensureCalls != 1 {
		t.Fatalf("redirect status=%d location=%q ensure=%d", rec.Code, rec.Header().Get("Location"), ensureCalls)
	}

	store.err = errors.New("proxy state missing")
	req = httptest.NewRequest(http.MethodPost, "/jupyter/session-1/api/kernels", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("store error status=%d body=%s", rec.Code, rec.Body.String())
	}

	failing := echo.New()
	RegisterJupyterRoutes(failing, JupyterOptions{
		BasePath: "/jupyter",
		Store:    fakeJupyterStore{err: errors.New("missing")},
		EnsureReady: func(context.Context, string) (domain.ProxyState, error) {
			return domain.ProxyState{}, errors.New("ensure failed")
		},
	})
	req = httptest.NewRequest(http.MethodGet, "/jupyter/session-1", nil)
	rec = httptest.NewRecorder()
	failing.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("ensure redirect failure status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/jupyter/session-1/api/kernels", nil)
	rec = httptest.NewRecorder()
	failing.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("ensure proxy failure status=%d body=%s", rec.Code, rec.Body.String())
	}

	if JupyterTargetReachable(domain.ProxyState{}, time.Millisecond) {
		t.Fatalf("empty proxy state reported reachable")
	}
}

func TestJupyterProxyPreservesPublicHostForSameOriginChecks(t *testing.T) {
	var gotHost, gotOrigin string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotOrigin = r.Header.Get("Origin")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(backendURL.Port())
	if err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	state := domain.ProxyState{Enabled: true, GuestHost: "127.0.0.1", GuestPort: port, HostPort: port, ProxyPath: "/jupyter/session-1"}
	RegisterJupyterRoutes(e, JupyterOptions{
		BasePath: "/jupyter",
		Store:    fakeJupyterStore{state: state},
		EnsureReady: func(context.Context, string) (domain.ProxyState, error) {
			return state, nil
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/jupyter/session-1/api/kernels", nil)
	request.Host = "proxy.example.test"
	request.Header.Set("Origin", "http://proxy.example.test")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("proxy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotHost != request.Host || gotOrigin != request.Header.Get("Origin") {
		t.Fatalf("backend host/origin = %q/%q, want %q/%q", gotHost, gotOrigin, request.Host, request.Header.Get("Origin"))
	}
}

func TestNewJupyterProxyTransportDisablesProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	transport := newJupyterProxyTransport()
	if transport.Proxy != nil {
		t.Fatalf("jupyter proxy transport should not use proxy environment")
	}
}

type fakeJupyterStore struct {
	state domain.ProxyState
	err   error
}

func (s fakeJupyterStore) GetProxyState(string) (domain.ProxyState, error) {
	if s.err != nil {
		return domain.ProxyState{}, s.err
	}
	return s.state, nil
}
