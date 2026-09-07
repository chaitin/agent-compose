package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

type Store interface {
	GetProxyState(id string) (domain.ProxyState, error)
}

type EnsureReadyFunc func(ctx context.Context, sessionID string) (domain.ProxyState, error)

type JupyterOptions struct {
	BasePath    string
	Store       Store
	EnsureReady EnsureReadyFunc
}

func RegisterJupyterRoutes(app *echo.Echo, opts JupyterOptions) {
	base := strings.TrimRight(opts.BasePath, "/")
	transport := newJupyterProxyTransport()
	app.GET(base+"/:sessionID", func(c echo.Context) error {
		proxyState, err := opts.EnsureReady(c.Request().Context(), c.Param("sessionID"))
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
		if !JupyterTargetReachable(func() domain.ProxyState {
			proxyState, err := opts.Store.GetProxyState(sessionID)
			if err != nil {
				return domain.ProxyState{}
			}
			return proxyState
		}(), 250*time.Millisecond) {
			if _, err := opts.EnsureReady(c.Request().Context(), sessionID); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		proxyState, err := opts.Store.GetProxyState(sessionID)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		target, err := url.Parse("http://" + driverpkg.JupyterConnectAddress(execution.ToDriverProxyState(proxyState)))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.SetXForwarded()
				// Preserve the public proxy host so Jupyter's same-origin and
				// XSRF checks see the same host as the browser Origin header.
				req.Out.Host = req.In.Host
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

func newJupyterProxyTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}

func JupyterTargetReachable(proxyState domain.ProxyState, timeout time.Duration) bool {
	return sandboxes.JupyterTargetReachable(proxyState, timeout)
}
