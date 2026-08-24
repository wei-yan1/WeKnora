package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/pkg/pluginapi"
)

func main() {
	address := flag.String("address", os.Getenv("WEKNORA_PLUGIN_ADDR"), "gRPC listen address")
	flag.Parse()
	if *address == "" {
		*address = "127.0.0.1:9776"
	}

	if err := pluginapi.ServeParser(context.Background(), *address, pluginapi.ParserHandler{
		PluginID:     "example.template-markdown-parser",
		Capabilities: []string{"parse"},
		OnHealth: func(context.Context) pluginapi.HealthResponse {
			return pluginapi.HealthResponse{State: "running", Message: "template parser ready"}
		},
		OnParse: func(_ context.Context, request pluginapi.ParserRequest) (pluginapi.ParserResponse, error) {
			content := strings.TrimSpace(string(request.FileContent))
			if content == "" {
				return pluginapi.ParserResponse{}, errors.New("file_content is required")
			}
			return pluginapi.ParserResponse{
				MarkdownContent: content,
				Metadata: map[string]string{
					"engine":    "template_markdown",
					"file_name": request.FileName,
				},
			}, nil
		},
	}); err != nil {
		panic(err)
	}
}
