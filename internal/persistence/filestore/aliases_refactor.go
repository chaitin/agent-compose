package filestore

import (
	"context"

	modeldomain "agent-compose/internal/model"
)

type SessionTag = modeldomain.SessionTag
type SessionEnvVar = modeldomain.SessionEnvVar
type SessionSummary = modeldomain.SessionSummary
type SessionListOptions = modeldomain.SessionListOptions
type SessionListResult = modeldomain.SessionListResult
type SessionWorkspace = modeldomain.SessionWorkspace
type Session = modeldomain.Session
type NotebookCell = modeldomain.NotebookCell
type SessionEvent = modeldomain.SessionEvent
type AgentRun = modeldomain.AgentRun
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState
type CapabilityGatewaySettings = modeldomain.CapabilityGatewaySettings

type ConfigStore interface {
	GetCapabilityGateway(context.Context) (CapabilityGatewaySettings, error)
}

const (
	VMStatusPending = modeldomain.VMStatusPending
	VMStatusRunning = modeldomain.VMStatusRunning
	CellTypeAgent   = modeldomain.CellTypeAgent
	CellTypeShell   = modeldomain.CellTypeShell
)

const (
	capabilitySessionTokenEnvName = modeldomain.CapabilitySessionTokenEnvName
	capabilityCapsetTagName       = modeldomain.CapabilityCapsetTagName
)

var sessionMatchesListOptions = modeldomain.SessionMatchesListOptions
var normalizeSessionTriggerSource = modeldomain.NormalizeSessionTriggerSource
var normalizeSessionListBounds = modeldomain.NormalizeSessionListBounds
var paginateSessions = modeldomain.PaginateSessions
var normalizeCapsetIDs = modeldomain.NormalizeCapsetIDs
var firstNonEmpty = modeldomain.FirstNonEmpty
