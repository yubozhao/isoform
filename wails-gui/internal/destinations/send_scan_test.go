package destinations

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The scan: every function in the shipped Go source of wails-gui (with its
// nested quick-chat-spotlight module) and yansu-agent that can send over the
// network names its destination with //isoform:destination. "Can send" is
// decided from the code, not from a list of files:
//
//   - it builds an HTTP request or client (net/http NewRequest*, Get, Post,
//     PostForm, Head, a Client literal, DefaultClient);
//   - it dials (net, crypto/tls, a websocket Dialer);
//   - it constructs a network SDK's client (sdkSenders below);
//   - it runs a program that sends, named by a literal (sendingPrograms).
//
// Every direct dependency in each module's go.mod is classified in
// moduleDeps, so a new dependency that talks to the network can't slip past
// the scan: the test fails until it is classified (and, if it sends, its
// constructors are added to sdkSenders).
//
// Programs found by path at run time (the person's Claude Code and Codex,
// magpie) can't be seen from a literal; the functions that start them are in
// requiredDirectives, so moving or untagging them fails too.

const directivePrefix = "//isoform:destination"

// moduleRoots are scanned relative to this package (wails-gui/internal/destinations).
var moduleRoots = []string{"../..", "../../../yansu-agent"}

// skipDirs are not shipped in the app: developer tools, fixtures, the frontend.
var skipDirs = map[string]bool{
	"frontend": true, "node_modules": true, "build": true, "testdata": true,
	"scripts": true, "vendor": true, ".git": true,
}

// skipPaths are developer-only programs under cmd/ that the app doesn't ship.
var skipPaths = []string{
	"wails-gui/cmd/",
}

var httpSenders = map[string]bool{
	"NewRequest": true, "NewRequestWithContext": true, "Get": true, "Post": true,
	"PostForm": true, "Head": true,
}

var dialSenders = map[string]map[string]bool{
	"net":        {"Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialIP": true},
	"crypto/tls": {"Dial": true, "DialWithDialer": true},
}

var dialerTypes = map[string]map[string]bool{
	"net":                          {"Dialer": true},
	"crypto/tls":                   {"Dialer": true},
	"github.com/gorilla/websocket": {"Dialer": true},
}

// sdkSenders are the constructors of network clients in third-party packages.
var sdkSenders = map[string]map[string]bool{
	"github.com/bwmarrin/discordgo":                {"New": true},
	"github.com/slack-go/slack":                    {"New": true},
	"github.com/slack-go/slack/socketmode":         {"New": true},
	"github.com/larksuite/oapi-sdk-go/v3":          {"NewClient": true},
	"github.com/larksuite/oapi-sdk-go/v3/ws":       {"NewClient": true},
	"github.com/posthog/posthog-go":                {"New": true, "NewWithConfig": true},
	"github.com/mark3labs/mcp-go/client":           {"NewSSEMCPClient": true, "NewStreamableHttpClient": true, "NewClient": true},
	"github.com/mark3labs/mcp-go/client/transport": {"NewSSE": true, "NewStreamableHTTP": true},
	"codeberg.org/readeck/go-readability/v2":       {"FromURL": true},
	"github.com/gorilla/websocket":                 {"DefaultDialer": true},
}

// sendingPrograms are programs that reach the network, when named by a
// literal in exec.Command or exec.CommandContext.
var sendingPrograms = map[string]bool{
	"curl": true, "wget": true, "git": true, "opencli": true, "ssh": true,
	"scp": true, "rsync": true, "npx": true, "npm": true, "pnpm": true,
	"bun": true, "pip": true, "pip3": true, "brew": true,
}

// moduleDeps classifies every direct dependency: true when it can send over
// the network (its constructors must then be in sdkSenders), false when it
// can't.
var moduleDeps = map[string]bool{
	"github.com/Microsoft/go-winio":          false,
	"github.com/aymanbagabas/go-pty":         false,
	"github.com/bwmarrin/discordgo":          true,
	"github.com/creack/pty":                  false,
	"github.com/energye/systray":             false,
	"github.com/go-ole/go-ole":               false,
	"github.com/google/uuid":                 false,
	"github.com/gorilla/websocket":           true,
	"github.com/k2-fsa/sherpa-onnx-go":       false,
	"github.com/larksuite/oapi-sdk-go/v3":    true,
	"github.com/ledongthuc/pdf":              false,
	"github.com/mark3labs/mcp-go":            true,
	"github.com/moutend/go-wca":              false,
	"github.com/mozillazg/go-unidecode":      false,
	"github.com/pdfcpu/pdfcpu":               false,
	"github.com/pkg/errors":                  false,
	"github.com/posthog/posthog-go":          true,
	"github.com/rivo/uniseg":                 false,
	"github.com/robfig/cron/v3":              false,
	"github.com/skratchdot/open-golang":      false,
	"github.com/slack-go/slack":              true,
	"github.com/wailsapp/wails/v2":           false,
	"github.com/xuri/excelize/v2":            false,
	"github.com/yalue/onnxruntime_go":        false,
	"golang.org/x/crypto":                    false,
	"golang.org/x/image":                     false,
	"golang.org/x/sync":                      false,
	"golang.org/x/sys":                       false,
	"golang.org/x/text":                      false,
	"golang.org/x/mod":                       false,
	"gopkg.in/yaml.v2":                       false,
	"modernc.org/sqlite":                     false,
	"yansu-agent":                            false,
	"codeberg.org/readeck/go-readability/v2": true,
	"github.com/bmatcuk/doublestar/v4":       false,
	"github.com/spf13/cobra":                 false,
	"go.starlark.net":                        false,
	"mvdan.cc/sh/v3":                         false,
}

// requiredDirectives are the functions that start a sending program found at
// run time, or otherwise send where the scan can't see it. Each must keep its
// directive with this id.
var requiredDirectives = map[string]ID{
	// Ollama probes and vector calls can use the selected remote address.
	"wails-gui/memory_embedding.go:detectOllama":                 EmbeddingsAPI,
	"wails-gui/memory_embedding.go:ollamaHasModel":               EmbeddingsAPI,
	"wails-gui/memory_embedding.go:openaiProvider.embedBatchRaw": EmbeddingsAPI,
	// The person's Claude Code, for every assistant call and hand-off it takes.
	"yansu-agent/agent/external_runner_controlled.go:ExternalAgentRunner.executeControlled": Assistant,
	// The magpie gateway forwards the native loop's calls to the provider set up.
	"wails-gui/magpie_sidecar.go:magpieSidecar.launch": Assistant,
	// Claude Code with only WebSearch, for topics, Daily News and Chat.
	"wails-gui/research_cli.go:runClaudeResearch": AssistantSearch,
	// Texts sent through Messages on this computer.
	"wails-gui/text_channel_messages.go:runMessagesScript": TextMessages,
	// What the person's assistants read over MCP leaves on stdout.
	"yansu-agent/mcpread/server.go:Serve": AgentsMCPRead,
}

type sendSite struct {
	pos    token.Position
	reason string
}

type taggedFunc struct {
	counters []string
	key      string // path:Func
	pos      token.Position
	ids      []string
	sites    []sendSite
}

func TestEverySendIsListed(t *testing.T) {
	funcs, err := scanSends()
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	seen := map[string]taggedFunc{}
	for _, f := range funcs {
		seen[f.key] = f
		if len(f.sites) == 0 {
			continue
		}
		if len(f.ids) == 0 {
			problems = append(problems, fmt.Sprintf("%s: %s sends (%s at line %d) but names no destination; add %s <id>",
				f.pos.Filename, f.key, f.sites[0].reason, f.sites[0].pos.Line, directivePrefix))
			continue
		}
		for _, id := range f.ids {
			if _, ok := Lookup(ID(id)); !ok {
				problems = append(problems, fmt.Sprintf("%s: %s names %q, which isn't in the list", f.pos.Filename, f.key, id))
			}
		}
	}
	for key, want := range requiredDirectives {
		f, ok := seen[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: required sender not found (moved or renamed? update requiredDirectives)", key))
			continue
		}
		if !containsID(f.ids, string(want)) {
			problems = append(problems, fmt.Sprintf("%s: must name %s", key, want))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("%d sends aren't listed:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}

// A directive on a function that sends nothing is kept honest too: an id
// must still be in the list.
func TestDirectivesNameListedIDs(t *testing.T) {
	funcs, err := scanSends()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range funcs {
		for _, id := range f.ids {
			if _, ok := Lookup(ID(id)); !ok {
				t.Errorf("%s names %q, which isn't in the list", f.key, id)
			}
		}
	}
}

func TestEveryDependencyIsClassified(t *testing.T) {
	for _, root := range moduleRoots {
		for _, mod := range []string{filepath.Join(root, "go.mod"), filepath.Join(root, "quick-chat-spotlight", "go.mod")} {
			data, err := os.ReadFile(mod)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, dep := range directDeps(string(data)) {
				sends, ok := moduleDeps[dep]
				if !ok {
					t.Errorf("%s: dependency %s isn't classified in moduleDeps; if it can send over the network, add its constructors to sdkSenders", mod, dep)
					continue
				}
				if sends && !depHasSenders(dep) {
					t.Errorf("%s: %s can send but no constructor of it is in sdkSenders", mod, dep)
				}
			}
		}
	}
}

func TestListIsWellFormed(t *testing.T) {
	ids := map[ID]bool{}
	for _, d := range List() {
		if ids[d.ID] {
			t.Errorf("duplicate id %s", d.ID)
		}
		ids[d.ID] = true
		if d.Where == "" || d.What == "" || d.When == "" || d.Content == "" {
			t.Errorf("%s: every field is required", d.ID)
		}
	}
}

func depHasSenders(dep string) bool {
	for pkg := range sdkSenders {
		if pkg == dep || strings.HasPrefix(pkg, dep+"/") {
			return true
		}
	}
	return false
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func directDeps(gomod string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}
		var spec string
		if inBlock {
			spec = line
		} else if strings.HasPrefix(line, "require ") {
			spec = strings.TrimPrefix(line, "require ")
		} else {
			continue
		}
		if spec == "" || strings.Contains(spec, "// indirect") {
			continue
		}
		if fields := strings.Fields(spec); len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}

func scanSends() ([]taggedFunc, error) {
	var out []taggedFunc
	fset := token.NewFileSet()
	for _, root := range moduleRoots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		repo := filepath.Dir(absRoot)
		err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != absRoot) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repo, path)
			rel = filepath.ToSlash(rel)
			for _, skip := range skipPaths {
				if strings.HasPrefix(rel, skip) {
					return nil
				}
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			out = append(out, scanFile(fset, rel, file)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func scanFile(fset *token.FileSet, rel string, file *ast.File) []taggedFunc {
	imports := map[string]string{} // local name -> import path
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		switch {
		case imp.Name != nil:
			name = imp.Name.Name
		case strings.HasPrefix(path, "github.com/larksuite/oapi-sdk-go/v3") && name == "v3":
			name = "lark"
		case path == "codeberg.org/readeck/go-readability/v2":
			name = "readability"
		case path == "github.com/posthog/posthog-go":
			name = "posthog"
		}
		imports[name] = path
	}
	var out []taggedFunc
	for _, decl := range file.Decls {
		var doc *ast.CommentGroup
		var name string
		switch d := decl.(type) {
		case *ast.FuncDecl:
			doc, name = d.Doc, d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
			}
		case *ast.GenDecl:
			doc = d.Doc
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					if vs.Doc != nil && doc == nil {
						doc = vs.Doc
					}
					if len(vs.Names) > 0 && name == "" {
						name = vs.Names[0].Name
					}
				}
			}
			if name == "" {
				continue
			}
		default:
			continue
		}
		f := taggedFunc{key: rel + ":" + name, pos: fset.Position(decl.Pos()), ids: directiveIDs(doc)}
		ast.Inspect(decl, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && imports[pkg.Name] == "wails-gui/internal/destinations" {
						switch sel.Sel.Name {
						case "RecordSearch":
							f.counters = append(f.counters, "AssistantSearch")
						case "RecordAnswer":
							f.counters = append(f.counters, "AgentsMCPRead")
						case "Record", "CountRequest", "CountHTTP":
							if len(call.Args) > 0 {
								if id, ok := call.Args[len(call.Args)-1].(*ast.SelectorExpr); ok {
									f.counters = append(f.counters, id.Sel.Name)
								}
							}
						}
					}
				}
			}
			if reason := sendReason(n, imports); reason != "" {
				f.sites = append(f.sites, sendSite{pos: fset.Position(n.Pos()), reason: reason})
			}
			return true
		})
		if len(f.sites) > 0 || len(f.ids) > 0 || len(f.counters) > 0 {
			out = append(out, f)
		}
	}
	return out
}

func recvName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	}
	return "?"
}

func directiveIDs(doc *ast.CommentGroup) []string {
	if doc == nil {
		return nil
	}
	var ids []string
	for _, c := range doc.List {
		if !strings.HasPrefix(c.Text, directivePrefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(c.Text, directivePrefix))
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			ids = append(ids, "")
			continue
		}
		for _, id := range strings.Split(fields[0], ",") {
			ids = append(ids, strings.TrimSpace(id))
		}
	}
	return ids
}

// sendReason says why n sends, or "" when it doesn't.
func sendReason(n ast.Node, imports map[string]string) string {
	pkgSel := func(expr ast.Expr) (string, string) {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return "", ""
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return "", ""
		}
		path, ok := imports[id.Name]
		if !ok {
			return "", ""
		}
		return path, sel.Sel.Name
	}
	switch x := n.(type) {
	case *ast.CallExpr:
		path, fn := pkgSel(x.Fun)
		switch {
		case path == "net/http" && httpSenders[fn]:
			return "http." + fn
		case dialSenders[path][fn]:
			return path + "." + fn
		case sdkSenders[path][fn]:
			return path + "." + fn
		case path == "os/exec" && (fn == "Command" || fn == "CommandContext"):
			args := x.Args
			if fn == "CommandContext" && len(args) > 0 {
				args = args[1:]
			}
			if len(args) > 0 {
				if lit, ok := args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					prog, _ := strconv.Unquote(lit.Value)
					if sendingPrograms[filepath.Base(prog)] {
						return "exec " + prog
					}
				}
			}
		}
	case *ast.CompositeLit:
		path, typ := pkgSel(x.Type)
		if path == "net/http" && typ == "Client" {
			return "http.Client"
		}
		if dialerTypes[path][typ] {
			return path + "." + typ
		}
	case *ast.SelectorExpr:
		path, name := pkgSel(x)
		if path == "net/http" && name == "DefaultClient" {
			return "http.DefaultClient"
		}
		if path == "github.com/gorilla/websocket" && name == "DefaultDialer" {
			return "websocket.DefaultDialer"
		}
	}
	return ""
}

// The scan itself is tested on source it is handed: a send with no
// directive, one with an unknown id, and one under a renamed import are all
// found.
func TestScanFindsUnlistedSends(t *testing.T) {
	const src = `package p

import (
	"net/http"
	ws "github.com/gorilla/websocket"
	"os/exec"
)

func untagged() { http.Post("https://example.com", "", nil) }

//isoform:destination email
func tagged() { _ = &http.Client{} }

//isoform:destination nowhere
func unknown() { _ = ws.Dialer{} }

func runs() { exec.Command("curl", "https://example.com").Run() }

func local() { exec.Command("ls").Run() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]taggedFunc{}
	for _, f := range scanFile(fset, "p.go", file) {
		got[strings.TrimPrefix(f.key, "p.go:")] = f
	}
	if f := got["untagged"]; len(f.sites) != 1 || len(f.ids) != 0 {
		t.Errorf("untagged: %+v", f)
	}
	if f := got["tagged"]; len(f.sites) != 1 || !containsID(f.ids, "email") {
		t.Errorf("tagged: %+v", f)
	}
	if f := got["unknown"]; len(f.sites) != 1 || !containsID(f.ids, "nowhere") {
		t.Errorf("unknown: %+v", f)
	}
	if _, ok := Lookup("nowhere"); ok {
		t.Fatal("nowhere must not be listed")
	}
	if f := got["runs"]; len(f.sites) != 1 {
		t.Errorf("runs: %+v", f)
	}
	if _, ok := got["local"]; ok {
		t.Error("a local program is not a send")
	}
}

func TestEveryContentDestinationHasACounter(t *testing.T) {
	funcs, err := scanSends()
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, f := range funcs {
		for _, id := range f.counters {
			recorded[strings.ToLower(id)] = true
		}
	}
	for _, d := range List() {
		switch d.ID {
		case Assistant, AgentsHandoff, EmbeddingsAPI: // Assistant calls, handoff ledger and finding_embedding_calls.
			continue
		case Relay, Push: // Pairing is shown without a weekly number.
			continue
		}
		if d.Content != ContentNone && !recorded[strings.ReplaceAll(string(d.ID), "_", "")] {
			t.Errorf("%s has content but no runtime counter", d.ID)
		}
	}
}

func TestTavilyRemoved(t *testing.T) {
	if _, ok := Lookup("tavily"); ok {
		t.Error("retired Tavily destination still listed")
	}
	for _, root := range moduleRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && skipDirs[entry.Name()] {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "TAVILY_API_KEY") || strings.Contains(string(src), "api.tavily.com") {
				t.Errorf("%s references retired Tavily configuration or transport", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
