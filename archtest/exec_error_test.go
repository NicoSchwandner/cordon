package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecErrorsMustBeHandled ensures that calls to backend.Exec (which return
// (int, error)) never silently discard the error. The bug this prevents: Exec
// previously returned nil error on non-zero exit codes, and callers that did
// `_, err := backend.Exec(...)` never caught command failures.
//
// Now that Exec returns error on non-zero exit, the remaining risk is callers
// that discard both return values without logging. This test flags those.
func TestExecErrorsMustBeHandled(t *testing.T) {
	root := findProjectRoot(t)
	appDir := filepath.Join(root, "internal", "application")
	fset := token.NewFileSet()

	err := filepath.Walk(appDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if parseErr != nil {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}

			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}

			// Match calls like o.backend.Exec(...) or b.Exec(...)
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			methodName := sel.Sel.Name
			if methodName != "Exec" && methodName != "ExecWithOutput" && methodName != "ExecRaw" {
				return true
			}

			// Check if ALL lhs are blank identifiers — that means both exit code
			// and error are discarded
			allBlank := true
			for _, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); !ok || ident.Name != "_" {
					allBlank = false
					break
				}
			}

			if allBlank && len(assign.Lhs) >= 2 {
				pos := fset.Position(assign.Pos())
				t.Errorf("VIOLATION: %s:%d — %s() called with all return values discarded; at minimum check the error",
					relPath, pos.Line, methodName)
			}

			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/application/: %v", err)
	}
}
