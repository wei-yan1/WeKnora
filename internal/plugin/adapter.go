package plugin

import (
	"github.com/Tencent/WeKnora/internal/datasource"
	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
)

// ExtensionAdapter wires a plugin of a specific extension type into the host's
// business registries. Adding a new extension type requires only a new
// ExtensionAdapter implementation and its registration; PluginManager, Runtime,
// health supervision, admission control and lifecycle management stay untouched.
type ExtensionAdapter interface {
	ExtensionType() string
	Register(manager *Manager, manifest Manifest, runtime Runtime) (adapterHandle, error)
	Unregister(handle adapterHandle)
}

// adapterHandle records the unregister information for one business
// registration. Each adapter uses only the fields it cares about; the rest stay
// empty.
type adapterHandle struct {
	connectorType string
	parserName    string
	searchType    string
}

// ExtensionAdapterRegistry maps an extension type to its business adapter. The
// loader looks up adapters by manifest.ExtensionType instead of growing a switch
// as new extension types are introduced.
type ExtensionAdapterRegistry struct {
	adapters map[string]ExtensionAdapter
}

func NewExtensionAdapterRegistry(connectorRegistry *datasource.ConnectorRegistry, searchRegistry *infraWebSearch.Registry) *ExtensionAdapterRegistry {
	r := &ExtensionAdapterRegistry{adapters: make(map[string]ExtensionAdapter)}
	r.Register(datasourceAdapter{registry: connectorRegistry})
	r.Register(parserAdapter{})
	if searchRegistry != nil {
		r.Register(searchAdapter{registry: searchRegistry})
	}
	return r
}

func (r *ExtensionAdapterRegistry) Register(adapter ExtensionAdapter) {
	r.adapters[adapter.ExtensionType()] = adapter
}

func (r *ExtensionAdapterRegistry) Get(extensionType string) (ExtensionAdapter, bool) {
	adapter, ok := r.adapters[extensionType]
	return adapter, ok
}

// datasourceAdapter wires a data-source plugin into the ConnectorRegistry.
type datasourceAdapter struct {
	registry *datasource.ConnectorRegistry
}

func (datasourceAdapter) ExtensionType() string { return ExtensionDataSource }

func (a datasourceAdapter) Register(manager *Manager, manifest Manifest, runtime Runtime) (adapterHandle, error) {
	connectorType, err := RegisterExternalDataSource(manager, a.registry, manifest, runtime, false)
	return adapterHandle{connectorType: connectorType}, err
}

func (a datasourceAdapter) Unregister(h adapterHandle) {
	a.registry.UnregisterFactory(h.connectorType)
	datasource.UnregisterExternalConnectorMetadata(h.connectorType)
}

// parserAdapter wires a parser plugin into the docparser engine registry.
type parserAdapter struct{}

func (parserAdapter) ExtensionType() string { return ExtensionParser }

func (parserAdapter) Register(manager *Manager, manifest Manifest, runtime Runtime) (adapterHandle, error) {
	descriptor, err := ParserDescriptorFromManifest(manifest)
	if err != nil {
		return adapterHandle{}, err
	}
	if err := RegisterExternalParser(manager, manifest, runtime, descriptor, false); err != nil {
		return adapterHandle{}, err
	}
	return adapterHandle{parserName: descriptor.EngineName}, nil
}

func (parserAdapter) Unregister(h adapterHandle) {
	UnregisterExternalParser(ParserDescriptor{EngineName: h.parserName})
}

// searchAdapter wires a web-search plugin into the tenant-scoped search registry.
type searchAdapter struct {
	registry *infraWebSearch.Registry
}

func (searchAdapter) ExtensionType() string { return ExtensionSearch }

func (a searchAdapter) Register(manager *Manager, manifest Manifest, runtime Runtime) (adapterHandle, error) {
	searchType, err := RegisterExternalWebSearch(manager, a.registry, manifest, runtime, false)
	return adapterHandle{searchType: searchType}, err
}

func (a searchAdapter) Unregister(h adapterHandle) {
	if a.registry != nil {
		a.registry.Unregister(h.searchType)
	}
}
