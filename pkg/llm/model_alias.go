package llm

import (
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
)

const (
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeManual = model.SessionTypeManual
)

type ConfigStore = storage.ConfigStore
type Store = storage.Store
type Session = model.Session
type SessionSummary = model.SessionSummary
type SessionEnvVar = model.SessionEnvVar
type LLMProvider = model.LLMProvider
type LLMModel = model.LLMModel
type LLMResolvedTarget = model.LLMResolvedTarget
type LLMFacadeToken = model.LLMFacadeToken
