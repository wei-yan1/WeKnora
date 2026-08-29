package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/datasource"
	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	pluginPkg "github.com/Tencent/WeKnora/internal/plugin"
)

// PluginHandler exposes plugin control-plane management endpoints.
type PluginHandler struct {
	manager           *pluginPkg.Manager
	connectorRegistry *datasource.ConnectorRegistry
	searchRegistry    *infraWebSearch.Registry
	retrieverRegistry *pluginPkg.RetrieverProviderRegistry
}

// NewPluginHandler creates a PluginHandler. The manager and registries are the
// same singletons used at startup so a rescan operates on the live control
// plane rather than a detached copy.
func NewPluginHandler(
	manager *pluginPkg.Manager,
	connectorRegistry *datasource.ConnectorRegistry,
	searchRegistry *infraWebSearch.Registry,
	retrieverRegistry *pluginPkg.RetrieverProviderRegistry,
) *PluginHandler {
	return &PluginHandler{
		manager:           manager,
		connectorRegistry: connectorRegistry,
		searchRegistry:    searchRegistry,
		retrieverRegistry: retrieverRegistry,
	}
}

// Rescan re-runs plugin discovery over the configured directories and
// incrementally loads any newly appeared plugins. It never touches already
// loaded plugins, so it is safe to call repeatedly.
func (h *PluginHandler) Rescan(c *gin.Context) {
	report := pluginPkg.RescanExternalFromEnvWithRegistries(
		c.Request.Context(),
		h.manager,
		h.connectorRegistry,
		h.searchRegistry,
		h.retrieverRegistry,
	)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": report})
}
