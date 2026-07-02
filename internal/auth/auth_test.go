package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestAuthManagerDisabledAllowsRequests(t *testing.T) {
	testAuthManagerDisabledAllowsRequests(t)
}

func testAuthManagerDisabledAllowsRequests(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{})
	req := httptest.NewRequest(http.MethodGet, "/agentcompose.v1.SessionService/ListSessions", nil)
	if _, _, ok := manager.validateRequest(req); !ok {
		t.Fatal("disabled auth should validate every request")
	}
	app := echo.New()
	manager.RegisterRoutes(app)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled status code = %d", rec.Code)
	}
	var status authStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode disabled status: %v", err)
	}
	if status.Enabled || !status.LoggedIn {
		t.Fatalf("disabled status = %#v", status)
	}
}

func TestAuthManagerDefaultsUsernameAndTTL(t *testing.T) {
	testAuthManagerDefaultsUsernameAndTTL(t)
}

func testAuthManagerDefaultsUsernameAndTTL(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{AuthPassword: "secret", AuthSecret: "test-secret"})
	if !manager.enabled || manager.username != "admin" || manager.ttl != 24*time.Hour {
		t.Fatalf("auth defaults = enabled:%t username:%q ttl:%s", manager.enabled, manager.username, manager.ttl)
	}
}

func TestAuthManagerValidatesSignedCookie(t *testing.T) {
	testAuthManagerValidatesSignedCookie(t)
}

func testAuthManagerValidatesSignedCookie(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{
		AuthUsername:   "admin",
		AuthPassword:   "secret",
		AuthSecret:     "test-secret",
		AuthSessionTTL: time.Hour,
	})
	expiresAt := time.Now().UTC().Add(time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/agentcompose.v1.SessionService/ListSessions", nil)
	req.AddCookie(manager.cookie(manager.signedValue("admin", expiresAt), expiresAt))

	username, _, ok := manager.validateRequest(req)
	if !ok {
		t.Fatal("expected signed cookie to validate")
	}
	if username != "admin" {
		t.Fatalf("unexpected username %q", username)
	}
}

func TestAuthManagerRejectsExpiredCookie(t *testing.T) {
	testAuthManagerRejectsExpiredCookie(t)
}

func testAuthManagerRejectsExpiredCookie(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{
		AuthUsername:   "admin",
		AuthPassword:   "secret",
		AuthSecret:     "test-secret",
		AuthSessionTTL: time.Hour,
	})
	expiresAt := time.Now().UTC().Add(-time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/agentcompose.v1.SessionService/ListSessions", nil)
	req.AddCookie(manager.cookie(manager.signedValue("admin", expiresAt), expiresAt))

	if _, _, ok := manager.validateRequest(req); ok {
		t.Fatal("expected expired cookie to be rejected")
	}
}

func TestAuthManagerRejectsTamperedCookie(t *testing.T) {
	testAuthManagerRejectsTamperedCookie(t)
}

func testAuthManagerRejectsTamperedCookie(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{
		AuthUsername:   "admin",
		AuthPassword:   "secret",
		AuthSecret:     "test-secret",
		AuthSessionTTL: time.Hour,
	})
	expiresAt := time.Now().UTC().Add(time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/agentcompose.v1.SessionService/ListSessions", nil)
	req.AddCookie(manager.cookie(manager.signedValue("admin", expiresAt)+"x", expiresAt))

	if _, _, ok := manager.validateRequest(req); ok {
		t.Fatal("expected tampered cookie to be rejected")
	}
}

func TestAuthManagerRoutesAndMiddleware(t *testing.T) {
	testAuthManagerRoutesAndMiddleware(t)
}

func testAuthManagerRoutesAndMiddleware(t *testing.T) {
	t.Helper()
	assertAuthManagerRoutesAndMiddleware(t)
}

func TestAuthManagerOAuthFlowSetsAuthCookie(t *testing.T) {
	testAuthManagerOAuthFlowSetsAuthCookie(t)
}

func TestAuthManagerOAuthFlowPreservesSubpathNext(t *testing.T) {
	testAuthManagerOAuthFlowPreservesSubpathNext(t)
}

func testAuthManagerOAuthFlowSetsAuthCookie(t *testing.T) {
	t.Helper()
	var oauthServerURL string
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			if r.URL.Query().Get("client_id") != "client-id" {
				t.Fatalf("client_id = %q", r.URL.Query().Get("client_id"))
			}
			callback := r.URL.Query().Get("redirect_uri")
			redirect, err := url.Parse(callback)
			if err != nil {
				t.Fatalf("parse callback: %v", err)
			}
			query := redirect.Query()
			query.Set("code", "auth-code")
			query.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = query.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if r.Form.Get("code") != "auth-code" || r.Form.Get("client_id") != "client-id" || r.Form.Get("client_secret") != "client-secret" {
				t.Fatalf("token form = %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"user-1","username":"oauth-user"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthServer.Close()
	oauthServerURL = oauthServer.URL

	app := echo.New()
	manager := NewAuthManager(&Config{
		AuthUsername:     "admin",
		AuthSecret:       "test-secret",
		AuthSessionTTL:   time.Hour,
		OAuthAPIKey:      "client-id",
		OAuthSecret:      "client-secret",
		OAuthScopes:      []string{"profile"},
		OAuthCallbackURL: "/oauth/callback",
		OAuthAuthURL:     oauthServerURL + "/authorize",
		OAuthTokenURL:    oauthServerURL + "/token",
		OAuthUserInfoURL: oauthServerURL + "/userinfo",
	})
	manager.RegisterRoutes(app)
	app.Use(manager.Middleware)
	app.GET("/api/private", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?next=%2Fapi%2Fprivate", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d body = %s", rec.Code, rec.Body.String())
	}
	stateCookie := rec.Result().Cookies()[0]
	authRedirect := rec.Header().Get("Location")

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(authRedirect)
	if err != nil {
		t.Fatalf("follow provider redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("provider redirect status = %d", resp.StatusCode)
	}

	callbackReq := httptest.NewRequest(http.MethodGet, resp.Header.Get("Location"), nil)
	callbackReq.AddCookie(stateCookie)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, callbackReq)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/api/private" {
		t.Fatalf("callback location = %q", rec.Header().Get("Location"))
	}

	var authCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == authCookieName {
			authCookie = cookie
		}
	}
	if authCookie == nil {
		t.Fatal("expected auth cookie from oauth callback")
	}

	privateReq := httptest.NewRequest(http.MethodGet, "/api/private", nil)
	privateReq.AddCookie(authCookie)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, privateReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated private status = %d", rec.Code)
	}
}

func testAuthManagerOAuthFlowPreservesSubpathNext(t *testing.T) {
	t.Helper()
	var providerState string
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			if got := r.URL.Query().Get("next"); got != "" {
				t.Fatalf("provider authorize next = %q, want empty", got)
			}
			providerState = r.URL.Query().Get("state")
			if providerState == "" {
				t.Fatal("provider authorize state is empty")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthServer.Close()

	app := echo.New()
	manager := NewAuthManager(&Config{
		AuthSecret:       "test-secret",
		AuthSessionTTL:   time.Hour,
		OAuthAPIKey:      "client-id",
		OAuthSecret:      "client-secret",
		OAuthCallbackURL: "/oauth/callback",
		OAuthAuthURL:     oauthServer.URL + "/authorize",
		OAuthTokenURL:    oauthServer.URL + "/token",
	})
	manager.RegisterRoutes(app)

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?next=%2Fagent-compose%2F", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d body = %s", rec.Code, rec.Body.String())
	}
	stateCookie := rec.Result().Cookies()[0]
	state, next, ok := decodeOAuthStateCookie(stateCookie.Value)
	if !ok {
		t.Fatal("state cookie did not decode")
	}
	if next != "/agent-compose/" {
		t.Fatalf("state cookie next = %q, want /agent-compose/", next)
	}

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("follow provider authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider authorize status = %d", resp.StatusCode)
	}
	if providerState != state {
		t.Fatalf("provider state = %q, want %q", providerState, state)
	}
}

func TestAuthManagerOAuthTokenExchangeFailure(t *testing.T) {
	testAuthManagerOAuthTokenExchangeFailure(t)
}

func testAuthManagerOAuthTokenExchangeFailure(t *testing.T) {
	t.Helper()
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			callback := r.URL.Query().Get("redirect_uri")
			redirect, err := url.Parse(callback)
			if err != nil {
				t.Fatalf("parse callback: %v", err)
			}
			query := redirect.Query()
			query.Set("code", "bad-code")
			query.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = query.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthServer.Close()

	app := echo.New()
	manager := NewAuthManager(&Config{
		AuthSecret:            "test-secret",
		AuthSessionTTL:        time.Hour,
		OAuthAPIKey:           "client-id",
		OAuthSecret:           "client-secret",
		OAuthScopes:           []string{"profile"},
		OAuthCallbackURL:      "/oauth/callback",
		OAuthAuthURL:          oauthServer.URL + "/authorize",
		OAuthTokenURL:         oauthServer.URL + "/token",
		OAuthClientAuthMethod: "client_secret_post",
	})
	manager.RegisterRoutes(app)

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d body = %s", rec.Code, rec.Body.String())
	}
	stateCookie := rec.Result().Cookies()[0]

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("follow provider redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	callbackReq := httptest.NewRequest(http.MethodGet, resp.Header.Get("Location"), nil)
	callbackReq.AddCookie(stateCookie)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, callbackReq)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("callback status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to exchange authorization code") {
		t.Fatalf("callback body = %s", rec.Body.String())
	}
}

func assertAuthManagerRoutesAndMiddleware(t *testing.T) {
	t.Helper()
	manager := NewAuthManager(&Config{
		AuthUsername:   "admin",
		AuthPassword:   "secret",
		AuthSecret:     "test-secret",
		AuthSessionTTL: time.Hour,
	})
	app := echo.New()
	manager.RegisterRoutes(app)
	app.Use(manager.Middleware)
	app.GET("/api/private", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	app.GET("/agent-compose.html", func(c echo.Context) error {
		return c.String(http.StatusOK, "app")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/private", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated api status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/agent-compose?x=1", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("html redirect status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login?next=%2Fagent-compose%3Fx%3D1" {
		t.Fatalf("redirect location = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/login?next=%2Fagent-compose", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login route should stay public and fall through, status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected login cookie")
	}
	var status authStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if !status.Enabled || !status.LoggedIn || status.Username != "admin" || status.ExpiresAt == "" {
		t.Fatalf("login status response = %#v", status)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated status code = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode authenticated status: %v", err)
	}
	if !status.Enabled || !status.LoggedIn || status.Username != "admin" || status.ExpiresAt == "" {
		t.Fatalf("authenticated status response = %#v", status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/private", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated api status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if rec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout cookie max age = %d", rec.Result().Cookies()[0].MaxAge)
	}
}

func TestAuthManagerRedirectsHTMLToSPALoginWithNext(t *testing.T) {
	manager := NewAuthManager(&Config{
		AuthUsername:   "admin",
		AuthPassword:   "secret",
		AuthSecret:     "test-secret",
		AuthSessionTTL: time.Hour,
	})
	app := echo.New()
	manager.RegisterRoutes(app)
	app.Use(manager.Middleware)
	app.GET("/agent-compose", func(c echo.Context) error {
		return c.String(http.StatusOK, "app")
	})

	req := httptest.NewRequest(http.MethodGet, "/agent-compose", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("html redirect status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login?next=%2Fagent-compose" {
		t.Fatalf("redirect location = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login route should be public and fall through, status = %d", rec.Code)
	}
}
