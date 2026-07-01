package llm

import (
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
)

const (
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed
)

type ConfigStore = storage.ConfigStore
type Store = storage.Store
type Session = model.Session
type SessionEnvVar = model.SessionEnvVar
type LLMProvider = model.LLMProvider
type LLMModel = model.LLMModel
type LLMResolvedTarget = model.LLMResolvedTarget
type LLMFacadeToken = model.LLMFacadeToken
