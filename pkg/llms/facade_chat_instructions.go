package llms

import (
	"encoding/json"
	"strings"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

// Chat templates can require a single initial system message. Keep instruction
// order while leaving the user/assistant/tool conversation in its original order.
func chatInstructionPrefix(prompt []protocolbridge.Message) ([]protocolbridge.Message, bool) {
	var instructions []protocolbridge.Message
	var conversation []protocolbridge.Message
	for _, message := range prompt {
		if message.Role == protocolbridge.RoleSystem || message.Role == protocolbridge.RoleDeveloper {
			instructions = append(instructions, message)
		} else {
			conversation = append(conversation, message)
		}
	}
	if len(instructions) == 0 || (len(instructions) == 1 && prompt[0].Role == protocolbridge.RoleSystem) {
		return prompt, false
	}
	var parts []protocolbridge.Part
	for index, message := range instructions {
		if index > 0 {
			parts = append(parts, protocolbridge.Part{Type: protocolbridge.PartText, Text: &protocolbridge.TextPart{Text: "\n\n"}})
		}
		parts = append(parts, message.Parts...)
	}
	initial := instructions[0]
	initial.Role, initial.Parts = protocolbridge.RoleSystem, parts
	return append([]protocolbridge.Message{initial}, conversation...), true
}

func rawChatInstructionPrefix(messages []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
	var instructions []map[string]json.RawMessage
	var conversation []map[string]json.RawMessage
	initialSystem := false
	for index, message := range messages {
		var role string
		if err := json.Unmarshal(message["role"], &role); err == nil && role == string(protocolbridge.RoleSystem) {
			instructions = append(instructions, message)
			initialSystem = initialSystem || index == 0
		} else {
			conversation = append(conversation, message)
		}
	}
	if len(instructions) == 0 || (len(instructions) == 1 && initialSystem) {
		return messages, false
	}
	var text []string
	var parts []json.RawMessage
	for index, message := range instructions {
		if index > 0 {
			parts = append(parts, json.RawMessage(`{"type":"text","text":"\n\n"}`))
		}
		var value string
		if err := json.Unmarshal(message["content"], &value); err == nil {
			text = append(text, value)
			part, err := json.Marshal(map[string]string{"type": "text", "text": value})
			if err != nil {
				return messages, false
			}
			parts = append(parts, part)
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(message["content"], &blocks); err != nil {
			return messages, false
		}
		parts = append(parts, blocks...)
	}
	var content []byte
	var err error
	if len(text) == len(instructions) {
		content, err = json.Marshal(strings.Join(text, "\n\n"))
	} else {
		content, err = json.Marshal(parts)
	}
	if err != nil {
		return messages, false
	}
	initial := make(map[string]json.RawMessage, len(instructions[0]))
	for key, value := range instructions[0] {
		initial[key] = value
	}
	initial["content"] = content
	return append([]map[string]json.RawMessage{initial}, conversation...), true
}
