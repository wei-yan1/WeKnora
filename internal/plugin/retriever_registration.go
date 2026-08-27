package plugin

import (
	"context"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
	pluginproto "github.com/Tencent/WeKnora/pkg/pluginapi/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// RetrieverProviderInfo records how to reach one external retriever plugin for
// a given engine type. The host's remote engine factory consumes this to build
// a GRPCRetrieverRepository when a VectorStore of that engine type is created.
type RetrieverProviderInfo struct {
	PluginID     string
	EngineType   types.RetrieverEngineType
	Capabilities []string
	// OpenSession opens a store session: it acquires the current runtime
	// generation, builds a client from the raw connection, and calls OpenStore.
	// It returns the client, the opaque store_handle, and the runtime generation
	// the handle is bound to.
	OpenSession func(ctx context.Context, config pluginapi.RetrieverStoreConfig) (pluginapi.RetrieverPluginClient, string, uint64, error)
	// GenerationValid reports whether a runtime generation is still current.
	// Used to lazily re-open a store session after the plugin runtime restarts.
	GenerationValid func(generation uint64) bool
}

// RetrieverProviderRegistry maps an engine type to its external plugin. It is
// the retriever counterpart of datasource.ConnectorRegistry: adapters write
// into it at load time, and the engine factory reads from it at store build
// time.
type RetrieverProviderRegistry struct {
	mu        sync.RWMutex
	providers map[types.RetrieverEngineType]RetrieverProviderInfo
}

func NewRetrieverProviderRegistry() *RetrieverProviderRegistry {
	return &RetrieverProviderRegistry{providers: make(map[types.RetrieverEngineType]RetrieverProviderInfo)}
}

// Get returns the provider registered for an engine type.
func (r *RetrieverProviderRegistry) Get(engineType types.RetrieverEngineType) (RetrieverProviderInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.providers[engineType]
	return info, ok
}

func (r *RetrieverProviderRegistry) put(info RetrieverProviderInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[info.EngineType] = info
}

func (r *RetrieverProviderRegistry) remove(engineType types.RetrieverEngineType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, engineType)
}

// RegisterExternalRetriever wires an external retriever plugin into the
// RetrieverProviderRegistry. No backend connection is made here: that happens
// lazily when a VectorStore of this engine type is opened. The plugin runtime
// is started by the loader (manager.Start), not here.
func RegisterExternalRetriever(
	registry *RetrieverProviderRegistry,
	manager *Manager,
	manifest Manifest,
	runtime Runtime,
	_ bool, // lazyStart: kept for signature parity with other adapters; loader owns Start
) (types.RetrieverEngineType, error) {
	if registry == nil {
		return "", fmt.Errorf("retriever provider registry is nil")
	}
	provider, ok := runtime.(connProvider)
	if !ok {
		return "", fmt.Errorf("runtime for retriever plugin %q does not expose a gRPC connection", manifest.ID)
	}
	engineTypeStr, _ := manifest.Metadata["engine_type"].(string)
	if engineTypeStr == "" {
		return "", fmt.Errorf("retriever plugin %q must declare metadata.engine_type", manifest.ID)
	}
	engineType := types.RetrieverEngineType(engineTypeStr)
	pluginID := manifest.ID

	// Reject an external engine type that collides with a built-in engine or an
	// already-registered external engine. Relying on developer discipline would
	// silently shadow an earlier registration (load order decides the winner).
	if types.IsValidEngineType(engineType) {
		return "", fmt.Errorf("retriever engine type %q conflicts with a built-in or already-registered engine type", engineType)
	}

	// Expose the external engine type to VectorStore validation and the
	// /vector-stores/types listing, mapping the plugin's config fields onto the
	// registration UI schema so users can create a store without host changes.
	types.RegisterExternalVectorStoreType(types.VectorStoreTypeInfo{
		Type:             engineTypeStr,
		DisplayName:      manifest.Name,
		ConnectionFields: configFieldsToVectorStoreFields(manifest.Config),
	})

	registry.put(RetrieverProviderInfo{
		PluginID:     pluginID,
		EngineType:   engineType,
		Capabilities: manifest.Capabilities,
		OpenSession: func(ctx context.Context, config pluginapi.RetrieverStoreConfig) (pluginapi.RetrieverPluginClient, string, uint64, error) {
			lease, err := manager.AcquireInvocation(ctx, pluginID)
			if err != nil {
				return nil, "", 0, fmt.Errorf("acquire retriever invocation: %w", err)
			}
			defer lease.Close()
			client := pluginapi.NewRetrieverPluginClient(provider.Conn())
			resp, err := client.OpenStore(lease.Context, &pluginproto.RetrieverOpenStoreRequest{
				Config: retrieverConfigToStruct(config),
			})
			if err != nil {
				return nil, "", 0, fmt.Errorf("open retriever store: %w", err)
			}
			if resp.GetError() != "" {
				return nil, "", 0, fmt.Errorf("open retriever store: %s", resp.GetError())
			}
			return client, resp.GetStoreHandle(), lease.Generation, nil
		},
		GenerationValid: func(generation uint64) bool {
			return manager.GenerationValid(pluginID, generation)
		},
	})
	return engineType, nil
}

// UnregisterExternalRetriever removes an external retriever provider and its
// engine type registration.
func UnregisterExternalRetriever(registry *RetrieverProviderRegistry, engineType types.RetrieverEngineType) {
	if registry != nil {
		registry.remove(engineType)
	}
	types.UnregisterExternalVectorStoreType(engineType)
}

// configFieldsToVectorStoreFields maps a plugin manifest's config fields onto
// the VectorStore registration schema, so external engine types get dynamic
// connection fields without hard-coding engine-specific branches in the host.
func configFieldsToVectorStoreFields(fields []ConfigField) []types.VectorStoreFieldInfo {
	if len(fields) == 0 {
		return nil
	}
	out := make([]types.VectorStoreFieldInfo, 0, len(fields))
	for _, f := range fields {
		out = append(out, types.VectorStoreFieldInfo{
			Name:        f.Key,
			Type:        f.Type,
			Required:    f.Required,
			Sensitive:   f.Secret,
			Default:     f.Default,
			Description: f.Description,
			Enum:        f.Enum,
		})
	}
	return out
}

func retrieverConfigToStruct(config pluginapi.RetrieverStoreConfig) *structpb.Struct {
	m := map[string]any{
		"settings":     config.Settings,
		"credentials":  config.Credentials,
		"index_config": config.IndexConfig,
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

// retrieverAdapter wires a retriever plugin into the RetrieverProviderRegistry.
type retrieverAdapter struct {
	registry *RetrieverProviderRegistry
}

func (retrieverAdapter) ExtensionType() string { return ExtensionRetriever }

func (a retrieverAdapter) Register(manager *Manager, manifest Manifest, runtime Runtime) (adapterHandle, error) {
	engineType, err := RegisterExternalRetriever(a.registry, manager, manifest, runtime, false)
	return adapterHandle{retrieverEngine: string(engineType)}, err
}

func (a retrieverAdapter) Unregister(h adapterHandle) {
	UnregisterExternalRetriever(a.registry, types.RetrieverEngineType(h.retrieverEngine))
}
