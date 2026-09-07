package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// legacySourceLoader is the retired spelling of the scheduler source; stored
// events written before the rename still carry it.
const legacySourceLoader = "loader"

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var httpHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func ValidateTopicName(topic string) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("topic is required")
	}
	if len(topic) > 128 {
		return fmt.Errorf("topic is too long")
	}
	if !topicEventNamePattern.MatchString(topic) {
		return fmt.Errorf("topic contains invalid characters")
	}
	return nil
}

func NormalizeHTTPHeaderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !httpHeaderNamePattern.MatchString(name) {
		return "", fmt.Errorf("header name contains invalid characters")
	}
	return name, nil
}

func NormalizeSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case domain.TopicEventSourceWebhook:
		return domain.TopicEventSourceWebhook
	case domain.TopicEventSourceScheduler, legacySourceLoader:
		return domain.TopicEventSourceScheduler
	case domain.TopicEventSourceSystem:
		return domain.TopicEventSourceSystem
	default:
		return ""
	}
}

// SourceFilterValues returns every persisted source value that is
// equivalent to source. Loader was renamed to scheduler, but upgraded databases
// intentionally retain loader-era event rows, so scheduler filters must match
// both stored values.
func SourceFilterValues(source string) []string {
	normalized := NormalizeSource(source)
	switch normalized {
	case "":
		return nil
	case domain.TopicEventSourceScheduler:
		return []string{domain.TopicEventSourceScheduler, legacySourceLoader}
	default:
		return []string{normalized}
	}
}

func NormalizeDispatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", domain.TopicEventDispatchPending:
		return domain.TopicEventDispatchPending
	case domain.TopicEventDispatchPublishing:
		return domain.TopicEventDispatchPublishing
	case domain.TopicEventDispatchPublishedToBus:
		return domain.TopicEventDispatchPublishedToBus
	case domain.TopicEventDispatchNoSubscriber:
		return domain.TopicEventDispatchNoSubscriber
	case domain.TopicEventDispatchRetrying:
		return domain.TopicEventDispatchRetrying
	case domain.TopicEventDispatchDeadLetter:
		return domain.TopicEventDispatchDeadLetter
	case domain.TopicEventDispatchCanceled:
		return domain.TopicEventDispatchCanceled
	default:
		return ""
	}
}

func NormalizeDeliveryStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.EventDeliveryStatusMatched:
		return domain.EventDeliveryStatusMatched
	case domain.EventDeliveryStatusRunStarted:
		return domain.EventDeliveryStatusRunStarted
	case domain.EventDeliveryStatusRunSucceeded:
		return domain.EventDeliveryStatusRunSucceeded
	case domain.EventDeliveryStatusRunFailed:
		return domain.EventDeliveryStatusRunFailed
	case domain.EventDeliveryStatusSkipped:
		return domain.EventDeliveryStatusSkipped
	default:
		return ""
	}
}

func PayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}
