package loaders

import "time"

type LoaderRunPageFilter struct {
	LoaderIDs       []string
	RequireTrigger  bool
	TriggerID       string
	Status          string
	BeforeStartedAt time.Time
	BeforeLoaderID  string
	BeforeRunID     string
	Limit           int
}

type LoaderRunKey struct {
	LoaderID string
	RunID    string
}

type LoaderEventPageFilter struct {
	LoaderIDs       []string
	RequireTrigger  bool
	TriggerID       string
	RunID           string
	BeforeCreatedAt time.Time
	BeforeLoaderID  string
	BeforeEventID   string
	Limit           int
}
