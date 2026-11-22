package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readPrefixes name the Store methods that only read.
var readPrefixes = []string{"Get", "List", "Load", "Count", "Find", "Read", "Is", "Has"}

// readsOnWriter lists reads that require the writer connection. Document
// the connection-specific reason for each exception.
var readsOnWriter = map[string]bool{
	"ReadRevision": true, // total_changes() and data_version must be compared on the fence's writer connection.
}

// TestReadOnlyStoreMethodsUseTheReadPool fails when a read-only Store method
// uses s.db directly, unless it is listed in readsOnWriter.
func TestReadOnlyStoreMethodsUseTheReadPool(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || !fn.Name.IsExported() || !isReadName(fn.Name.Name) {
				continue
			}
			recv := receiverOfStore(fn)
			if recv == "" {
				continue
			}
			if usesField(fn.Body, recv, "db") {
				found = append(found, fn.Name.Name)
			}
		}
	}
	sort.Strings(found)
	var added []string
	seen := map[string]bool{}
	for _, name := range found {
		seen[name] = true
		if !readsOnWriter[name] {
			added = append(added, name)
		}
	}
	if len(added) > 0 {
		t.Errorf("read-only Store methods on the writer connection (s.db): %s\nRead on s.streamReader(), or list the method in readsOnWriter with its reason.", strings.Join(added, ", "))
	}
	var moved []string
	for name := range readsOnWriter {
		if !seen[name] {
			moved = append(moved, name)
		}
	}
	sort.Strings(moved)
	if len(moved) > 0 {
		t.Errorf("no longer on the writer, take them off readsOnWriter: %s", strings.Join(moved, ", "))
	}
}

func isReadName(name string) bool {
	if name == "PrincipalIDs" {
		return true
	}
	for _, p := range readPrefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) && strings.ToUpper(name[len(p):len(p)+1]) == name[len(p):len(p)+1] {
			return true
		}
	}
	return false
}

// receiverOfStore is the receiver's name when the method is on *Store.
func receiverOfStore(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
		return ""
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Store" {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func usesField(body *ast.BlockStmt, recv, field string) bool {
	hit := false
	ast.Inspect(body, func(n ast.Node) bool {
		// The rollback switch's own branch is meant to reach the writer.
		if stmt, ok := n.(*ast.IfStmt); ok {
			if id, ok := stmt.Cond.(*ast.Ident); ok && id.Name == "UIStateOnWriter" {
				return false
			}
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
				hit = true
			}
		}
		return !hit
	})
	return hit
}
