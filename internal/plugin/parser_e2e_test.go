package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// TestExternalParserProcessRuntime models a parser repository that is built
// independently from the host. It verifies manifest discovery, process
// startup, parser handshake/health, registration in the existing docparser
// registry, and one real Read call across gRPC.
func TestExternalParserProcessRuntime(t *testing.T) {
	binary := os.Getenv("WEKNORA_TEMPLATE_PARSER_PLUGIN_BIN")
	if binary == "" {
		t.Skip("set WEKNORA_TEMPLATE_PARSER_PLUGIN_BIN to run the external parser acceptance test")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("parser plugin binary: %v", err)
	}

	pluginRoot := t.TempDir()
	manifest := Manifest{
		APIVersion:      APIVersionV1,
		ID:              "example.template-markdown-parser",
		Name:            "Template Markdown Parser",
		Version:         "0.1.0",
		ExtensionType:   ExtensionParser,
		ProtocolVersion: ProtocolVersionV1,
		Entrypoint:      binary,
		Permissions:     Permissions{Network: NetworkNone},
		Capabilities:    []string{"parse"},
		Metadata: map[string]any{
			"engine_name": "template_markdown",
			"description": "Template parser",
			"file_types":  []any{"md", "markdown", "txt"},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), manifestBytes, 0o644))

	manager := NewManager("")
	registry := datasource.NewConnectorRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, LoadExternal(ctx, []string{pluginRoot}, manager, registry))
	t.Cleanup(func() {
		UnregisterExternalParser(ParserDescriptor{EngineName: "template_markdown"})
		_ = manager.Unregister(context.Background(), manifest.ID)
	})

	health, err := manager.Health(ctx, manifest.ID)
	require.NoError(t, err)
	require.Equal(t, StateRunning, health.State)

	var found bool
	for _, engine := range docparser.ListAllEngines(false, nil, nil) {
		if engine.Name != "template_markdown" {
			continue
		}
		found = true
		require.True(t, engine.Available)
		require.Equal(t, []string{"md", "markdown", "txt"}, engine.FileTypes)
	}
	require.True(t, found, "external parser engine was not discovered")

	reader, err := docparser.NewReader(ctx, "template_markdown", "md", false, docparser.ReaderDeps{})
	require.NoError(t, err)
	result, err := reader.Read(ctx, &types.ReadRequest{
		FileContent: []byte("# hello from an independent process"),
		FileName:    "README.md",
		FileType:    "md",
	})
	require.NoError(t, err)
	require.Equal(t, "# hello from an independent process", result.MarkdownContent)
	require.Equal(t, "template_markdown", result.Metadata["engine"])
}
