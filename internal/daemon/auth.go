package daemon

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	agentcompose "agent-compose/pkg/agentcompose/service"
	"agent-compose/pkg/auth"
	"agent-compose/pkg/config"
)

func installMiddleware(app *echo.Echo, conf *config.Config) {
	app.Use(middleware.RequestLogger())
	app.Use(middleware.Recover())
	authManager := auth.NewAuthManager(&auth.Config{
		AuthUsername:          conf.AuthUsername,
		AuthPassword:          conf.AuthPassword,
		AuthSecret:            conf.AuthSecret,
		AuthSessionTTL:        conf.AuthSessionTTL,
		OAuthAPIKey:           conf.OAuthAPIKey,
		OAuthSecret:           conf.OAuthSecret,
		OAuthScopes:           conf.OAuthScopes,
		OAuthCallbackURL:      conf.OAuthCallbackURL,
		OAuthAuthURL:          conf.OAuthAuthURL,
		OAuthTokenURL:         conf.OAuthTokenURL,
		OAuthUserInfoURL:      conf.OAuthUserInfoURL,
		OAuthClientAuthMethod: conf.OAuthClientAuthMethod,
		Bypass:                isLocalUnixSocketRequest,
		Skipper:               agentcompose.IsRuntimeLLMFacadeRequest,
	})
	authManager.RegisterRoutes(app)
	app.Use(authManager.Middleware)

	if conf.HTTPBasicAuth == "" {
		return
	}
	username := conf.HTTPBasicAuth
	password := ""
	if i := strings.Index(conf.HTTPBasicAuth, ":"); i >= 0 {
		username = conf.HTTPBasicAuth[:i]
		password = conf.HTTPBasicAuth[i+1:]
	}
	app.Use(middleware.BasicAuthWithConfig(middleware.BasicAuthConfig{
		Skipper: func(c echo.Context) bool {
			return isLocalUnixSocketRequest(c.Request()) || agentcompose.IsRuntimeLLMFacadeRequest(c.Request())
		},
		Realm: "Password Required",
		Validator: func(u, p string, c echo.Context) (bool, error) {
			return u == username && p == password, nil
		},
	}))
}
