package plugin

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

// RegisterExternalDataSource connects a lifecycle runtime to the existing
// datasource registry without adding a connector-specific branch to the
// application. The manifest ID is the default connector type; metadata can
// override it for packages that expose a human-readable plugin ID.
func RegisterExternalDataSource(manager *Manager, registry *datasource.ConnectorRegistry, manifest Manifest, runtime Runtime, lazyStart bool) (string, error) {
	return registerExternalDataSource(manager, registry, manifest, runtime, lazyStart, "")
}

func registerExternalDataSource(manager *Manager, registry *datasource.ConnectorRegistry, manifest Manifest, runtime Runtime, lazyStart bool, sourceDir string) (string, error) {
	if manifest.ExtensionType != ExtensionDataSource {
		return "", fmt.Errorf("plugin %q is not a datasource", manifest.ID)
	}
	provider, ok := runtime.(connProvider)
	if !ok {
		return "", fmt.Errorf("runtime for plugin %q does not expose a gRPC connection", manifest.ID)
	}
	if err := manager.Register(manifest, runtime); err != nil {
		return "", err
	}
	connectorType := manifest.ID
	if manifest.Metadata != nil {
		if value, ok := manifest.Metadata["connector_type"].(string); ok && value != "" {
			connectorType = value
		}
	}
	if err := registry.RegisterFactory(connectorType, func(ctx context.Context, scope datasource.ConnectorScope, config *types.DataSourceConfig) (datasource.ConnectorLease, error) {
		if lazyStart {
			if err := manager.Start(ctx, manifest.ID); err != nil {
				return nil, err
			}
		}
		invocationLease, err := manager.AcquireInvocation(ctx, manifest.ID)
		if err != nil {
			return nil, err
		}
		generation, release := invocationLease.Generation, invocationLease.Close
		conn := provider.Conn()
		if conn == nil {
			release()
			return nil, fmt.Errorf("plugin %q is not running", manifest.ID)
		}
		client := pluginapi.NewDataSourcePluginClient(conn)
		var streaming pluginapi.DataSourceStreamingPluginClient
		if candidate, ok := client.(pluginapi.DataSourceStreamingPluginClient); ok {
			streaming = candidate
		}
		return processConnectorLease{release: release, generation: generation, connector: &GRPCConnectorProxy{
			ConnectorType:     connectorType,
			Client:            client,
			StreamingClient:   streaming,
			ConfigSchema:      EffectiveConfigSchema(manifest),
			Invocation:        invocationFromScope(scope),
			RuntimeGeneration: generation,
			RuntimeContext:    invocationLease.Context,
			GenerationValid:   func(value uint64) bool { return manager.GenerationValid(manifest.ID, value) },
		}}, nil
	}); err != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", err
	}
	// Derive connector metadata from the manifest so the external plugin
	// surfaces in GET /datasource/types without any hard-coded entry in the
	// main repository.
	meta := ConnectorMetadataFromManifest(manifest, connectorType)
	datasource.RegisterExternalConnectorMetadata(meta)
	// A local (relative) icon is served by the host from the plugin directory;
	// record its absolute path so the icon endpoint can stream it.
	if iconFile, ok := resolveLocalIconFile(sourceDir, manifestIconValue(manifest)); ok {
		datasource.RegisterExternalConnectorIconFile(connectorType, iconFile)
	}
	return connectorType, nil
}

// resolveLocalIconFile interprets the manifest metadata.icon value. A bare
// relative filename (no scheme) is resolved against the plugin directory and
// returned as an absolute path; an absolute http(s) URL or empty value yields
// ok=false (the frontend uses the URL directly, or falls back to a placeholder).
func resolveLocalIconFile(sourceDir, icon string) (string, bool) {
	icon = strings.TrimSpace(icon)
	if icon == "" {
		return "", false
	}
	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") || strings.HasPrefix(icon, "data:") {
		return "", false
	}
	if sourceDir == "" {
		return "", false
	}
	return filepath.Join(sourceDir, filepath.FromSlash(icon)), true
}

// connectorIconURL returns the host route that streams a plugin-bundled icon
// for the given connector type. Keep in sync with the route registered for
// DataSourceHandler.GetConnectorIcon.
func connectorIconURL(connectorType string) string {
	return "/api/v1/datasource/icon/" + url.PathEscape(connectorType)
}

// ConnectorMetadataFromManifest derives connector metadata from a plugin
// manifest. Built-in connectors live in datasource.ConnectorMetadataRegistry;
// external plugins flow through this helper so adding a plugin never requires
// editing the main repository's connector list.
func ConnectorMetadataFromManifest(manifest Manifest, connectorType string) datasource.ConnectorMetadata {
	meta := datasource.ConnectorMetadata{
		Type:         connectorType,
		Name:         manifest.Name,
		AuthType:     "none",
		Capabilities: manifest.Capabilities,
		ConfigSchema: EffectiveConfigSchema(manifest),
		External:     true,
	}
	if manifest.Metadata == nil {
		return meta
	}
	if v, ok := manifest.Metadata["description"].(string); ok {
		meta.Description = v
	}
	if v, ok := manifest.Metadata["icon"].(string); ok {
		meta.Icon = v
		if _, isLocal := resolveLocalIconFile(manifest.SourceDir, v); isLocal {
			// A plugin-bundled icon is served by the host at a stable URL so the
			// frontend can render it without knowing the plugin directory.
			meta.Icon = connectorIconURL(connectorType)
		}
	}
	if v, ok := manifest.Metadata["auth_type"].(string); ok {
		meta.AuthType = v
	}
	switch v := manifest.Metadata["priority"].(type) {
	case int:
		meta.Priority = v
	case int64:
		meta.Priority = int(v)
	case float64:
		meta.Priority = int(v)
	}
	return meta
}
