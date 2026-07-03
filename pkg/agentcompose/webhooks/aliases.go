package webhooks

import owner "agent-compose/pkg/events/webhooks"

const (
	DefaultQueueName = owner.DefaultQueueName
)

type (
	AcceptedResponse       = owner.AcceptedResponse
	EventRunJSON           = owner.EventRunJSON
	EventRunsResponse      = owner.EventRunsResponse
	EventSessionJSON       = owner.EventSessionJSON
	EventSessionsResponse  = owner.EventSessionsResponse
	Reservation            = owner.Reservation
	RunQueue               = owner.RunQueue
	SourceJSON             = owner.SourceJSON
	SourceListResponse     = owner.SourceListResponse
	SourceRequest          = owner.SourceRequest
	SourceResponse         = owner.SourceResponse
	TopicEventJSON         = owner.TopicEventJSON
	TopicEventListResponse = owner.TopicEventListResponse
	TopicEventResponse     = owner.TopicEventResponse
)

var (
	BuildPayload             = owner.BuildPayload
	DecodeJSONObject         = owner.DecodeJSONObject
	EventRunsResponseFor     = owner.EventRunsResponseFor
	EventSessionsResponseFor = owner.EventSessionsResponseFor
	ExistingBodyHash         = owner.ExistingBodyHash
	ExtractCorrelationID     = owner.ExtractCorrelationID
	ExtractDeliveryID        = owner.ExtractDeliveryID
	ExtractIdempotencyKey    = owner.ExtractIdempotencyKey
	IntentFromBody           = owner.IntentFromBody
	NewRunQueue              = owner.NewRunQueue
	NewRunQueueFromConfig    = owner.NewRunQueueFromConfig
	NoopReservations         = owner.NoopReservations
	PresentedToken           = owner.PresentedToken
	ProviderFromTopic        = owner.ProviderFromTopic
	QueryValuesToMap         = owner.QueryValuesToMap
	ReadBody                 = owner.ReadBody
	RequestContentTypeIsJSON = owner.RequestContentTypeIsJSON
	SanitizeHeaders          = owner.SanitizeHeaders
	SourceToJSON             = owner.SourceToJSON
	TokenHash                = owner.TokenHash
	TopicEventToJSON         = owner.TopicEventToJSON
	ValidTokenHash           = owner.ValidTokenHash
	ValidateExternalTopic    = owner.ValidateExternalTopic
)
