package plugin

import "testing"

func TestValidateConfigSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"settings": map[string]any{
				"type":     "object",
				"required": []any{"root"},
				"properties": map[string]any{
					"root": map[string]any{"type": "string"},
				},
			},
		},
	}
	if err := ValidateConfigSchema(schema, map[string]any{"settings": map[string]any{"root": "C:/docs"}}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := ValidateConfigSchema(schema, map[string]any{"settings": map[string]any{}}); err == nil {
		t.Fatal("missing required setting was accepted")
	}
	if err := ValidateConfigSchema(schema, map[string]any{"settings": map[string]any{"root": 42.0}}); err == nil {
		t.Fatal("wrong setting type was accepted")
	}
}

func TestEffectiveConfigSchemaWrapsLegacyFieldsUnderSettings(t *testing.T) {
	schema := EffectiveConfigSchema(Manifest{Config: []ConfigField{{Key: "root", Type: "string", Required: true}}})
	if err := ValidateConfigSchema(schema, map[string]any{"settings": map[string]any{"root": "C:/docs"}}); err != nil {
		t.Fatalf("derived schema rejected valid config: %v", err)
	}
}
