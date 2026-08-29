package plugin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// configFieldsFromSchema 把一个扁平 JSON-Schema（properties 按字段名键）解析成
// 前端可渲染的 config field 列表。reserved 中的 key 会被跳过——它们由宿主专用
// 表单区渲染，不重复出现（避免 label-only 空壳）。
//
// 字段类型映射（与前端 SchemaFieldInput 约定一致）：
//
//	string           → text input        (secret:true → password input)
//	boolean          → switch
//	integer / number → number input
//	array / string[] → comma-separated text input
//	enum present     → select（options 由 enum 值派生）
func configFieldsFromSchema(schema map[string]any, reserved map[string]struct{}) []types.WebSearchProviderConfigField {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return nil
	}
	requiredSet := map[string]struct{}{}
	if raw, ok := schema["required"].([]any); ok {
		for _, item := range raw {
			if key, ok := item.(string); ok {
				requiredSet[strings.TrimSpace(key)] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]types.WebSearchProviderConfigField, 0, len(keys))
	for _, key := range keys {
		if _, isReserved := reserved[key]; isReserved {
			continue
		}
		property, _ := properties[key].(map[string]any)
		if property == nil {
			continue
		}
		title, _ := property["title"].(string)
		description, _ := property["description"].(string)
		jsonType, _ := property["type"].(string)
		secret, _ := property["secret"].(bool)
		enum, _ := property["enum"].([]any)
		_, isRequired := requiredSet[key]
		fields = append(fields, types.WebSearchProviderConfigField{
			Key:         key,
			Label:       title,
			Type:        configFieldType(jsonType, secret, len(enum) > 0),
			Required:    isRequired,
			Default:     configFieldDefaultString(property["default"]),
			Description: description,
			Options:     configEnumOptions(enum),
		})
	}
	return fields
}

// configFieldsFromLegacy 把简洁的 config 列表转换成 ConfigFields，跳过 reserved
// key。Label 回退到 Description，因为 legacy 列表没有单独的 title。
func configFieldsFromLegacy(config []ConfigField, reserved map[string]struct{}) []types.WebSearchProviderConfigField {
	fields := make([]types.WebSearchProviderConfigField, 0, len(config))
	for _, field := range config {
		key := strings.TrimSpace(field.Key)
		if _, isReserved := reserved[key]; isReserved {
			continue
		}
		enum := make([]any, len(field.Enum))
		for i, value := range field.Enum {
			enum[i] = value
		}
		fields = append(fields, types.WebSearchProviderConfigField{
			Key:         key,
			Label:       field.Description,
			Type:        configFieldType(field.Type, field.Secret, len(field.Enum) > 0),
			Required:    field.Required,
			Default:     configFieldDefaultString(field.Default),
			Description: field.Description,
			Options:     configEnumOptions(enum),
		})
	}
	return fields
}

// configFieldType 把一个类型声明归一化为前端的渲染意图。enum 永远优先（渲染为
// 下拉框）。
func configFieldType(declaredType string, secret bool, hasEnum bool) string {
	if hasEnum {
		return "select"
	}
	switch declaredType {
	case "boolean":
		return "boolean"
	case "integer", "number":
		return "number"
	case "array", "string[]":
		return "array"
	}
	if secret {
		return "secret"
	}
	return "string"
}

func configEnumOptions(values []any) []types.WebSearchProviderConfigFieldOption {
	if len(values) == 0 {
		return nil
	}
	options := make([]types.WebSearchProviderConfigFieldOption, 0, len(values))
	for _, value := range values {
		text := fmt.Sprint(value)
		options = append(options, types.WebSearchProviderConfigFieldOption{Label: text, Value: text})
	}
	return options
}

func configFieldDefaultString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
