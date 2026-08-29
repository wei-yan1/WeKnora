package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Reserved keys drive capability flags and dedicated host-side form sections;
// they must never surface as custom ConfigFields (the frontend would render
// them twice — once as a real input, once as a label-only shell).
func TestWebSearchProviderTypeInfoExcludesReservedKeysFromConfigFields(t *testing.T) {
	manifest := Manifest{
		APIVersion: APIVersionV1, ID: "test.reserved", Name: "Reserved", Version: "1.0.0",
		ExtensionType: ExtensionSearch, ProtocolVersion: ProtocolVersionV1,
		Config: []ConfigField{
			{Key: "api_key", Type: "secret", Required: true},
			{Key: "base_url", Type: "string", Required: true},
			{Key: "proxy_url", Type: "string"},
			{Key: "search_depth", Type: "string", Description: "Search depth"},
		},
	}
	info := webSearchProviderTypeInfo(manifest, "test.reserved")
	require.True(t, info.RequiresAPIKey)
	require.True(t, info.RequiresBaseURL)
	require.True(t, info.SupportsProxy)
	require.Len(t, info.ConfigFields, 1)
	require.Equal(t, "search_depth", info.ConfigFields[0].Key)
	require.Equal(t, "Search depth", info.ConfigFields[0].Label)
}

func TestWebSearchProviderTypeInfoFromConfigSchema(t *testing.T) {
	manifest := Manifest{
		APIVersion: APIVersionV1, ID: "test.schema", Name: "Schema", Version: "1.0.0",
		ExtensionType: ExtensionSearch, ProtocolVersion: ProtocolVersionV1,
		Config: []ConfigField{
			{Key: "api_key", Type: "secret", Required: true},
		},
		ConfigSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"search_depth": map[string]any{
					"type":        "string",
					"title":       "Search Depth",
					"description": "basic or advanced",
					"enum":        []any{"basic", "advanced"},
					"default":     "basic",
				},
				"page_size": map[string]any{
					"type":    "integer",
					"title":   "Page Size",
					"default": 5,
				},
				"safe_mode": map[string]any{
					"type":  "boolean",
					"title": "Safe Mode",
				},
				// reserved keys must not surface as custom fields even when
				// declared inside the schema
				"api_key": map[string]any{"type": "string"},
			},
			"required": []any{"search_depth"},
		},
	}
	info := webSearchProviderTypeInfo(manifest, "test.schema")
	require.True(t, info.RequiresAPIKey) // capability flag still honored from config list

	fields := info.ConfigFields
	require.Len(t, fields, 3) // sorted by key: page_size, safe_mode, search_depth

	require.Equal(t, "page_size", fields[0].Key)
	require.Equal(t, "number", fields[0].Type)
	require.Equal(t, "Page Size", fields[0].Label)
	require.Equal(t, "5", fields[0].Default)

	require.Equal(t, "safe_mode", fields[1].Key)
	require.Equal(t, "boolean", fields[1].Type)

	require.Equal(t, "search_depth", fields[2].Key)
	require.Equal(t, "select", fields[2].Type)
	require.Equal(t, "Search Depth", fields[2].Label)
	require.Equal(t, "basic or advanced", fields[2].Description)
	require.True(t, fields[2].Required)
	require.Len(t, fields[2].Options, 2)
	require.Equal(t, "basic", fields[2].Options[0].Value)
	require.Equal(t, "advanced", fields[2].Options[1].Value)
	require.Equal(t, "basic", fields[2].Default)
}

func TestWebSearchConfigFieldTypeNormalization(t *testing.T) {
	require.Equal(t, "select", webSearchConfigFieldType("string", false, true))
	require.Equal(t, "boolean", webSearchConfigFieldType("boolean", false, false))
	require.Equal(t, "number", webSearchConfigFieldType("integer", false, false))
	require.Equal(t, "number", webSearchConfigFieldType("number", false, false))
	require.Equal(t, "array", webSearchConfigFieldType("string[]", false, false))
	require.Equal(t, "secret", webSearchConfigFieldType("string", true, false))
	require.Equal(t, "string", webSearchConfigFieldType("string", false, false))
}
