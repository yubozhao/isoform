package main

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"wails-gui/internal/agentjoin"
	"wails-gui/internal/workonit"
)

func mcpConnect(t *testing.T, url, name string) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: name, Version: "1"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize %s: %v", name, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mcpCall(t *testing.T, c *mcpclient.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name, req.Params.Arguments = tool, args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var text strings.Builder
	for _, part := range res.Content {
		if tc, ok := part.(mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

// TestAgentJoinsThroughIsoformsTools: an agent adds the tool server with the
// join address and asks to join under its client's name; until the person's
// Yes its tools only say which code to show, and a second client with the
// same address never learns that code. After Yes it has exactly six tools,
// reads its hand-off, posts progress and its result, and Disconnect ends it.
func TestAgentJoinsThroughIsoformsTools(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	base, code, _ := strings.Cut(start.Link, agentjoin.PathPrefix)
	// The page names the tool server's address; the pasted message doesn't.
	_, _, page := agentJoinCall(t, "GET", start.Link, "")
	key := app.agentJoinService().MCPKey()
	mcpURL := base + "/mcp?join=" + key
	if !strings.Contains(page, mcpURL) || strings.Contains(start.Message, key) {
		t.Fatalf("the tool server address: on the page %v, in the message %v", strings.Contains(page, mcpURL), strings.Contains(start.Message, key))
	}

	codex := mcpConnect(t, mcpURL, "Codex")
	join := app.GetAgentJoin()
	if join == nil || join.State != agentjoin.StateRequested || join.Via != "mcp" || join.Name != "Codex" || len(join.Check) != 4 {
		t.Fatalf("join = %+v", join)
	}
	text, isErr := mcpCall(t, codex, "get_handoffs", nil)
	if !isErr || !strings.Contains(text, join.Check) {
		t.Fatalf("before yes: %v %q", isErr, text)
	}
	intruder := mcpConnect(t, mcpURL, "Someone else")
	if text, isErr := mcpCall(t, intruder, "get_handoffs", nil); !isErr || strings.Contains(text, join.Check) {
		t.Fatalf("a second client learned the code: %q", text)
	}
	if j := app.GetAgentJoin(); j.Name != "Codex" || j.Check != join.Check {
		t.Fatalf("the second client replaced the first: %+v", j)
	}

	if err := app.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if app.GetAgentJoin() != nil {
		t.Fatal("the link wasn't spent by the yes")
	}
	connected := app.GetConnectedAgents()
	if len(connected) != 1 || connected[0].CatalogID != "codex" {
		t.Fatalf("connected = %+v", connected)
	}
	// Rotation: whoever read the join page can't use its address after the
	// Yes, neither on a session it already had nor on a new one.
	if _, isErr := mcpCall(t, intruder, "get_handoffs", nil); !isErr {
		t.Fatal("the join page's address still works after Yes")
	}
	late := mcpConnect(t, mcpURL, "Read the page")
	if _, isErr := mcpCall(t, late, "get_handoffs", nil); !isErr {
		t.Fatal("a new client with the join page's address reads hand-offs after Yes")
	}
	// Only the session that completed the code gets its fresh key, once.
	text, isErr = mcpCall(t, codex, "get_handoffs", nil)
	_, fresh, found := strings.Cut(text, "/mcp?key=")
	fresh, _, _ = strings.Cut(fresh, " ")
	if isErr || !found || fresh == "" || fresh == key {
		t.Fatalf("first call after yes: %v %q", isErr, text)
	}
	if text, _ := mcpCall(t, codex, "get_handoffs", nil); strings.Contains(text, fresh) {
		t.Fatal("the fresh key was shown twice")
	}
	if text, _ := mcpCall(t, intruder, "get_handoffs", nil); strings.Contains(text, fresh) {
		t.Fatal("another session saw the fresh key")
	}
	if _, isErr := mcpCall(t, mcpConnect(t, base+"/mcp?key="+fresh, "Codex again"), "get_handoffs", nil); isErr {
		t.Fatal("the fresh key doesn't work")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := codex.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "ask_agent,get_answer,get_handoff,get_handoffs,post_progress,post_result" {
		t.Fatalf("tools = %s", got)
	}

	in := briefInput()
	in.Kind = workonit.KindCode
	prepared, err := app.PrepareWorkOnIt(in)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := app.SendWorkOnIt(prepared.TaskID, "codex", workonit.MethodConnected)
	if err != nil || sent.Via != "inbox" {
		t.Fatalf("send: %+v %v", sent, err)
	}
	text, isErr = mcpCall(t, codex, "get_handoffs", nil)
	if isErr || !strings.Contains(text, "Draft the launch note") || !strings.Contains(text, sent.RecordID) {
		t.Fatalf("get_handoffs: %v %q", isErr, text)
	}
	// With only the pasted message (its join code), nothing opens.
	leaked := mcpConnect(t, base+"/mcp?join="+code, "Leaked message")
	if _, isErr := mcpCall(t, leaked, "get_handoffs", nil); !isErr {
		t.Fatal("the pasted message alone reads hand-offs")
	}
	if text, isErr := mcpCall(t, codex, "post_progress", map[string]any{"id": sent.RecordID, "line": "Drafting the intro."}); isErr {
		t.Fatalf("progress: %q", text)
	}
	if item, ok := app.agentInboxService().Find(sent.RecordID); !ok || item.Progress != "Drafting the intro." {
		t.Fatalf("progress kept = %+v", item)
	}
	text, isErr = mcpCall(t, codex, "post_result", map[string]any{
		"id": sent.RecordID, "summary": "Drafted it.", "result": "# Launch note",
		"files": []any{map[string]any{"name": "outline.md", "text": "- one"}},
	})
	if isErr {
		t.Fatalf("result: %q", text)
	}
	results, _ := app.GetTaskLinkResults()
	if len(results) != 1 || results[0].Summary != "Drafted it." || strings.Join(results[0].Files, ",") != "result.md,outline.md" || results[0].AgentID != "codex" {
		t.Fatalf("results = %+v", results)
	}
	if _, isErr := mcpCall(t, codex, "ask_agent", map[string]any{"to": "claude", "ask": "x"}); !isErr {
		t.Fatal("ask_agent answered before C2 exists")
	}

	if err := app.DisconnectAgent(connected[0].ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr := mcpCall(t, codex, "get_handoffs", nil); !isErr || !strings.Contains(text, "not connected") {
		t.Fatalf("after Disconnect: %v %q", isErr, text)
	}
}
