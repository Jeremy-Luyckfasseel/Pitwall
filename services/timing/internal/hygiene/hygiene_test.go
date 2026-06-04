// Package hygiene holds source-level guard tests. It has no production code.
package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AC2: structured logging only — no bare fmt.Print*/println to stdout anywhere in
// the service's production code. The logger (internal/logging) is the only sink.
// This inspects the AST (not text) so doc comments and strings never false-positive.
func TestNoBarePrintStatements(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	fset := token.NewFileSet()

	var offenders []string
	err := filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name := offendingCall(call.Fun); name != "" {
				rel, _ := filepath.Rel(moduleRoot, path)
				offenders = append(offenders, rel+":"+itoa(fset.Position(call.Pos()).Line)+"  "+name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk error: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("found bare print statements (use the structured logger instead):\n%s",
			strings.Join(offenders, "\n"))
	}
}

// offendingCall returns the offending call name (or "") for fmt.Print*/println.
func offendingCall(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok && pkg.Name == "fmt" {
			switch e.Sel.Name {
			case "Print", "Printf", "Println":
				return "fmt." + e.Sel.Name
			}
		}
	case *ast.Ident:
		if e.Name == "print" || e.Name == "println" {
			return e.Name + " (builtin)"
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
