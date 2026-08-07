package runs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const generatedProtoImportPrefix = "agent-compose/proto/"

func TestProductionCodeDoesNotDependOnTransportPackages(t *testing.T) {
	t.Parallel()

	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dependency contract source path")
	}
	packageDir := filepath.Dir(sourcePath)
	files, err := filepath.Glob(filepath.Join(packageDir, "*.go"))
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}

	forbidden := []string{"net/http", "connectrpc.com/connect"}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", filepath.Base(path), err)
			}
			for _, prefix := range forbidden {
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					t.Errorf("%s imports transport package %q", filepath.Base(path), importPath)
				}
			}
		}
	}
}

func TestAttachBoundaryDoesNotDependOnGeneratedProto(t *testing.T) {
	t.Parallel()

	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dependency contract source path")
	}
	packageDir := filepath.Dir(sourcePath)
	for _, name := range []string{"attach_input.go", "attach_output.go"} {
		file := parseDependencyContractFile(t, filepath.Join(packageDir, name))
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", name, err)
			}
			if strings.HasPrefix(importPath, generatedProtoImportPrefix) {
				t.Errorf("%s imports generated protobuf package %q", name, importPath)
			}
		}
	}

	controller := parseDependencyContractFile(t, filepath.Join(packageDir, "controller.go"))
	ast.Inspect(controller, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && identifier.Name == "agentcomposev2" && strings.HasPrefix(selector.Sel.Name, "Attach") {
			t.Errorf("controller.go references generated attach type agentcomposev2.%s", selector.Sel.Name)
		}
		return true
	})
}

func parseDependencyContractFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
	return file
}
