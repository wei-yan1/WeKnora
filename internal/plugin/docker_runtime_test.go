package plugin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerRuntimeSecurityArgs(t *testing.T) {
	manifest := validManifest()
	manifest.Permissions.AllowedDestinations = []string{"api.example.com:443"}
	runtime := NewDockerRuntime(manifest, "example/plugin:dev")
	args := runtime.dockerArgs("C:\\plugin-runtime", "/run/weknora/plugin.sock", "weknora-plugin-test")
	joined := strings.Join(args, " ")
	require.Contains(t, joined, "--network none")
	require.Contains(t, joined, "--read-only")
	require.Contains(t, joined, "--cap-drop ALL")
	require.Contains(t, joined, "no-new-privileges")
	require.Contains(t, joined, "--pids-limit 128")
	require.Contains(t, joined, "--memory 512m")
	require.Contains(t, joined, "WEKNORA_PLUGIN_NETWORK_ALLOWLIST=api.example.com:443")
}
