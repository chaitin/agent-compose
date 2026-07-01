package events

import (
	"agent-compose/pkg/bus"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
)

type ConfigStore = storage.ConfigStore
type LoaderBus = bus.LoaderBus
type LoaderTopicEvent = model.LoaderTopicEvent
type TopicEventRecord = model.TopicEventRecord
type TopicEventFilter = model.TopicEventFilter
type WebhookSource = model.WebhookSource
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink
type EventSessionTraceItem = model.EventSessionTraceItem

type Service struct {
	config   *appconfig.Config
	configDB *storage.ConfigStore
}

func NewService(config *appconfig.Config, configDB *storage.ConfigStore) *Service {
	return &Service{config: config, configDB: configDB}
}
