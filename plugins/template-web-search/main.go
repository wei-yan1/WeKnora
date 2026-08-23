package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

func main() {
	address := flag.String("address", os.Getenv("WEKNORA_PLUGIN_ADDR"), "gRPC listen address")
	flag.Parse()
	if *address == "" {
		*address = "127.0.0.1:9775"
	}

	if err := pluginapi.ServeWebSearch(context.Background(), *address, pluginapi.WebSearchHandler{
		PluginID:     "example.template-search",
		Capabilities: []string{"search"},
		OnSearch: func(_ context.Context, request pluginapi.WebSearchRequest) (pluginapi.WebSearchResponse, error) {
			if request.Query == "" {
				return pluginapi.WebSearchResponse{}, fmt.Errorf("query is required")
			}
			return pluginapi.WebSearchResponse{Results: []*pluginapi.WebSearchResult{{
				Title:   "Template result for " + request.Query,
				URL:     "https://example.com/search?q=" + url.QueryEscape(request.Query),
				Snippet: "Replace this deterministic result with the provider API implementation.",
				Source:  "template_search",
			}}}, nil
		},
	}); err != nil {
		panic(err)
	}
}
