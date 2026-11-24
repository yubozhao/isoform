package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDocumentedMobileConnectionFieldsMatchWireContract(t *testing.T) {
	data, err := os.ReadFile("../mobile-protocol/PROTOCOL.md")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)type MobileConnectionStatus struct \{(.*?)\n\}`).FindSubmatch(data)
	if len(block) != 2 {
		t.Fatal("connection status contract missing")
	}
	documented := map[string]bool{}
	for _, match := range regexp.MustCompile("json:\"([^\"]+)\"").FindAllSubmatch(block[1], -1) {
		documented[string(match[1])] = true
	}
	wire := map[string]bool{}
	typ := reflect.TypeOf(MobileConnectionStatus{})
	for i := range typ.NumField() {
		wire[typ.Field(i).Tag.Get("json")] = true
	}
	if !reflect.DeepEqual(documented, wire) {
		t.Fatalf("documented fields %v differ from wire fields %v", documented, wire)
	}
}

func TestShellQuoteDocumentedEscapeRoundTrips(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "ai_provider.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var comment string
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "shellSingleQuote" {
			comment = fn.Doc.Text()
		}
	}
	match := regexp.MustCompile(`escaping any embedded single quote as:\s+([^\n]+)`).FindStringSubmatch(comment)
	if len(match) != 2 {
		t.Fatal("shell quote escape example missing")
	}
	// Execute the documented escape, so incorrect punctuation fails exactly
	// as it would for someone copying it into a POSIX shell.
	example := "'left" + match[1] + "right'"
	for _, quoted := range []string{example, shellSingleQuote("left'right")} {
		output, err := exec.Command("sh", "-c", "printf '%s' "+quoted).CombinedOutput()
		if err != nil || string(output) != "left'right" {
			t.Fatalf("quote example %q returned %q: %v", quoted, output, err)
		}
	}
}

func TestRemovedConnectorHasNoComposerContract(t *testing.T) {
	for _, name := range []string{"frontend/src/components/stream-body.tsx", "frontend/src/hooks/useComposerInput.ts"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			// The dead provider must not remain accepted by either consumer.
			if strings.Contains(strings.ToLower(string(data)), "ring"+"central") {
				t.Fatal("removed connector remains in the composer contract")
			}
		})
	}
}
