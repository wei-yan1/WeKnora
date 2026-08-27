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
	return LoadExternalWithRegistries(ctx, roots, manager, registry, nil, nil)
}

// LoadExternalWithRegistries is the full v1 loader. Datasource and parser
// packages can be loaded with the legacy LoadExternal helper; a host that also
// exposes external web-search providers supplies the web-search registry.
//
// Extension-specific registration is delegated to an ExtensionAdapterRegistry,
// so adding a new extension type requires registering a new adapter rather than
// adding a switch case here.
func LoadExternalWithRegistries(ctx context.Context, roots []string, manager *Manager, registry *datasource.ConnectorRegistry, searchRegistry *infraWebSearch.Registry, retrieverRegistry *RetrieverProviderRegistry) error {
	packages, err := DiscoverPackages(roots)
	if err != nil {
		return err
	}
	adapters := NewExtensionAdapterRegistry(registry, searchRegistry, retrieverRegistry)
	loaded := make([]loadedAdapter, 0, len(packages))
	cleanup := func() {
		for i := len(loaded) - 1; i >= 0; i-- {
			loaded[i].adapter.Unregister(loaded[i].handle)
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
		adapter, ok := adapters.Get(manifest.ExtensionType)
		if !ok {
			cleanup()
			return fmt.Errorf("plugin %q uses extension_type %q, but no adapter is registered for it", manifest.ID, manifest.ExtensionType)
		}
		handle, err := adapter.Register(manager, manifest, runtime)
		if err != nil {
			cleanup()
			return err
		}
		loaded = append(loaded, loadedAdapter{id: manifest.ID, adapter: adapter, handle: handle})
		if err := manager.Start(ctx, manifest.ID); err != nil {
			cleanup()
			return err
		}
	}
	return nil
}

// loadedAdapter records a completed load for rollback on partial failure.
type loadedAdapter struct {
	id      string
	adapter ExtensionAdapter
	handle  adapterHandle
}

func LoadExternalFromEnv(ctx context.Context, manager *Manager, registry *datasource.ConnectorRegistry) error {
	return LoadExternalFromEnvWithRegistries(ctx, manager, registry, nil, nil)
}

// pluginDirEnvVars maps each extension type to the environment variable that
// names the directory holding plugins of that type. Directories are organized
// by extension type so a host can point each entry point at its own plugin
// directory; every directory is still scanned and loaded at startup.
var pluginDirEnvVars = []struct {
	extensionType string
	envVar        string
}{
	{ExtensionDataSource, "WEKNORA_PLUGIN_DIR_DATASOURCE"},
	{ExtensionParser, "WEKNORA_PLUGIN_DIR_PARSER"},
	{ExtensionSearch, "WEKNORA_PLUGIN_DIR_SEARCH"},
	{ExtensionModel, "WEKNORA_PLUGIN_DIR_MODEL"},
	{ExtensionRetriever, "WEKNORA_PLUGIN_DIR_RETRIEVER"},
}

func LoadExternalFromEnvWithRegistries(ctx context.Context, manager *Manager, registry *datasource.ConnectorRegistry, searchRegistry *infraWebSearch.Registry, retrieverRegistry *RetrieverProviderRegistry) error {
	var roots []string
	for _, entry := range pluginDirEnvVars {
		value := strings.TrimSpace(os.Getenv(entry.envVar))
		if value == "" {
			continue
		}
		for _, dir := range strings.FieldsFunc(value, func(r rune) bool {
			return r == os.PathListSeparator || r == ','
		}) {
			if dir = strings.TrimSpace(dir); dir != "" {
				roots = append(roots, dir)
			}
		}
	}
	if len(roots) == 0 {
		return nil
	}
	return LoadExternalWithRegistries(ctx, roots, manager, registry, searchRegistry, retrieverRegistry)
}
