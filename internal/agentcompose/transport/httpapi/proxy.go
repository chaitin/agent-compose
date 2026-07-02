package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	driverpkg "agent-compose/internal/driver"
)

type ProxyState struct {
	ProxyPath  string
	GuestHost  string
	HostPort   int
	GuestPort  int
	JupyterURL string
	Token      string
}

type ProxyRoutes struct {
	BasePath    string
	EnsureReady func(ctx context.Context, sessionID string) (ProxyState, error)
	GetState    func(sessionID string) (ProxyState, error)
}

func RegisterProxyRoutes(app *echo.Echo, routes ProxyRoutes) {
	base := strings.TrimRight(routes.BasePath, "/")
	app.GET(base+"/:sessionID", func(c echo.Context) error {
		proxyState, err := routes.EnsureReady(c.Request().Context(), c.Param("sessionID"))
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
		proxyState, err := routes.GetState(sessionID)
		if err != nil || !jupyterTargetReachable(proxyState, 250*time.Millisecond) {
			if _, err := routes.EnsureReady(c.Request().Context(), sessionID); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		proxyState, err = routes.GetState(sessionID)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
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
			ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
				rw.WriteHeader(http.StatusBadGateway)
				_, _ = rw.Write([]byte(proxyErr.Error()))
			},
		}
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}

func jupyterTargetReachable(proxyState ProxyState, timeout time.Duration) bool {
	_, port := driverpkg.JupyterConnectTarget(toDriverProxyState(proxyState))
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", driverpkg.JupyterConnectAddress(toDriverProxyState(proxyState)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func toDriverProxyState(state ProxyState) driverpkg.ProxyState {
	return driverpkg.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}
