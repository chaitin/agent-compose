package chat

import (
	"encoding/json"
	"time"
)

// Role identifies who produced a [Message].
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one contribution to a conversation.
type Message struct {
	// ID is stable for a message read back from history and empty for one just
	// streamed.
	ID string
	// Role is who produced the message.
	Role Role
	// Text is the message's content.
	Text string
	// Result carries the agent's structured output when its Agent declares an
	// output schema, and is nil otherwise. Assistant messages only.
	Result json.RawMessage
	// Time is when the daemon recorded the message.
	Time time.Time
}

// Continuity reports whether a conversation kept the environment its earlier
// turns ran in.
type Continuity string

const (
	// Continuous means the conversation resumed its original environment, so
	// whatever context the agent persisted there is still available.
	Continuous Continuity = "continuous"
	// Restarted means the environment was gone and had to be rebuilt. Earlier
	// turns remain readable through [Conversation.History], but the agent no
	// longer has the context it had accumulated. Products normally tell the
	// user about this.
	Restarted Continuity = "restarted"
)
