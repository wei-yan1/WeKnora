package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestExternalWebSearchProcessRuntime(t *testing.T) {
	binary := os.Getenv("WEKNORA_TEMPLATE_WEB_SEARCH_PLUGIN_BIN")
	if binary == "" {
		t.Skip("set WEKNORA_TEMPLATE_WEB_SEARCH_PLUGIN_BIN to run the external web search acceptance test")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("web search plugin binary: %v", err)
	}

	pluginRoot := t.TempDir()
	manifest := Manifest{
		APIVersion:      APIVersionV1,
		ID:              "example.template-search",
		Name:            "Template Web Search Provider",
		Version:         "0.1.0",
		ExtensionType:   ExtensionSearch,
		ProtocolVersion: ProtocolVersionV1,
		Entrypoint:      binary,
		Permissions:     Permissions{Network: NetworkNone},
		Capabilities:    []string{"search"},
		Metadata:        map[string]any{"provider_type": "template_search"},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), manifestBytes, 0o644))

	manager := NewManager("")
	searchRegistry := infraWebSearch.NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, LoadExternalWithRegistries(ctx, []string{pluginRoot}, manager, datasource.NewConnectorRegistry(), searchRegistry, nil))
	t.Cleanup(func() {
		searchRegistry.Unregister("template_search")
		_ = manager.Unregister(context.Background(), manifest.ID)
	})

	health, err := manager.Health(ctx, manifest.ID)
	require.NoError(t, err)
	require.Equal(t, StateRunning, health.State)

	provider, err := searchRegistry.CreateProvider("template_search", types.WebSearchProviderParameters{APIKey: "tenant-secret"})
	require.NoError(t, err)
	results, err := provider.Search(ctx, "hello world", 5, false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "Template result for hello world", results[0].Title)
	require.Equal(t, "template_search", results[0].Source)
}
