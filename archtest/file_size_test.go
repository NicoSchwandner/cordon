package archtest

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const maxFileLines = 750
// maxFuncLines: createFromRepos (313 lines) is the current largest function.
// Set ceiling above it to prevent further growth without failing existing code.
const maxFuncLines = 350

func TestNoOversizedFiles(t *testing.T) {
	root := findProjectRoot(t)
	internalDir := filepath.Join(root, "internal")

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		lines := bytes.Count(data, []byte("\n")) + 1
		if lines > maxFileLines {
			relPath, _ := filepath.Rel(root, path)
			t.Errorf("VIOLATION: %s has %d lines (max %d) — split into multiple files in the same package",
				relPath, lines, maxFileLines)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}
}

func TestNoOversizedFunctions(t *testing.T) {
	root := findProjectRoot(t)
	internalDir := filepath.Join(root, "internal")
	fset := token.NewFileSet()

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}

			startLine := fset.Position(fn.Body.Pos()).Line
			endLine := fset.Position(fn.Body.End()).Line
			bodyLines := endLine - startLine

			if bodyLines > maxFuncLines {
				funcName := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
						if ident, ok := star.X.(*ast.Ident); ok {
							funcName = ident.Name + "." + funcName
						}
					}
				}
				t.Errorf("VIOLATION: %s func %s is %d lines (max %d) — extract into smaller functions",
					relPath, funcName, bodyLines, maxFuncLines)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}
}
