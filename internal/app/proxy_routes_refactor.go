package app

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	driverpkg "agent-compose/pkg/driver"
)

func registerProxyRoutes(app *echo.Echo, service *Service) {
	base := strings.TrimRight(service.config.JupyterProxyBasePath, "/")
	app.GET(base+"/:sessionID", func(c echo.Context) error {
		_, proxyState, err := service.ensureSessionProxyReady(c.Request().Context(), c.Param("sessionID"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		location := strings.TrimRight(proxyState.ProxyPath, "/")
		if proxyState.Token != "" {
			location += "?token=" + url.QueryEscape(proxyState.Token)
		}
		return c.Redirect(http.StatusTemporaryRedirect, location)
	})
	app.Any(base+"/:sessionID/*", func(c echo.Context) error {
		sessionID := c.Param("sessionID")
		proxyState, err := service.store.GetProxyState(sessionID)
		if err != nil || !jupyterTargetReachable(proxyState, 250*time.Millisecond) {
			if _, _, err := service.ensureSessionProxyReady(c.Request().Context(), sessionID); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
			proxyState, err = service.store.GetProxyState(sessionID)
			if err != nil {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
		}
		target, err := url.Parse("http://" + driverpkg.JupyterConnectAddress(toDriverProxyState(proxyState)))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.SetXForwarded()
				req.Out.Host = target.Host
				req.Out.URL.Path = req.In.URL.Path
				req.Out.URL.RawPath = req.Out.URL.Path
				req.Out.URL.RawQuery = req.In.URL.RawQuery
			},
			ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, proxyErr error) {
				rw.WriteHeader(http.StatusBadGateway)
				_, _ = rw.Write([]byte(proxyErr.Error()))
			},
		}
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}
