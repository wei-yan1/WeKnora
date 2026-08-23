package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
)

// LoadExternal discovers packages from independent plugin roots, creates a
// runtime from the manifest entrypoint, registers it in the lifecycle manager,
// and starts it. The datasource path also registers an instance-aware factory
// in the existing connector registry.
func LoadExternal(ctx context.Context, roots []string, manager *Manager, registry *datasource.ConnectorRegistry) error {
	return LoadExternalWithRegistries(ctx, roots, manager, registry, nil)
}

// LoadExternalWithRegistries is the full v1 loader. Datasource and parser
// packages can be loaded with the legacy LoadExternal helper; a host that also
// exposes external web-search providers supplies the web-search registry.
func LoadExternalWithRegistries(ctx context.Context, roots []string, manager *Manager, registry *datasource.ConnectorRegistry, searchRegistry *infraWebSearch.Registry) error {
	packages, err := DiscoverPackages(roots)
	if err != nil {
		return err
	}
	loaded := make([]struct {
		id            string
		connectorType string
		parserName    string
		searchType    string
	}, 0, len(packages))
	cleanup := func() {
		for i := len(loaded) - 1; i >= 0; i-- {
			if loaded[i].connectorType != "" {
				registry.UnregisterFactory(loaded[i].connectorType)
				datasource.UnregisterExternalConnectorMetadata(loaded[i].connectorType)
			}
			if loaded[i].parserName != "" {
				UnregisterExternalParser(ParserDescriptor{EngineName: loaded[i].parserName})
			}
			if loaded[i].searchType != "" && searchRegistry != nil {
				searchRegistry.Unregister(loaded[i].searchType)
			}
			_ = manager.Unregister(context.Background(), loaded[i].id)
		}
	}
	for _, pkg := range packages {
		manifest := pkg.Manifest
		entrypoint := manifest.Entrypoint
		if entrypoint == "" {
			cleanup()
			return fmt.Errorf("plugin %q has no entrypoint", manifest.ID)
		}
		var runtime Runtime
		if strings.HasPrefix(entrypoint, "docker://") {
			runtime = NewDockerRuntime(manifest, strings.TrimPrefix(entrypoint, "docker://"))
		} else {
			if !filepath.IsAbs(entrypoint) {
				entrypoint = filepath.Join(pkg.Root, entrypoint)
			}
			runtime = NewProcessRuntime(manifest, entrypoint)
		}
		var connectorType, parserName, searchType string
		switch manifest.ExtensionType {
		case ExtensionDataSource:
			connectorType, err = RegisterExternalDataSource(manager, registry, manifest, runtime, false)
		case ExtensionParser:
			descriptor, descriptorErr := ParserDescriptorFromManifest(manifest)
			if descriptorErr != nil {
				cleanup()
				return descriptorErr
			}
			err = RegisterExternalParser(manager, manifest, runtime, descriptor, false)
			parserName = descriptor.EngineName
		case ExtensionSearch:
			if searchRegistry == nil {
				cleanup()
				return fmt.Errorf("web search plugin %q requires a web search registry", manifest.ID)
			}
			searchType, err = RegisterExternalWebSearch(manager, searchRegistry, manifest, runtime, false)
		default:
			cleanup()
			return fmt.Errorf("plugin %q uses extension_type %q, but the v1 external protocol does not implement it yet", manifest.ID, manifest.ExtensionType)
		}
		if err != nil {
			cleanup()
			return err
		}
		loaded = append(loaded, struct {
			id            string
			connectorType string
			parserName    string
			searchType    string
		}{manifest.ID, connectorType, parserName, searchType})
		if err := manager.Start(ctx, manifest.ID); err != nil {
			cleanup()
			return err
		}
	}
	return nil
}

func LoadExternalFromEnv(ctx context.Context, manager *Manager, registry *datasource.ConnectorRegistry) error {
	return LoadExternalFromEnvWithRegistries(ctx, manager, registry, nil)
}

func LoadExternalFromEnvWithRegistries(ctx context.Context, manager *Manager, registry *datasource.ConnectorRegistry, searchRegistry *infraWebSearch.Registry) error {
	value := strings.TrimSpace(os.Getenv("WEKNORA_PLUGIN_DIRS"))
	if value == "" {
		return nil
	}
	roots := strings.FieldsFunc(value, func(r rune) bool {
		return r == os.PathListSeparator || r == ','
	})
	return LoadExternalWithRegistries(ctx, roots, manager, registry, searchRegistry)
}
