package plugin

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
)

// EffectiveConfigSchema returns the explicit schema, or derives the same
// contract from the legacy config: []ConfigField declaration.
func EffectiveConfigSchema(manifest Manifest) map[string]any {
	if len(manifest.ConfigSchema) > 0 {
		return manifest.ConfigSchema
	}
	if len(manifest.Config) == 0 {
		return nil
	}
	settingsProperties := make(map[string]any, len(manifest.Config))
	required := make([]any, 0)
	for _, field := range manifest.Config {
		property := map[string]any{"type": field.Type}
		if field.Description != "" {
			property["description"] = field.Description
		}
		if field.Secret {
			property["secret"] = true
		}
		if field.Default != nil {
			property["default"] = field.Default
		}
		if len(field.Enum) > 0 {
			values := make([]any, len(field.Enum))
			for i := range field.Enum {
				values[i] = field.Enum[i]
			}
			property["enum"] = values
		}
		settingsProperties[field.Key] = property
		if field.Required {
			required = append(required, field.Key)
		}
	}
	// DataSourceConfig has a stable outer envelope. Connector-specific fields
	// live under settings, while credentials remain a separate secret map.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"settings": map[string]any{"type": "object", "properties": settingsProperties},
		},
	}
	if len(required) > 0 {
		schema["properties"].(map[string]any)["settings"].(map[string]any)["required"] = required
	}
	return schema
}

// ValidateConfigSchema validates the small, portable subset of JSON Schema
// needed by plugin manifests. An absent schema intentionally means any config
// is accepted for backward compatibility.
func ValidateConfigSchema(schema map[string]any, config map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	if err := validateSchemaValue("config", schema, config); err != nil {
		return fmt.Errorf("invalid plugin config: %w", err)
	}
	return nil
}

func validateSchemaValue(path string, schema map[string]any, value any) error {
	if expected, ok := schema["type"].(string); ok && !jsonTypeMatches(expected, value) {
		return fmt.Errorf("%s must be %s", path, expected)
	}
	if t, _ := schema["type"].(string); t == "url" {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			if err := datasource.ValidateConnectorBaseURL(s); err != nil {
				return fmt.Errorf("%s failed SSRF validation: %w", path, err)
			}
		}
	}
	if enum, ok := schema["enum"].([]any); ok && value != nil {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must be one of the declared enum values", path)
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				key, _ := raw.(string)
				if strings.TrimSpace(key) != "" && (object[key] == nil || (fmt.Sprint(object[key]) == "")) {
					return fmt.Errorf("%s.%s is required", path, key)
				}
			}
		}
		additional, hasAdditional := schema["additionalProperties"].(bool)
		for key, child := range object {
			raw, known := properties[key]
			if !known {
				if hasAdditional && !additional {
					return fmt.Errorf("%s.%s is not declared", path, key)
				}
				continue
			}
			childSchema, ok := raw.(map[string]any)
			if ok {
				if err := validateSchemaValue(path+"."+key, childSchema, child); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func jsonTypeMatches(expected string, value any) bool {
	if value == nil {
		return expected != "null"
	}
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array", "string[]":
		kind := reflect.ValueOf(value).Kind()
		if kind != reflect.Array && kind != reflect.Slice {
			return false
		}
		if expected == "string[]" {
			for i := 0; i < reflect.ValueOf(value).Len(); i++ {
				if _, ok := reflect.ValueOf(value).Index(i).Interface().(string); !ok {
					return false
				}
			}
		}
		return true
	case "string", "directory", "path", "url":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			return value.(float64) == float64(int64(value.(float64)))
		default:
			return false
		}
	case "number":
		kind := reflect.ValueOf(value).Kind()
		return kind >= reflect.Int && kind <= reflect.Float64
	default:
		return true
	}
}
