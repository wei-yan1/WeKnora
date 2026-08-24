package plugin

import (
	"context"
	"fmt"
	"strings"

	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

// RegisterExternalWebSearch connects a lifecycle runtime to the existing
// tenant-scoped web search registry. Provider parameters are kept at the
// factory boundary, so one long-lived plugin process can serve many tenants
// without sharing credentials between provider instances.
func RegisterExternalWebSearch(
	manager *Manager,
	registry *infraWebSearch.Registry,
	manifest Manifest,
	runtime Runtime,
	lazyStart bool,
) (string, error) {
	if manifest.ExtensionType != ExtensionSearch {
		return "", fmt.Errorf("plugin %q is not a web search provider", manifest.ID)
	}
	if registry == nil {
		return "", fmt.Errorf("web search registry is nil")
	}
	provider, ok := runtime.(connProvider)
	if !ok {
		return "", fmt.Errorf("runtime for web search plugin %q does not expose a gRPC connection", manifest.ID)
	}

	providerType := manifest.ID
	if manifest.Metadata != nil {
		if value, ok := manifest.Metadata["provider_type"].(string); ok && strings.TrimSpace(value) != "" {
			providerType = strings.TrimSpace(value)
		}
	}
	if err := manager.Register(manifest, runtime); err != nil {
		return "", err
	}
	if err := registry.RegisterWithInfo(providerType, func(params types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) {
		if lazyStart {
			if err := manager.Start(context.Background(), manifest.ID); err != nil {
				return nil, err
			}
		}
		conn := provider.Conn()
		if conn == nil {
			return nil, fmt.Errorf("web search plugin %q is not running", manifest.ID)
		}
		client := pluginapi.NewWebSearchPluginClient(conn)
		return &GRPCWebSearchProxy{Client: client, Params: params, NameValue: providerType, Manager: manager, PluginID: manifest.ID}, nil
	}, webSearchProviderTypeInfo(manifest, providerType)); err != nil {
		_ = manager.Unregister(context.Background(), manifest.ID)
		return "", err
	}
	return providerType, nil
}

func webSearchProviderTypeInfo(manifest Manifest, providerType string) types.WebSearchProviderTypeInfo {
	info := types.WebSearchProviderTypeInfo{ID: providerType, Name: manifest.Name, Description: manifest.Name}
	if manifest.Metadata != nil {
		if value, ok := manifest.Metadata["description"].(string); ok && strings.TrimSpace(value) != "" {
			info.Description = strings.TrimSpace(value)
		}
		if value, ok := manifest.Metadata["docs_url"].(string); ok {
			info.DocsURL = strings.TrimSpace(value)
		}
	}
	for _, field := range manifest.Config {
		key := strings.TrimSpace(field.Key)
		switch key {
		case "api_key":
			info.RequiresAPIKey = field.Required
		case "engine_id":
			info.RequiresEngineID = field.Required
		case "base_url":
			info.RequiresBaseURL = field.Required
		case "proxy_url":
			info.SupportsProxy = true
		}
		info.ConfigFields = append(info.ConfigFields, types.WebSearchProviderConfigField{
			Key:         key,
			Label:       field.Description,
			Type:        field.Type,
			Required:    field.Required,
			Description: field.Description,
		})
	}
	return info
}
