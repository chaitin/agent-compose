package projects

import "fmt"

// eventPromptPayloadLimit caps the serialized payload a declarative event
// trigger appends to its prompt, in JavaScript string units (UTF-16 code
// units). Webhook bodies are usually well below it; anything larger is cut and
// marked so the agent knows it is reading a prefix.
const eventPromptPayloadLimit = 64 * 1024

// eventPromptFormatter is a JavaScript function expression, evaluated inside a
// declarative event trigger's callback, that renders the triggering event as a
// block appended to the declared prompt:
//
//	<trigger-event topic="webhook.github.push">
//	{"ref":"refs/heads/main",...}
//	</trigger-event>
//
// Bus delivery wraps the payload in a {topic, createdAt, payload} envelope,
// while StartSchedulerRun hands the callback the raw request payload. The
// envelope is unwrapped and the raw payload is paired with the declared topic,
// so the agent sees the same block for either path. A run without a payload
// gets the declared prompt unchanged.
var eventPromptFormatter = fmt.Sprintf(`function(topic, event) {
    if (event === undefined || event === null) { return ""; }
    var payload = event;
    if (typeof event === "object" && !Array.isArray(event) && typeof event.topic === "string" &&
        Object.keys(event).sort().join(",") === "createdAt,payload,topic") {
      topic = event.topic;
      payload = event.payload;
    }
    var json = JSON.stringify(payload === undefined ? null : payload);
    var attributes = " topic=\"" + topic + "\"";
    if (json.length > %[1]d) {
      attributes += " truncated=\"true\" original-length=\"" + json.length + "\"";
      var cut = %[1]d;
      var last = json.charCodeAt(cut - 1);
      if (last >= 0xd800 && last <= 0xdbff) { cut -= 1; }
      json = json.slice(0, cut);
    }
    json = json.split("</trigger-event").join("<\\/trigger-event");
    return "\n\n<trigger-event" + attributes + ">\n" + json + "\n</trigger-event>";
  }`, eventPromptPayloadLimit)

// eventTriggerPromptExpression returns the JavaScript expression a declarative
// event trigger passes to scheduler.agent: the declared prompt followed by the
// callback's event rendered by eventPromptFormatter.
func eventTriggerPromptExpression(prompt, topic string) string {
	return fmt.Sprintf("%s + (%s)(%s, event)", JSStringLiteral(prompt), eventPromptFormatter, JSStringLiteral(topic))
}
