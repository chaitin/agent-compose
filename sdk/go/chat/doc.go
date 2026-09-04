// Package chat is a client for holding a conversation with an agent-compose
// Agent.
//
// The vocabulary is conversational on purpose. A [Conversation] is a durable
// thread of turns with one Agent; [Conversation.Send] contributes a message and
// returns a [Reply] that streams the agent's answer. Nothing in this package
// exposes the daemon's Run, Sandbox, or attach-frame vocabulary; code that
// needs those should use the lower level run client instead.
//
// A Conversation keeps its environment across turns, so the agent retains
// whatever context it persisted there. When that environment is gone and the
// conversation had to be rebuilt, [Conversation.Continuity] reports
// [Restarted] rather than silently starting over.
//
// # Lifetime
//
// [Conversation.Close] releases local resources and leaves the conversation
// intact on the server; a later [Agent.Open] resumes it. Only
// [Conversation.Delete] ends it. Disconnecting never cancels work in progress:
// an agent that is still working when the caller goes away keeps working, and
// the events it produced meanwhile are available from
// [Conversation.History] on return.
//
// A Client is safe for concurrent use. A Conversation is not, and permits one
// Reply in flight at a time.
package chat
