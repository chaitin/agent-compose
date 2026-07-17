package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	controlauth "agent-compose/pkg/auth"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func newDaemonRequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			requestID := strings.TrimSpace(c.Request().Header.Get("X-Request-ID"))
			if requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\r\n") {
				requestID = uuid.NewString()
				c.Request().Header.Set("X-Request-ID", requestID)
			}
			c.Response().Header().Set("X-Request-ID", requestID)
			return next(c)
		}
	}
}

func newDaemonHTTPSecurityMiddleware(service *controlauth.Service, jupyterBasePath string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			policy, applies := rawHTTPPolicy(request, jupyterBasePath)
			if !applies || policy.access == controlauth.AccessRead {
				return next(c)
			}
			actor, ok := controlauth.IdentityFromContext(request.Context())
			if !ok {
				return daemonUnauthorized(c)
			}
			if !controlauth.Allowed(actor.Role, policy.access) {
				_ = service.DenyAudit(context.WithoutCancel(request.Context()), actor, request.Header.Get("X-Request-ID"), policy.action)
				return echo.NewHTTPError(http.StatusForbidden, controlauth.ErrPermissionDenied.Error())
			}

			resource := rawHTTPResource(c, policy.resourceType)
			params, _ := json.Marshal(map[string]string{"method": request.Method, "path": request.URL.Path})
			audit, err := service.BeginAudit(request.Context(), actor, request.Header.Get("X-Request-ID"), policy.action, resource, string(params))
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "begin operation audit")
			}
			callErr := next(c)
			status := controlauth.AuditStatusSucceeded
			errorCode, errorMessage := "", ""
			if callErr != nil {
				status = controlauth.AuditStatusFailed
				errorCode = fmt.Sprintf("http_%d", httpStatusForError(callErr))
				errorMessage = callErr.Error()
			}
			if finishErr := service.FinishAudit(context.WithoutCancel(request.Context()), audit, status, resource, "{}", errorCode, errorMessage); finishErr != nil {
				if callErr != nil {
					return fmt.Errorf("%w; finish operation audit: %v", callErr, finishErr)
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "finish operation audit")
			}
			return callErr
		}
	}
}

type rawHTTPRoutePolicy struct {
	access       controlauth.Access
	action       string
	resourceType string
}

func rawHTTPPolicy(request *http.Request, jupyterBasePath string) (rawHTTPRoutePolicy, bool) {
	if request == nil || request.URL == nil || daemonAuthExemptRequest(request, jupyterBasePath) || strings.HasPrefix(request.URL.Path, "/agentcompose.v2.") {
		return rawHTTPRoutePolicy{}, false
	}
	if request.Method == http.MethodGet {
		return rawHTTPRoutePolicy{access: controlauth.AccessRead}, true
	}
	if request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/agent-compose/workspaces/") && strings.HasSuffix(request.URL.Path, "/upload") {
		return rawHTTPRoutePolicy{access: controlauth.AccessOperation, action: "workspace.upload", resourceType: "workspace"}, true
	}
	if (request.Method == http.MethodPut || request.Method == http.MethodDelete) && strings.HasPrefix(request.URL.Path, "/api/webhook-sources/") {
		action := "webhook-source.update"
		if request.Method == http.MethodDelete {
			action = "webhook-source.delete"
		}
		return rawHTTPRoutePolicy{access: controlauth.AccessOperation, action: action, resourceType: "webhook-source"}, true
	}
	return rawHTTPRoutePolicy{}, false
}

func rawHTTPResource(c echo.Context, resourceType string) controlauth.Resource {
	id := strings.TrimSpace(c.Param("workspaceID"))
	if id == "" {
		id = strings.TrimSpace(c.Param("source_id"))
	}
	return controlauth.Resource{Type: resourceType, ID: id}
}

func httpStatusForError(err error) int {
	if httpErr, ok := err.(*echo.HTTPError); ok {
		return httpErr.Code
	}
	return http.StatusInternalServerError
}
