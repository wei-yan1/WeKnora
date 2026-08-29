package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	modelprovider "github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
	pluginproto "github.com/Tencent/WeKnora/pkg/pluginapi/proto"
)

// modelReservedKeys are the config keys with dedicated host-side model form
// sections (api_key / base_url boxes in ModelEditorDialog). They never surface
// as custom ConfigFields, so the frontend never renders them twice.
var modelReservedKeys = map[string]struct{}{
	"api_key":  {},
	"base_url": {},
}

// RegisterExternalModel wires an external model plugin into the host model
// provider registry. Model capability factories (chat.NewChat, ...) are
// synchronous — unlike the datasource connector factory, there is no lazy-start
// hook to defer the connection. The runtime is therefore started eagerly here
// so a live gRPC client can be registered before any model is resolved.
func RegisterExternalModel(manager *Manager, manifest Manifest, runtime Runtime, lazyStart bool) (string, error) {
	if manifest.ExtensionType != ExtensionModel {
		return "", fmt.Errorf("plugin %q is not a model", manifest.ID)
	}
	provider, ok := runtime.(connProvider)
	if !ok {
		return "", fmt.Errorf("runtime for model %q does not expose a gRPC connection", manifest.ID)
	}
	if err := manager.Register(manifest, runtime); err != nil {
		return "", err
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), defaultStartGrace)
	err := manager.Start(startCtx, manifest.ID)
	cancelStart()
	if err != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", err
	}
	if provider.Conn() == nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", fmt.Errorf("model plugin %q is not running", manifest.ID)
	}

	// Cross-check the runtime ModelInfo against the manifest capabilities so the
	// host never dispatches to a capability the plugin does not actually
	// implement (declaring "chat" while runtime reports only "embedding").
	modelClient := pluginapi.NewModelPluginClient(provider.Conn())
	infoCtx, cancelInfo := context.WithTimeout(context.Background(), 10*time.Second)
	infoResp, infoErr := modelClient.ModelInfo(infoCtx, &pluginproto.ModelInfoRequest{})
	cancelInfo()
	if infoErr != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", fmt.Errorf("query model info for %q: %w", manifest.ID, infoErr)
	}
	if err := verifyModelCapabilities(manifest.Capabilities, infoResp.GetCapabilities()); err != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", err
	}

	modelProvider := manifest.ID
	if manifest.Metadata != nil {
		if v, ok := manifest.Metadata["provider"].(string); ok && strings.TrimSpace(v) != "" {
			modelProvider = strings.TrimSpace(v)
		}
	}
	modelprovider.RegisterExternalModelResolver(modelProvider, func(ctx context.Context, _ string) (pluginapi.ModelPluginClient, context.Context, func(), error) {
		lease, err := manager.AcquireInvocation(ctx, manifest.ID)
		if err != nil {
			return nil, ctx, func() {}, err
		}
		conn := provider.Conn()
		if conn == nil {
			lease.Close()
			return nil, ctx, func() {}, fmt.Errorf("model plugin %q is not running", manifest.ID)
		}
		callCtx := pluginapi.WithInvocationContext(lease.Context, invocationFromContext(ctx, ""))
		return pluginapi.NewModelPluginClient(conn), callCtx, lease.Close, nil
	})

	// Record static metadata so the /models/providers endpoint can merge this
	// external plugin into the provider list for dynamic frontend rendering.
	description := ""
	if manifest.Metadata != nil {
		if v, ok := manifest.Metadata["description"].(string); ok {
			description = strings.TrimSpace(v)
		}
	}
	var configFields []types.WebSearchProviderConfigField
	if len(manifest.ConfigSchema) > 0 {
		configFields = configFieldsFromSchema(manifest.ConfigSchema, modelReservedKeys)
	} else {
		configFields = configFieldsFromLegacy(manifest.Config, modelReservedKeys)
	}
	modelprovider.RegisterExternalModelInfo(modelprovider.ExternalModelInfo{
		Provider:     modelProvider,
		Name:         manifest.Name,
		Description:  description,
		Capabilities: manifest.Capabilities,
		ConfigFields: configFields,
	})
	return modelProvider, nil
}

// UnregisterExternalModel removes an external model plugin from the provider
// registry. The runtime lifecycle is owned by Manager; this only removes the
// client so future model resolution stops finding the plugin.
func UnregisterExternalModel(providerName string) {
	modelprovider.UnregisterExternalModelResolver(providerName)
	modelprovider.UnregisterExternalModelInfo(providerName)
}

// verifyModelCapabilities ensures every capability declared in the manifest is
// also reported by the plugin's runtime ModelInfo, so a mismatched declaration
// is rejected at load time rather than surfacing as a confusing runtime error.
func verifyModelCapabilities(declared, reported []string) error {
	if len(declared) == 0 {
		return nil
	}
	reportedSet := make(map[string]struct{}, len(reported))
	for _, c := range reported {
		reportedSet[c] = struct{}{}
	}
	for _, c := range declared {
		if _, ok := reportedSet[c]; !ok {
			return fmt.Errorf("model plugin declares capability %q but runtime ModelInfo does not report it", c)
		}
	}
	return nil
}
