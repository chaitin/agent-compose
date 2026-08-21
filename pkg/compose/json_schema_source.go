package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"agent-compose/pkg/sources"

	"gopkg.in/yaml.v3"
)

// JSONSchemaSource accepts either an inline JSON Schema or the same source
// descriptor shape used by scheduler.script. A mapping containing provider is
// treated as a source descriptor; every other mapping and boolean is inline.
type JSONSchemaSource struct {
	Inline *JSONSchema
	Source sources.Source
}

func (s JSONSchemaSource) IsZero() bool { return s.Inline == nil && !s.Source.HasContent() }

func (s *JSONSchemaSource) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode && mappingHasSourceProvider(value) {
		var source sources.Source
		if err := value.Decode(&source); err != nil {
			return err
		}
		*s = JSONSchemaSource{Source: source}
		return nil
	}
	if value.Kind != yaml.MappingNode && (value.Kind != yaml.ScalarNode || (value.Tag != "!!bool" && value.Tag != "!!null")) {
		return fmt.Errorf("JSON Schema must be an object or boolean")
	}
	if value.Tag == "!!null" {
		return fmt.Errorf("JSON Schema must not be null")
	}
	var decoded any
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	data, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("encode JSON Schema: %w", err)
	}
	schema := JSONSchema(data)
	*s = JSONSchemaSource{Inline: &schema}
	return nil
}

func (s JSONSchemaSource) MarshalYAML() (any, error) {
	if s.Source.HasContent() {
		return s.Source, nil
	}
	if s.Inline == nil {
		return nil, nil
	}
	return s.Inline.yamlValue()
}

// JSONSchema stores a normalized JSON representation while preserving boolean
// schemas and arbitrary extension keywords.
type JSONSchema json.RawMessage

func (s JSONSchema) MarshalJSON() ([]byte, error) { return bytes.Clone(s), nil }

func (s *JSONSchema) UnmarshalJSON(data []byte) error {
	canonical, err := canonicalJSONSchemaDocument(data)
	if err != nil {
		return err
	}
	*s = canonical
	return nil
}

func (s JSONSchema) MarshalYAML() (any, error) { return s.yamlValue() }

func (s JSONSchema) yamlValue() (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(s))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return jsonNumbersForYAML(value), nil
}

func jsonNumbersForYAML(value any) any {
	switch value := value.(type) {
	case json.Number:
		tag := "!!int"
		if bytes.ContainsAny([]byte(value), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value.String()}
	case map[string]any:
		for key, item := range value {
			value[key] = jsonNumbersForYAML(item)
		}
		return value
	case []any:
		for i := range value {
			value[i] = jsonNumbersForYAML(value[i])
		}
		return value
	default:
		return value
	}
}

func validateJSONSchemaDocument(data []byte) error {
	_, err := canonicalJSONSchemaDocument(data)
	return err
}

func canonicalJSONSchemaDocument(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON Schema: %w", err)
	}
	switch value.(type) {
	case map[string]any, bool:
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode JSON Schema: %w", err)
		}
		return canonical, nil
	default:
		return nil, fmt.Errorf("JSON Schema must be an object or boolean")
	}
}

func mappingHasKey(node *yaml.Node, key string) bool {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

func mappingHasSourceProvider(node *yaml.Node) bool {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "provider" {
			continue
		}
		value := node.Content[i+1]
		return value.Kind == yaml.ScalarNode && (value.Value == "file" || value.Value == "http" || value.Value == "git")
	}
	return false
}

func validateJSONSchemaSource(node *yaml.Node, path string) error {
	if node.Kind == yaml.MappingNode && mappingHasSourceProvider(node) {
		return validateMapping(node, path, sourceFieldValidators(nil))
	}
	if node.Kind == yaml.MappingNode || (node.Kind == yaml.ScalarNode && node.Tag == "!!bool") {
		return nil
	}
	return newParseError(node, path, "expected a JSON Schema object or boolean, or a source mapping")
}

func normalizeJSONSchemaSource(path string, source JSONSchemaSource, options NormalizeOptions) (*JSONSchema, *sources.Source, error) {
	if source.IsZero() {
		return nil, nil, nil
	}
	if source.Inline != nil {
		if err := validateJSONSchemaDocument(*source.Inline); err != nil {
			return nil, nil, &ValidationError{Path: path, Message: err.Error()}
		}
		cloned := JSONSchema(bytes.Clone(*source.Inline))
		return &cloned, nil, nil
	}
	normalizedSource, err := normalizeSchedulerScriptSource(path, source.Source, options)
	if err != nil {
		return nil, nil, err
	}
	if !options.ResolveSchemaURLs {
		return nil, &normalizedSource, nil
	}
	resolver := options.ScriptSourceResolver
	if resolver == nil {
		resolver = NewDefaultScriptSourceResolver(options.Env)
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	content, err := resolver.Resolve(ctx, normalizedSource)
	if err != nil {
		return nil, nil, &ValidationError{Path: path, Message: err.Error()}
	}
	canonical, err := canonicalJSONSchemaDocument(content)
	if err != nil {
		return nil, nil, &ValidationError{Path: path, Message: err.Error()}
	}
	schema := JSONSchema(canonical)
	return &schema, nil, nil
}
