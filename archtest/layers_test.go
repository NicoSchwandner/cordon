package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/NicoSchwandner/cordon/internal"

// layerRules defines which layers are forbidden from importing which other layers.
// Key = layer prefix, Value = list of forbidden import prefixes.
var layerRules = map[string][]string{
	"internal/domain": {
		"internal/application",
		"internal/infrastructure",
		"internal/api",
	},
	"internal/application": {
		"internal/infrastructure",
		"internal/api",
	},
	"internal/infrastructure": {
		"internal/api",
	},
}

func TestLayerDependencies(t *testing.T) {
	root := findProjectRoot(t)
	fset := token.NewFileSet()

	for layer, forbidden := range layerRules {
		layerDir := filepath.Join(root, layer)
		if _, err := os.Stat(layerDir); os.IsNotExist(err) {
			continue
		}

		err := filepath.Walk(layerDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			// Skip test files
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Errorf("failed to parse %s: %v", path, parseErr)
				return nil
			}

			relPath, _ := filepath.Rel(root, path)
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				for _, f := range forbidden {
					forbiddenFull := "github.com/NicoSchwandner/cordon/" + f
					if strings.HasPrefix(importPath, forbiddenFull) {
						t.Errorf("VIOLATION: %s imports %s (layer %q must not import %q)",
							relPath, importPath, layer, f)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", layer, err)
		}
	}
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up to find go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
