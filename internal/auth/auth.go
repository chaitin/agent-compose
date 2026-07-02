package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
)

const authCookieName = "agent_compose_auth"
const oauthStateCookieName = "agent_compose_oauth_state"

type authStatusResponse struct {
	Enabled      bool   `json:"enabled"`
	LoggedIn     bool   `json:"loggedIn"`
	OAuthEnabled bool   `json:"oauthEnabled"`
	Username     string `json:"username,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AuthManager struct {
	enabled        bool
	username       string
	password       string
	secret         []byte
	ttl            time.Duration
	bypass         func(*http.Request) bool
	skipper        func(*http.Request) bool
	oauthEnabled   bool
	oauthUser      string
	oauthUserInfo  string
	oauth2Config   *oauth2.Config
	oauthStateTTL  time.Duration
	oauthCookieTTL time.Duration
}

type Config struct {
	AuthUsername          string
	AuthPassword          string
	AuthSecret            string
	AuthSessionTTL        time.Duration
	Bypass                func(*http.Request) bool
	Skipper               func(*http.Request) bool
	OAuthAPIKey           string
	OAuthSecret           string
	OAuthScopes           []string
	OAuthCallbackURL      string
	OAuthAuthURL          string
	OAuthTokenURL         string
	OAuthUserInfoURL      string
	OAuthClientAuthMethod string
}

type oauthUserInfoResponse struct {
	ID       string `json:"id"`
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

func NewAuthManager(config *Config) *AuthManager {
	manager := &AuthManager{
		enabled:        config.AuthPassword != "",
		username:       strings.TrimSpace(config.AuthUsername),
		password:       config.AuthPassword,
		ttl:            config.AuthSessionTTL,
		bypass:         config.Bypass,
		skipper:        config.Skipper,
		oauthStateTTL:  5 * time.Minute,
		oauthCookieTTL: config.AuthSessionTTL,
	}
	manager.oauthEnabled = config.OAuthAPIKey != "" && config.OAuthCallbackURL != "" && config.OAuthAuthURL != "" && config.OAuthTokenURL != ""
	if manager.oauthEnabled {
		manager.enabled = true
		manager.oauthUser = strings.TrimSpace(config.AuthUsername)
		if manager.oauthUser == "" {
			manager.oauthUser = "oauth"
		}
		manager.oauthUserInfo = config.OAuthUserInfoURL
		authStyle := oauth2.AuthStyleInParams
		clientSecret := config.OAuthSecret
		switch strings.ToLower(config.OAuthClientAuthMethod) {
		case "client_secret_basic":
			authStyle = oauth2.AuthStyleInHeader
		case "none":
			clientSecret = ""
		case "client_secret_post", "":
			authStyle = oauth2.AuthStyleInParams
		default:
			slog.Warn("unsupported OAUTH_CLIENT_AUTH_METHOD; using client_secret_post", "value", config.OAuthClientAuthMethod)
		}
		manager.oauth2Config = &oauth2.Config{
			ClientID:     config.OAuthAPIKey,
			ClientSecret: clientSecret,
			RedirectURL:  config.OAuthCallbackURL,
			Scopes:       config.OAuthScopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:   config.OAuthAuthURL,
				TokenURL:  config.OAuthTokenURL,
				AuthStyle: authStyle,
			},
		}
	}
	if manager.username == "" {
		manager.username = "admin"
	}
	if manager.ttl <= 0 {
		manager.ttl = 24 * time.Hour
	}
	if !manager.enabled {
		return manager
	}
	if config.AuthSecret != "" {
		manager.secret = []byte(config.AuthSecret)
	} else {
		manager.secret = make([]byte, 32)
		if _, err := rand.Read(manager.secret); err != nil {
			manager.secret = []byte(fmt.Sprintf("%d:%s", time.Now().UnixNano(), manager.password))
		}
		slog.Warn("AUTH_SECRET is not set; auth sessions will be invalid after restart")
	}
	return manager
}

func (a *AuthManager) RegisterRoutes(e *echo.Echo) {
	e.GET("/api/auth/status", a.handleStatus)
	e.POST("/api/auth/login", a.handleLogin)
	e.POST("/api/auth/logout", a.handleLogout)
	e.GET("/oauth/authorize", a.handleOAuthAuthorize)
	e.GET("/oauth/callback", a.handleOAuthCallback)
}

func (a *AuthManager) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !a.enabled || isPublicAuthPath(c.Request().URL.Path) {
			return next(c)
		}
		if a.skipper != nil && a.skipper(c.Request()) {
			return next(c)
		}
		if a.bypass != nil && a.bypass(c.Request()) {
			return next(c)
		}
		if !a.protectsPath(c.Request().URL.Path, c.Request().Header.Get("Accept")) {
			return next(c)
		}
		if _, _, ok := a.validateRequest(c.Request()); ok {
			return next(c)
		}
		if acceptsHTML(c.Request()) {
			return c.Redirect(http.StatusFound, loginRedirectPath(c.Request()))
		}
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
}

func (a *AuthManager) handleStatus(c echo.Context) error {
	username, expiresAt, ok := a.validateRequest(c.Request())
	resp := authStatusResponse{Enabled: a.enabled, LoggedIn: !a.enabled || ok, OAuthEnabled: a.oauthEnabled}
	if ok {
		resp.Username = username
		resp.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return c.JSON(http.StatusOK, resp)
}

func (a *AuthManager) handleLogin(c echo.Context) error {
	if !a.enabled {
		return c.JSON(http.StatusOK, authStatusResponse{Enabled: false, LoggedIn: true})
	}
	if a.password == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "password login is not configured"})
	}
	var req loginRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid login request"})
	}
	if subtle.ConstantTimeCompare([]byte(req.Username), []byte(a.username)) != 1 ||
		subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.password)) != 1 {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
	}
	expiresAt := time.Now().UTC().Add(a.ttl)
	http.SetCookie(c.Response(), a.cookie(a.signedValue(a.username, expiresAt), expiresAt))
	return c.JSON(http.StatusOK, authStatusResponse{
		Enabled:   true,
		LoggedIn:  true,
		Username:  a.username,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

func (a *AuthManager) handleLogout(c echo.Context) error {
	http.SetCookie(c.Response(), a.cookie("", time.Unix(0, 0).UTC()))
	return c.JSON(http.StatusOK, authStatusResponse{Enabled: a.enabled, LoggedIn: false})
}

func (a *AuthManager) handleOAuthAuthorize(c echo.Context) error {
	if !a.oauthEnabled || a.oauth2Config == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "oauth is not configured"})
	}
	state, err := generateOAuthState(16)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to start oauth login"})
	}
	next := sanitizeOAuthNext(c.QueryParam("next"))
	http.SetCookie(c.Response(), a.oauthStateCookie(state, next, time.Now().UTC().Add(a.oauthStateTTL)))
	authURL := a.oauth2Config.AuthCodeURL(state, oauth2.SetAuthURLParam("scope", strings.Join(a.oauth2Config.Scopes, " ")))
	return c.Redirect(http.StatusFound, authURL)
}

func (a *AuthManager) handleOAuthCallback(c echo.Context) error {
	if !a.oauthEnabled || a.oauth2Config == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "oauth is not configured"})
	}
	if authErr := c.QueryParam("error"); authErr != "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "oauth authorization failed: " + authErr})
	}
	stateCookie, err := c.Request().Cookie(oauthStateCookieName)
	if err != nil || stateCookie.Value == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "oauth state cookie missing or expired"})
	}
	http.SetCookie(c.Response(), a.oauthStateCookie("", "", time.Unix(0, 0).UTC()))
	expectedState, next, ok := decodeOAuthStateCookie(stateCookie.Value)
	if !ok || expectedState == "" || expectedState != c.QueryParam("state") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "oauth state mismatch"})
	}
	code := c.QueryParam("code")
	if code == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "authorization code is missing"})
	}
	token, err := a.oauth2Config.Exchange(c.Request().Context(), code)
	if err != nil {
		slog.Error("oauth token exchange failed",
			"error", err,
			"token_url", a.oauth2Config.Endpoint.TokenURL,
			"redirect_url", a.oauth2Config.RedirectURL,
		)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "failed to exchange authorization code"})
	}
	username, err := a.fetchOAuthUsername(c.Request(), token)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "failed to retrieve oauth user"})
	}
	if username == "" {
		username = a.oauthUser
	}
	expiresAt := time.Now().UTC().Add(a.ttl)
	http.SetCookie(c.Response(), a.cookie(a.signedValue(username, expiresAt), expiresAt))
	return c.Redirect(http.StatusFound, sanitizeOAuthNext(next))
}

func (a *AuthManager) cookie(value string, expiresAt time.Time) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if value == "" {
		maxAge = -1
	}
	return &http.Cookie{
		Name:     authCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (a *AuthManager) signedValue(username string, expiresAt time.Time) string {
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	payload := username + "|" + expiry
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(payload))
	value := payload + "|" + hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (a *AuthManager) oauthStateCookie(state, next string, expiresAt time.Time) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if state == "" {
		maxAge = -1
	}
	return &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    encodeOAuthStateCookie(state, next),
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (a *AuthManager) fetchOAuthUsername(r *http.Request, token *oauth2.Token) (string, error) {
	if a.oauthUserInfo == "" {
		return a.oauthUser, nil
	}
	client := a.oauth2Config.Client(r.Context(), token)
	resp, err := client.Get(a.oauthUserInfo)
	if err != nil {
		return "", fmt.Errorf("call userinfo endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info oauthUserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	return firstNonEmptyOAuthValue(info.Username, info.Email, info.Name, info.ID, info.Sub, a.oauthUser), nil
}

func (a *AuthManager) validateRequest(r *http.Request) (string, time.Time, bool) {
	if !a.enabled {
		return "", time.Time{}, true
	}
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return "", time.Time{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", time.Time{}, false
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return "", time.Time{}, false
	}
	username, expiry, signature := parts[0], parts[1], parts[2]
	expiresUnix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	expiresAt := time.Unix(expiresUnix, 0).UTC()
	if !time.Now().UTC().Before(expiresAt) {
		return "", time.Time{}, false
	}
	if username != a.username && (!a.oauthEnabled || username == "") {
		return "", time.Time{}, false
	}
	expected := a.signedValue(username, expiresAt)
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) != 1 {
		return "", time.Time{}, false
	}
	if _, err := hex.DecodeString(signature); err != nil {
		return "", time.Time{}, false
	}
	return username, expiresAt, true
}

func (a *AuthManager) protectsPath(path string, accept string) bool {
	if strings.HasPrefix(path, "/agentcompose.v1.") || strings.HasPrefix(path, "/agent-compose/session/") {
		return true
	}
	if strings.HasPrefix(path, "/api/") {
		return true
	}
	if path == "/" || path == "/index.html" || path == "/agent-compose.html" || path == "/config.html" || path == "/loaders.html" {
		return strings.Contains(accept, "text/html")
	}
	return strings.Contains(accept, "text/html")
}

func isPublicAuthPath(path string) bool {
	if strings.HasPrefix(path, "/api/webhooks/") {
		return true
	}
	return path == "/login" || path == "/api/auth/status" || path == "/api/auth/login" || path == "/api/auth/logout" ||
		path == "/oauth/authorize" || path == "/oauth/callback"
}

func acceptsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func loginRedirectPath(r *http.Request) string {
	next := r.URL.RequestURI()
	if next == "" || isLoginPath(next) || strings.HasPrefix(next, "//") {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(next)
}

func generateOAuthState(length int) (string, error) {
	if length <= 0 {
		length = 16
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func encodeOAuthStateCookie(state, next string) string {
	payload := state + "|" + sanitizeOAuthNext(next)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeOAuthStateCookie(value string) (string, string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], sanitizeOAuthNext(parts[1]), true
}

func sanitizeOAuthNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || isLoginPath(next) {
		return "/"
	}
	return next
}

func isLoginPath(path string) bool {
	return path == "/login" || strings.HasPrefix(path, "/login?") || strings.HasPrefix(path, "/login#")
}

func firstNonEmptyOAuthValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
