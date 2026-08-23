package docparser

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type testEngineRegistration struct{ name string }

func (e testEngineRegistration) Name() string          { return e.name }
func (testEngineRegistration) Description() string     { return "test" }
func (testEngineRegistration) FileTypes(bool) []string { return []string{"txt"} }
func (testEngineRegistration) CheckAvailable(bool, map[string]string) (bool, string) {
	return true, ""
}
func (testEngineRegistration) NewReader(context.Context, ReaderDeps) (interfaces.DocReader, error) {
	return &SimpleFormatReader{}, nil
}

func TestListAllEnginesBuiltinIncludesDocumentFormats(t *testing.T) {
	engines := ListAllEngines(true, nil, nil)
	for _, engine := range engines {
		if engine.Name != "builtin" {
			continue
		}
		if !engine.Available {
			t.Fatalf("builtin engine is unavailable: %s", engine.UnavailableReason)
		}

		fileTypes := make(map[string]bool, len(engine.FileTypes))
		for _, fileType := range engine.FileTypes {
			fileTypes[fileType] = true
		}
		for _, want := range []string{"html", "htm", "xmind"} {
			if !fileTypes[want] {
				t.Errorf("builtin engine file types do not include %q: %v", want, engine.FileTypes)
			}
		}
		return
	}

	t.Fatal("builtin engine not found")
}

func TestRegisterEngineRejectsDuplicateNames(t *testing.T) {
	name := "test_duplicate_engine"
	first := testEngineRegistration{name: name}
	if err := RegisterEngine(first); err != nil {
		t.Fatalf("RegisterEngine: %v", err)
	}
	t.Cleanup(func() { UnregisterEngine(name) })
	if err := RegisterEngine(testEngineRegistration{name: name}); err == nil {
		t.Fatal("RegisterEngine accepted a duplicate parser engine name")
	}
}
