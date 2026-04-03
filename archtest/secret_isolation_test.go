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

// TestSecretIsolation ensures that no API response types contain fields
// that could accidentally expose real credentials.
func TestNoCredentialFieldsInAPI(t *testing.T) {
	root := findProjectRoot(t)
	apiDir := filepath.Join(root, "internal", "api")

	if _, err := os.Stat(apiDir); os.IsNotExist(err) {
		return
	}

	forbiddenFields := []string{
		"Password", "SecretValue", "Credential", "RealURL",
		"RealPassword", "RealSecret", "PlaintextSecret",
	}

	fset := token.NewFileSet()
	err := filepath.Walk(apiDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					for _, forbidden := range forbiddenFields {
						if name.Name == forbidden {
							t.Errorf("VIOLATION: %s struct %s has field %q — real credentials must never appear in API types",
								relPath, ts.Name.Name, name.Name)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking api dir: %v", err)
	}
}

// TestAuditWriteBeforeResponse verifies that the pipeline writes audit entries
// before returning responses. This is a structural check — it ensures the audit
// store Write call exists in the pipeline process flow.
func TestAuditWriteInPipeline(t *testing.T) {
	root := findProjectRoot(t)
	pipelinePath := filepath.Join(root, "internal", "application", "proxy", "pipeline.go")

	data, err := os.ReadFile(pipelinePath)
	if err != nil {
		t.Skipf("pipeline.go not found: %v", err)
	}

	content := string(data)

	// Verify writeAudit is called in the Process method
	if !strings.Contains(content, "p.writeAudit(ctx,") {
		t.Error("VIOLATION: Pipeline.Process must call writeAudit before returning")
	}

	// Verify every return path in Process writes audit
	// Count returns after "func (p *Pipeline) Process"
	processIdx := strings.Index(content, "func (p *Pipeline) Process")
	if processIdx == -1 {
		t.Fatal("Pipeline.Process method not found")
	}
	processBody := content[processIdx:]

	// Each return that returns a ProxyResponse should have a writeAudit before it
	// This is a heuristic — better than nothing
	returnCount := strings.Count(processBody, "return ProxyResponse{")
	auditCount := strings.Count(processBody, "p.writeAudit(ctx,")
	if auditCount < returnCount {
		t.Errorf("VIOLATION: Pipeline.Process has %d return paths but only %d writeAudit calls — every path must audit",
			returnCount, auditCount)
	}
}
