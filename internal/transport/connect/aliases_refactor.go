package connecttransport

import (
	appdomain "agent-compose/internal/app"
	imagedomain "agent-compose/internal/image"
	modeldomain "agent-compose/internal/model"
	sqlitestore "agent-compose/internal/persistence/sqlite"
)

type Service = appdomain.Service
type Store = appdomain.Store
type ConfigStore = appdomain.ConfigStore
type Session = appdomain.Session
type SessionEnvVar = appdomain.SessionEnvVar
type SessionTag = appdomain.SessionTag
type SessionSummary = appdomain.SessionSummary
type SessionWorkspace = appdomain.SessionWorkspace
type SessionListOptions = modeldomain.SessionListOptions
type CapabilityGatewaySettings = sqlitestore.CapabilityGatewaySettings

type ImageListRequest = imagedomain.ImageListRequest
type ImageListResult = imagedomain.ImageListResult
type ImagePullRequest = imagedomain.ImagePullRequest
type ImagePullResult = imagedomain.ImagePullResult
type ImageInspectRequest = imagedomain.ImageInspectRequest
type ImageInspectResult = imagedomain.ImageInspectResult
type ImageRemoveRequest = imagedomain.ImageRemoveRequest
type ImageRemoveResult = imagedomain.ImageRemoveResult
