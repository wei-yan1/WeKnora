package plugin

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

// RegisterExternalWebSearch connects a lifecycle runtime to the existing
// tenant-scoped web search registry. Provider parameters are kept at the
// factory boundary, so one long-lived plugin process can serve many tenants
// without sharing credentials between provider instances.
func RegisterExternalWebSearch(
	manager *Manager,
	registry *infraWebSearch.Registry,
	manifest Manifest,
	runtime Runtime,
	lazyStart bool,
) (string, error) {
	if manifest.ExtensionType != ExtensionSearch {
		return "", fmt.Errorf("plugin %q is not a web search provider", manifest.ID)
	}
	if registry == nil {
		return "", fmt.Errorf("web search registry is nil")
	}
	provider, ok := runtime.(connProvider)
	if !ok {
		return "", fmt.Errorf("runtime for web search plugin %q does not expose a gRPC connection", manifest.ID)
	}

	providerType := manifest.ID
	if manifest.Metadata != nil {
		if value, ok := manifest.Metadata["provider_type"].(string); ok && strings.TrimSpace(value) != "" {
			providerType = strings.TrimSpace(value)
		}
	}
	if err := manager.Register(manifest, runtime); err != nil {
		return "", err
	}
	info := webSearchProviderTypeInfo(manifest, providerType)
	if err := registry.RegisterWithInfo(providerType, func(params types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) {
		if lazyStart {
			if err := manager.Start(context.Background(), manifest.ID); err != nil {
				return nil, err
			}
		}
		conn := provider.Conn()
		if conn == nil {
			return nil, fmt.Errorf("web search plugin %q is not running", manifest.ID)
		}
		client := pluginapi.NewWebSearchPluginClient(conn)
		return &GRPCWebSearchProxy{Client: client, Params: params, NameValue: providerType, Manager: manager, PluginID: manifest.ID}, nil
	}, info); err != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", err
	}
	// A plugin-bundled icon is served by the host from the plugin directory;
	// record its absolute path so the icon endpoint can stream it.
	if iconFile, ok := resolveLocalIconFile(manifest.SourceDir, manifestIconValue(manifest)); ok {
		registry.RegisterIconFile(providerType, iconFile)
	}
	return providerType, nil
}

func webSearchProviderTypeInfo(manifest Manifest, providerType string) types.WebSearchProviderTypeInfo {
	info := types.WebSearchProviderTypeInfo{ID: providerType, Name: manifest.Name, Description: manifest.Name}
	if manifest.Metadata != nil {
		if value, ok := manifest.Metadata["description"].(string); ok && strings.TrimSpace(value) != "" {
			info.Description = strings.TrimSpace(value)
		}
		if value, ok := manifest.Metadata["docs_url"].(string); ok {
			info.DocsURL = strings.TrimSpace(value)
		}
		if icon := manifestIconValue(manifest); icon != "" {
			if _, isLocal := resolveLocalIconFile(manifest.SourceDir, icon); isLocal {
				// A plugin-bundled icon is served by the host at a stable URL.
				info.Icon = webSearchProviderIconURL(providerType)
			} else {
				info.Icon = icon
			}
		}
	}
	// Capability flags come from the four reserved keys only; they drive the
	// dedicated host-side form sections (API key box, base URL box, proxy box).
	for _, field := range manifest.Config {
		switch strings.TrimSpace(field.Key) {
		case "api_key":
			info.RequiresAPIKey = field.Required
		case "engine_id":
			info.RequiresEngineID = field.Required
		case "base_url":
			info.RequiresBaseURL = field.Required
		case "proxy_url":
			info.SupportsProxy = true
		}
	}
	// ConfigFields carry the provider's *additional* parameters. Prefer the
	// JSON-Schema declaration (full typing: title/description/secret/enum) and
	// fall back to the legacy config list. Reserved keys never surface as
	// custom fields — they already render through dedicated form sections.
	if len(manifest.ConfigSchema) > 0 {
		info.ConfigFields = webSearchConfigFieldsFromSchema(manifest.ConfigSchema)
	} else {
		info.ConfigFields = webSearchConfigFieldsFromLegacy(manifest.Config)
	}
	return info
}

// manifestIconValue returns the raw metadata.icon value declared by a plugin
// manifest, or "" when absent.
func manifestIconValue(manifest Manifest) string {
	if manifest.Metadata == nil {
		return ""
	}
	if v, ok := manifest.Metadata["icon"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// webSearchProviderIconURL returns the host route that streams a
// plugin-bundled icon for the given provider type. Keep in sync with the route
// registered for WebSearchProviderHandler.GetProviderIcon.
func webSearchProviderIconURL(providerType string) string {
	return "/api/v1/web-search-providers/icon/" + url.PathEscape(providerType)
}

// webSearchReservedKeys are the config keys with dedicated host-side form
// sections and request mapping (api_key → Parameters.APIKey, and so on).
// They drive capability flags only and never surface as custom ConfigFields,
// so the frontend never renders them twice.
var webSearchReservedKeys = map[string]struct{}{
	"api_key":   {},
	"engine_id": {},
	"base_url":  {},
	"proxy_url": {},
}

// webSearchConfigFieldsFromSchema converts a flat JSON-Schema declaration
// (properties keyed by field name) into frontend-renderable ConfigFields.
//
// Field typing:
//
//	string           → text input        (secret:true → password input)
//	boolean          → switch
//	integer / number → number input
//	array / string[] → comma-separated text input
//	enum present     → select (options derived from the enum values)
//
// Values persist in ExtraConfig as STRINGS regardless of type; the plugin
// parses them on its side. Sensitive credentials must use the reserved
// api_key key — extra fields are stored in plaintext by design.
func webSearchConfigFieldsFromSchema(schema map[string]any) []types.WebSearchProviderConfigField {
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
		if _, reserved := webSearchReservedKeys[key]; reserved {
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
			Type:        webSearchConfigFieldType(jsonType, secret, len(enum) > 0),
			Required:    isRequired,
			Default:     configFieldDefaultString(property["default"]),
			Description: description,
			Options:     webSearchEnumOptions(enum),
		})
	}
	return fields
}

// webSearchConfigFieldsFromLegacy converts the concise config list into
// ConfigFields, skipping reserved keys (they render through dedicated
// host-side form sections). Label falls back to Description because the
// legacy list has no separate title.
func webSearchConfigFieldsFromLegacy(config []ConfigField) []types.WebSearchProviderConfigField {
	fields := make([]types.WebSearchProviderConfigField, 0, len(config))
	for _, field := range config {
		key := strings.TrimSpace(field.Key)
		if _, reserved := webSearchReservedKeys[key]; reserved {
			continue
		}
		enum := make([]any, len(field.Enum))
		for i, value := range field.Enum {
			enum[i] = value
		}
		fields = append(fields, types.WebSearchProviderConfigField{
			Key:         key,
			Label:       field.Description,
			Type:        webSearchConfigFieldType(field.Type, field.Secret, len(field.Enum) > 0),
			Required:    field.Required,
			Default:     configFieldDefaultString(field.Default),
			Description: field.Description,
			Options:     webSearchEnumOptions(enum),
		})
	}
	return fields
}

// webSearchConfigFieldType normalizes a type declaration into the frontend
// rendering intent. An enum always wins (rendered as a select).
func webSearchConfigFieldType(declaredType string, secret bool, hasEnum bool) string {
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

func webSearchEnumOptions(values []any) []types.WebSearchProviderConfigFieldOption {
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
