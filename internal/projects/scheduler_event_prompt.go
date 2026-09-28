package projects

import (
	"fmt"
	"strconv"
	"strings"
)

// eventPromptPayloadLimit caps the payload text inside the block a declarative
// event trigger appends to its prompt, in JavaScript string units (UTF-16 code
// units). A webhook event carries the request headers, query, and body, so
// large bodies reach it; anything longer is cut and marked so the agent knows
// it is reading a prefix.
const eventPromptPayloadLimit = 64 * 1024

// eventPromptFormatterName is the script-level function that renders the
// triggering event. ProjectSchedulerTriggersAndScript defines it once in any
// script that has a trigger calling it.
const eventPromptFormatterName = "agentComposeTriggerEvent"

// eventPromptFormatterSource defines eventPromptFormatterName, which renders
// the event a declarative event trigger's callback receives as a block
// appended to the declared prompt:
//
//	<trigger-event topic="webhook.github.push">
//	{"body":{"ref":"refs/heads/main",...},"headers":{...},"topic":"webhook.github.push",...}
//	</trigger-event>
//
// Bus delivery wraps the payload in the {topic, createdAt, payload} envelope
// built by schedulers.TopicEventCallbackPayloadJSON, while StartSchedulerRun
// hands the callback the raw request payload. The envelope is unwrapped and
// the raw payload is paired with the declared topic, so the agent sees the
// same block for either path. The envelope is recognized by its exact shape,
// so a manual run replaying a stored run's payload_json renders like the
// original delivery. A run without a payload, or with an empty object (what a
// manual run without one sends), gets the declared prompt unchanged.
//
// Neither the topic nor the payload can end the block early: the topic is
// escaped as an attribute value, and "</trigger-event" in the payload JSON is
// written as "<\/trigger-event", which decodes to the same value.
var eventPromptFormatterSource = strings.ReplaceAll(`function agentComposeTriggerEvent(topic, event) {
  if (event === undefined || event === null) { return ""; }
  var payload = event;
  if (typeof event === "object" && !Array.isArray(event) && typeof event.topic === "string" &&
      typeof event.createdAt === "string" && Object.keys(event).sort().join(",") === "createdAt,payload,topic") {
    topic = event.topic;
    payload = event.payload;
  }
  if (payload === undefined || payload === null) { return ""; }
  if (typeof payload === "object" && !Array.isArray(payload) && Object.keys(payload).length === 0) { return ""; }
  var json = JSON.stringify(payload).split("</trigger-event").join("<\\/trigger-event");
  var attributes = " topic=\"" + String(topic).replace(/&/g, "&amp;").replace(/"/g, "&quot;")
    .replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/[\r\n]/g, " ") + "\"";
  if (json.length > LIMIT) {
    attributes += " truncated=\"true\" original-length=\"" + json.length + "\"";
    var cut = LIMIT;
    var last = json.charCodeAt(cut - 1);
    if (last >= 0xd800 && last <= 0xdbff) { cut -= 1; }
    json = json.slice(0, cut);
  }
  return "\n\n<trigger-event" + attributes + ">\n" + json + "\n</trigger-event>";
}
`, "LIMIT", strconv.Itoa(eventPromptPayloadLimit))

// eventTriggerPromptExpression returns the JavaScript expression a declarative
// event trigger passes to scheduler.agent: the declared prompt followed by the
// callback's event rendered by eventPromptFormatterSource.
func eventTriggerPromptExpression(prompt, topic string) string {
	return fmt.Sprintf("%s + %s(%s, event)", JSStringLiteral(prompt), eventPromptFormatterName, JSStringLiteral(topic))
}
