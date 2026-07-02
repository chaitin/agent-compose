package httpapi

import "github.com/labstack/echo/v4"

type WebhookHandlers struct {
	HandleWebhook          echo.HandlerFunc
	HandleListSources      echo.HandlerFunc
	HandlePutSource        echo.HandlerFunc
	HandleDeleteSource     echo.HandlerFunc
	HandleListEvents       echo.HandlerFunc
	HandleGetEventSessions echo.HandlerFunc
	HandleGetEventRuns     echo.HandlerFunc
	HandleGetEvent         echo.HandlerFunc
}

func RegisterWebhookRoutes(app *echo.Echo, h WebhookHandlers) {
	app.POST("/api/webhooks/:topic", h.HandleWebhook)
	app.GET("/api/webhook-sources", h.HandleListSources)
	app.PUT("/api/webhook-sources/:source_id", h.HandlePutSource)
	app.DELETE("/api/webhook-sources/:source_id", h.HandleDeleteSource)
	app.GET("/api/events", h.HandleListEvents)
	app.GET("/api/events/:event_id/sessions", h.HandleGetEventSessions)
	app.GET("/api/events/:event_id/runs", h.HandleGetEventRuns)
	app.GET("/api/events/:event_id", h.HandleGetEvent)
}

type RuntimeLLMHandlers struct {
	HandleResponses         echo.HandlerFunc
	HandleChatCompletions   echo.HandlerFunc
	HandleAnthropicMessages echo.HandlerFunc
}

func RegisterRuntimeLLMFacadeRoutes(app *echo.Echo, h RuntimeLLMHandlers) {
	app.POST("/api/runtime/sessions/:session_id/llm/openai/v1/responses", h.HandleResponses)
	app.POST("/api/runtime/sessions/:session_id/llm/openai/v1/chat/completions", h.HandleChatCompletions)
	app.POST("/api/runtime/sessions/:session_id/llm/anthropic/v1/messages", h.HandleAnthropicMessages)
}

type WorkspaceHandlers struct {
	HandleListFiles echo.HandlerFunc
	HandleUpload    echo.HandlerFunc
	HandleDownload  echo.HandlerFunc
}

func RegisterWorkspaceRoutes(app *echo.Echo, h WorkspaceHandlers) {
	base := "/api/agent-compose/workspaces"
	app.GET(base+"/:workspaceID/files", h.HandleListFiles)
	app.POST(base+"/:workspaceID/upload", h.HandleUpload)
	app.GET(base+"/:workspaceID/download", h.HandleDownload)
}
