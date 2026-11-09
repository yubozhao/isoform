package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Keep the profile implementation away from the direct entry points to
// private stores. This supplements, rather than replaces, source-flow tests.
func TestWorkProfileBannedSourceIdentifiers(t *testing.T) {
	banned := map[string]bool{
		"GetMemoryEntries": true, "memoryDir": true, "GetSessionEvents": true,
		"GetSessionEventsIncludingProtected": true, "GetMailOverview": true,
		"GetAboutYou": true, "GetAboutYouPortrait": true,
		"ListCurrentSelfModelEntries": true, "GetPrincipalRecentMessages": true,
	}
	files, err := filepath.Glob("work_profile*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && banned[id.Name] {
				t.Errorf("%s references banned source %s", fset.Position(id.Pos()), id.Name)
			}
			return true
		})
	}
}

// Follow local helper calls as well as the concrete source and model methods;
// this catches a private-store loader hidden one or more functions away.
func TestWorkProfileSourceCallGraphExcludesPrivateStores(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	functions := make(map[string][]*ast.FuncDecl)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body != nil {
				functions[fn.Name.Name] = append(functions[fn.Name.Name], fn)
			}
		}
	}
	banned := map[string]bool{
		"memoryDir": true, "GetMemoryEntries": true, "GetSessionEvents": true, "GetSessionEventsIncludingProtected": true,
		"GetMailOverview": true, "ListMailMessages": true, "GetPrincipalRecentMessages": true,
		"GetAboutYou": true, "GetAboutYouPortrait": true, "portraitPath": true,
		"ListCurrentSelfModelEntries": true, "readPortrait": true, "GetContacts": true,
		"GetChatHistory": true, "LoadAssistantHistory": true,
		"GetMemoryEntry": true, "SearchMemory": true, "GetMemoryEvidence": true,
		"GetSnapshotOCRText": true, "GetOCRTextBySnapshot": true, "GetExactOCRTextBySnapshot": true, "GetOCRFramesBySession": true, "GetStoredOCRBoxes": true,
		"ListSystemContacts": true, "ListImportedContactCards": true, "LoadConversationHistory": true, "GetAssistantHistory": true,
		"GetSelfModelEntryByID": true, "GetLatestSelfModelEntry": true, "ListSelfModelEntriesAt": true, "ListSelfModelEntries": true,
	}
	queue := []string{"workProfileMakeDraft", "workProfileCandidates", "workProfileGenerate", "workProfileAssembleDraft", "workProfilePreview", "KnowHow", "Insights", "yourAgentDirFingerprint"}
	seen := make(map[*ast.FuncDecl]bool)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, fn := range functions[name] {
			if seen[fn] {
				continue
			}
			seen[fn] = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if id, ok := node.(*ast.Ident); ok && banned[id.Name] {
					t.Errorf("%s reaches private store %s", fset.Position(id.Pos()), id.Name)
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch target := call.Fun.(type) {
				case *ast.Ident:
					if len(functions[target.Name]) > 0 {
						queue = append(queue, target.Name)
					}
				case *ast.SelectorExpr:
					switch target.Sel.Name {
					case "KnowHow", "Insights", "Generate":
						queue = append(queue, target.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
