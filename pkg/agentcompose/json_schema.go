package agentcompose

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
)

func validateJSONSchemaDocument(valueJSON, schemaJSON, valueName string) error {
	schemaJSON = strings.TrimSpace(schemaJSON)
	if schemaJSON == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", valueName, err)
	}
	var schema any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Errorf("%s schema must be valid JSON: %w", valueName, err)
	}
	return validateJSONSchemaValue(value, schema, valueName)
}

func validateJSONSchemaValue(value any, schema any, path string) error {
	rule, ok := schema.(map[string]any)
	if !ok || rule == nil {
		return fmt.Errorf("%s schema must be a JSON object", path)
	}
	if typeName, ok := rule["type"].(string); ok && !jsonSchemaTypeMatches(value, typeName) {
		return fmt.Errorf("%s must be %s", path, typeName)
	}
	if enumValues, ok := rule["enum"].([]any); ok {
		matched := false
		for _, item := range enumValues {
			if reflect.DeepEqual(item, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must match one of the allowed enum values", path)
		}
	}
	typeName, _ := rule["type"].(string)
	objectValue, isObject := value.(map[string]any)
	if typeName == "object" || (typeName == "" && isObject) {
		if !isObject {
			return fmt.Errorf("%s must be object", path)
		}
		if required, ok := rule["required"].([]any); ok {
			for _, item := range required {
				key, ok := item.(string)
				if !ok {
					continue
				}
				if _, exists := objectValue[key]; !exists {
					return fmt.Errorf("%s.%s is required", path, key)
				}
			}
		}
		properties, _ := rule["properties"].(map[string]any)
		for key, childSchema := range properties {
			if childValue, exists := objectValue[key]; exists {
				if err := validateJSONSchemaValue(childValue, childSchema, path+"."+key); err != nil {
					return err
				}
			}
		}
		if additional, ok := rule["additionalProperties"].(bool); ok && !additional {
			for key := range objectValue {
				if _, exists := properties[key]; !exists {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
			}
		}
	}
	arrayValue, isArray := value.([]any)
	if typeName == "array" || (typeName == "" && isArray) {
		if !isArray {
			return fmt.Errorf("%s must be array", path)
		}
		if itemSchema, ok := rule["items"]; ok {
			for index, item := range arrayValue {
				if err := validateJSONSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func jsonSchemaTypeMatches(value any, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && math.Trunc(number) == number
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}
