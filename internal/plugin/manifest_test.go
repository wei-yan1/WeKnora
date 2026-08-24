package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func validManifest() Manifest {
	return Manifest{APIVersion: APIVersionV1, ID: "example.localdir", Name: "Local Directory", Version: "1.0.0", ExtensionType: ExtensionDataSource, ProtocolVersion: ProtocolVersionV1, WeKnoraVersion: ">=1.0 <2.0", Permissions: Permissions{Network: NetworkNone}, Config: []ConfigField{{Key: "root", Type: "string", Required: true}}}
}

func TestManifestValidate(t *testing.T) {
	m := validManifest()
	require.NoError(t, m.Validate("1.4.0"))
	require.Error(t, m.Validate("2.0.0"))
	m.Permissions.Network = NetworkAllowlist
	require.Error(t, m.Validate("1.4.0"))
	m.Permissions.AllowedDestinations = []string{"api.example.com:443"}
	require.NoError(t, m.Validate("1.4.0"))
}

func TestParserManifestRequiresFileTypesMetadata(t *testing.T) {
	m := validManifest()
	m.ID = "example.parser"
	m.Name = "Example Parser"
	m.ExtensionType = ExtensionParser
	m.Capabilities = []string{"parse"}
	require.ErrorContains(t, m.Validate("1.4.0"), "metadata.file_types")
	m.Metadata = map[string]any{"file_types": []any{".PDF", "docx"}}
	require.NoError(t, m.Validate("1.4.0"))
}

func TestManifestValidatesDeclaredDataScopes(t *testing.T) {
	m := validManifest()
	m.Permissions.Data = &DataPermissions{
		Tenants:        []string{"self"},
		KnowledgeBases: []string{"kb-1"},
		DataSources:    []string{"ds-1"},
	}
	require.NoError(t, m.Validate("1.4.0"))

	m.Permissions.Data.DataSources = []string{""}
	require.ErrorContains(t, m.Validate("1.4.0"), "permissions.data.data_sources")
}

func TestLoadManifestAndDiscover(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "localdir")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte("api_version: weknora.plugin/v1\nid: example.localdir\nname: Local Directory\nversion: 1.0.0\nextension_type: datasource\nprotocol_version: v1\npermissions:\n  network: none\n  data:\n    tenants: [self]\n    knowledge_bases: [self]\n    data_sources: [self]\n"), 0o644))
	manifests, err := Discover([]string{root})
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	require.Equal(t, "example.localdir", manifests[0].ID)
	require.NotNil(t, manifests[0].Permissions.Data)
	require.Equal(t, []string{"self"}, manifests[0].Permissions.Data.Tenants)
}
