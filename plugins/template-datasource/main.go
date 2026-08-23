package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

func main() {
	address := flag.String("address", os.Getenv("WEKNORA_PLUGIN_ADDR"), "gRPC listen address")
	flag.Parse()
	if *address == "" {
		*address = "127.0.0.1:9778"
	}
	handler := templateHandler{}
	if err := pluginapi.Serve(context.Background(), *address, pluginapi.DataSourceHandler{
		PluginID: "your-org.example-datasource", Capabilities: []string{"incremental"},
		OnValidate:         handler.Validate,
		OnListResources:    handler.ListResources,
		OnFetchAll:         handler.FetchAll,
		OnFetchIncremental: handler.FetchIncremental,
	}); err != nil {
		panic(err)
	}
}

// templateHandler is intentionally small but runnable. It represents a
// source whose current snapshot is one deterministic document. Replace these
// methods with the real API/filesystem implementation when copying the
// template into an independent plugin repository.
type templateHandler struct{}

func (templateHandler) Validate(_ context.Context, request pluginapi.Request) error {
	endpoint, err := endpointFromRequest(request)
	if err != nil {
		return err
	}
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("settings.endpoint is required")
	}
	return nil
}

func (h templateHandler) ListResources(ctx context.Context, request pluginapi.Request) ([]pluginapi.Resource, error) {
	if err := h.Validate(ctx, request); err != nil {
		return nil, err
	}
	return []pluginapi.Resource{{ExternalID: "template:root", Name: "Template source", Type: "document"}}, nil
}

func (h templateHandler) FetchAll(ctx context.Context, request pluginapi.Request) ([]pluginapi.FetchedItem, error) {
	if err := h.Validate(ctx, request); err != nil {
		return nil, err
	}
	endpoint, _ := endpointFromRequest(request)
	return []pluginapi.FetchedItem{h.item(endpoint)}, nil
}

func (h templateHandler) FetchIncremental(ctx context.Context, request pluginapi.Request) ([]pluginapi.FetchedItem, map[string]any, error) {
	if err := h.Validate(ctx, request); err != nil {
		return nil, nil, err
	}
	endpoint, _ := endpointFromRequest(request)
	if request.Cursor != nil {
		if version, ok := request.Cursor["version"].(float64); ok && version >= 1 {
			return nil, map[string]any{"version": 1}, nil
		}
		if version, ok := request.Cursor["version"].(int); ok && version >= 1 {
			return nil, map[string]any{"version": 1}, nil
		}
	}
	return []pluginapi.FetchedItem{h.item(endpoint)}, map[string]any{"version": 1}, nil
}

func (templateHandler) item(endpoint string) pluginapi.FetchedItem {
	return pluginapi.FetchedItem{
		ExternalID: "template:document",
		Title:      "template.md",
		Content:    []byte("# WeKnora plugin template\n\nsource: " + endpoint + "\n"),
		MIMEType:   "text/markdown",
	}
}

func endpointFromRequest(request pluginapi.Request) (string, error) {
	settings, _ := request.Config["settings"].(map[string]any)
	endpoint, _ := settings["endpoint"].(string)
	if strings.TrimSpace(endpoint) == "" {
		return "", fmt.Errorf("settings.endpoint is required")
	}
	return endpoint, nil
}
