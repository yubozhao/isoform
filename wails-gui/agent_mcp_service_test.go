package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	app := newAgentJoinTestApp(t)
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

	if err := app.DisconnectAgent(connected[0].ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr := mcpCall(t, codex, "get_handoffs", nil); !isErr || !strings.Contains(text, "not connected") {
		t.Fatalf("after Disconnect: %v %q", isErr, text)
	}
}

// TestJoinedAgentUnderClosedEyes (agents G2): an agent joins through the
// tools with Closed Eyes on, but nothing is read or handed to it until
// Closed Eyes ends; then its hand-offs answer with no switch set, and
// Disconnect ends it.
func TestJoinedAgentUnderClosedEyes(t *testing.T) {
	app := newAgentJoinTestApp(t)
	app.closedEyes.enabled.Store(true)
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatalf("start under Closed Eyes: %v", err)
	}
	base, _, _ := strings.Cut(start.Link, agentjoin.PathPrefix)
	codex := mcpConnect(t, base+"/mcp?join="+app.agentJoinService().MCPKey(), "Codex")
	join := app.GetAgentJoin()
	if join == nil || join.State != agentjoin.StateRequested {
		t.Fatalf("join under Closed Eyes = %+v", join)
	}
	if text, isErr := mcpCall(t, codex, "get_handoffs", nil); !isErr || !strings.Contains(text, join.Check) {
		t.Fatalf("code under Closed Eyes: %v %q", isErr, text)
	}
	if err := app.AnswerAgentJoin(true); err != nil {
		t.Fatalf("yes under Closed Eyes: %v", err)
	}
	connected := app.GetConnectedAgents()
	if len(connected) != 1 {
		t.Fatalf("connected = %+v", connected)
	}
	// The first call hands over the agent's own key but reads nothing.
	text, isErr := mcpCall(t, codex, "get_handoffs", nil)
	if !isErr || !strings.Contains(text, "/mcp?key=") || !strings.Contains(text, "isn't taking agent work") {
		t.Fatalf("first call under Closed Eyes: %v %q", isErr, text)
	}
	if _, isErr := mcpCall(t, codex, "post_progress", map[string]any{"id": "x", "line": "y"}); !isErr {
		t.Fatal("post_progress answered under Closed Eyes")
	}
	// No new hand-off goes to it while Closed Eyes is on.
	if app.connectedHandoffReady("codex") {
		t.Fatal("a connected hand-off is ready under Closed Eyes")
	}
	if _, err := app.sendWorkOnItConnected(workonit.Task{Text: "x"}, "codex", "rec-1"); err != errTaskLinksClosedEyes {
		t.Fatalf("connected hand-off under Closed Eyes: %v", err)
	}

	app.closedEyes.enabled.Store(false)
	if text, isErr := mcpCall(t, codex, "get_handoffs", nil); isErr {
		t.Fatalf("get_handoffs with no switch set: %q", text)
	}
	if err := app.DisconnectAgent(connected[0].ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr := mcpCall(t, codex, "get_handoffs", nil); !isErr || !strings.Contains(text, "not connected") {
		t.Fatalf("after Disconnect: %v %q", isErr, text)
	}
}

func joinRequestAgent(t *testing.T, app *App, name string) (*mcpclient.Client, string) {
	t.Helper()
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	c := mcpConnect(t, start.MCPAddress, name)
	if err := app.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if text, failed := mcpCall(t, c, "get_handoffs", nil); failed {
		t.Fatal(text)
	}
	for _, agent := range app.GetConnectedAgents() {
		if agent.Name == name {
			return c, agent.ID
		}
	}
	t.Fatalf("missing joined agent %s", name)
	return nil, ""
}

func TestAgentMCPAskAgent(t *testing.T) {
	app := newAgentJoinTestApp(t)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	asker, from := joinRequestAgent(t, app, "Codex")
	target, to := joinRequestAgent(t, app, "Claude")
	ask := "summarize the pricing tiers"
	for input, want := range map[string]string{"cOdEx": "You can't ask yourself.", from: "You can't ask yourself.", "unknown": "Your person has no agent called unknown.", "Grok": "Grok can't be asked through Isoform yet. Say what you need in your result instead."} {
		if text, failed := mcpCall(t, asker, "ask_agent", map[string]any{"to": input, "ask": ask}); !failed || text != want {
			t.Fatalf("ask %s: %v %q", input, failed, text)
		}
	}
	app.closedEyes.enabled.Store(true)
	// Closed Eyes refuses the tool call itself (agents G2: the transport stays up for joining).
	if text, failed := mcpCall(t, asker, "ask_agent", map[string]any{"to": to, "ask": ask}); !failed || !strings.Contains(text, "isn't taking agent work") {
		t.Fatalf("Closed Eyes: %v %q", failed, text)
	}
	app.closedEyes.enabled.Store(false)
	if items := app.workRequestItems(time.Now()); len(items) != 0 {
		t.Fatalf("refused requests: %+v", items)
	}

	create := func(address string) string {
		t.Helper()
		text, failed := mcpCall(t, asker, "ask_agent", map[string]any{"to": address, "ask": ask})
		id := strings.TrimSuffix(strings.TrimPrefix(text, "Asked. Your person says yes or no in Isoform. Request id: "), ". Check with get_answer.")
		r, ok := app.getAgentRequest(id)
		if failed || !ok || r.From != from || r.To != to || r.Sentence != "Codex wants Claude to "+ask || r.Moves != ask || r.HandoffID != "" {
			t.Fatalf("ask: %q %+v", text, r)
		}
		return id
	}
	answer := func(id, action string) {
		t.Helper()
		if _, err := app.AnswerWork(workRequestID(id), WorkAnswerInput{Action: action}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) string {
		t.Helper()
		text, failed := mcpCall(t, asker, "get_answer", map[string]any{"id": id})
		if failed {
			t.Fatal(text)
		}
		return text
	}
	id := create("cLaUdE")
	items := app.workRequestItems(time.Now())
	if len(items) != 1 || items[0].Request.From.AgentID != from || items[0].Request.To.AgentID != to || items[0].Work.Text != ask || items[0].Work.Title != "Codex wants Claude to "+ask {
		t.Fatalf("Your work: %+v", items)
	}
	if got := get(id); !strings.HasPrefix(got, "Waiting for your person's yes, until ") {
		t.Fatal(got)
	}
	if got, failed := mcpCall(t, target, "get_answer", map[string]any{"id": id}); !failed || got != "There is no request with that id." {
		t.Fatalf("not owner: %q", got)
	}
	answer(id, workActionNo)
	if got := get(id); got != "Your person said no." {
		t.Fatal(got)
	}
	if got := app.agentInboxService().Items(); len(got) != 0 {
		t.Fatalf("No delivered %+v", got)
	}
	id = create(strings.ToUpper(to))
	if err := updateAgentRequest(id, time.Now(), func(r *AgentRequest) error { r.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli(); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := get(id); got != "No answer from your person." {
		t.Fatal(got)
	}

	id = create(to)
	answer(id, workActionYes)
	if _, err := app.AnswerWork(workRequestID(id), WorkAnswerInput{Action: workActionYes}); err == nil {
		t.Fatal("duplicate approval accepted")
	}
	r, _ := app.getAgentRequest(id)
	handoffs, err := app.GetWorkOnItHandoffs()
	if err != nil || len(handoffs) != 1 || handoffs[0].ID != r.TargetHandoffID || handoffs[0].Title != r.Sentence || handoffs[0].Text != r.Moves {
		t.Fatalf("handoffs: %+v %v", handoffs, err)
	}
	pending := app.agentInboxService().ForAgent(to, false)
	if len(pending) != 1 || pending[0].ID != r.TargetHandoffID || pending[0].Text != ask || pending[0].SourceID != id {
		t.Fatalf("target inbox: %+v", pending)
	}
	if got := get(id); got != "Your person said yes. Claude has it; no answer yet." {
		t.Fatal(got)
	}
	if text, failed := mcpCall(t, target, "post_result", map[string]any{"id": r.TargetHandoffID, "summary": "Two tiers.", "result": "Basic and Pro.\n", "files": []any{map[string]any{"name": "notes.txt", "text": "private attachment"}}}); failed {
		t.Fatal(text)
	}
	var result map[string]any
	text := get(id)
	if err := json.Unmarshal([]byte(text), &result); err != nil || len(result) != 3 || result["summary"] != "Two tiers." || result["result"] != "Basic and Pro.\n" || !reflect.DeepEqual(result["files"], []any{"result.md", "notes.txt"}) {
		t.Fatalf("answer: %q", text)
	}

	// The second request must not read the first one's completed result.
	id = create(to)
	answer(id, workActionYes)
	if got := get(id); got != "Your person said yes. Claude has it; no answer yet." {
		t.Fatal(got)
	}
	// Even an attachment named result.md contributes only its name.
	r, _ = app.getAgentRequest(id)
	if text, failed := mcpCall(t, target, "post_result", map[string]any{"id": r.TargetHandoffID, "summary": "Attached.", "files": []any{map[string]any{"name": "result.md", "text": "attachment contents"}}}); failed {
		t.Fatal(text)
	}
	text = get(id)
	if err := json.Unmarshal([]byte(text), &result); err != nil || result["summary"] != "Attached." || result["result"] != "" || strings.Contains(text, "attachment contents") {
		t.Fatalf("attachment-only answer: %q", text)
	}

	id = create(to)
	// A separate connected requester can still read its send failure.
	observer, _ := joinRequestAgent(t, app, "Atlas")
	message, failed := mcpCall(t, observer, "ask_agent", map[string]any{"to": to, "ask": ask})
	if failed {
		t.Fatal(message)
	}
	failureID := strings.TrimSuffix(strings.TrimPrefix(message, "Asked. Your person says yes or no in Isoform. Request id: "), ". Check with get_answer.")
	if err := app.DisconnectAgent(from); err != nil {
		t.Fatal(err)
	}
	if text, failed := mcpCall(t, asker, "get_answer", map[string]any{"id": id}); !failed || !strings.Contains(text, "not connected") {
		t.Fatalf("disconnected asker: %q", text)
	}
	answer(id, workActionYes)
	r, _ = app.getAgentRequest(id)
	if item, ok := app.agentInboxService().Find(r.TargetHandoffID); !ok || item.AgentID != to || item.Text != ask {
		t.Fatalf("delivery after asker disconnected: %+v", item)
	}
	if err := app.DisconnectAgent(to); err != nil {
		t.Fatal(err)
	}
	answer(failureID, workActionYes)
	if got, failed := mcpCall(t, observer, "get_answer", map[string]any{"id": failureID}); failed || got != "Your person said yes, but Claude couldn't be reached." {
		t.Fatal(got)
	}
	r, _ = app.getAgentRequest(failureID)
	if r.Answer != agentRequestYes || r.SendError == "" || r.SendReason != "disconnected" || r.TargetHandoffID != "" {
		t.Fatalf("target disconnected: %+v", r)
	}
	if err := updateAgentRequest(failureID, time.Now(), func(saved *AgentRequest) error {
		saved.ExpiresAt = time.Now().UnixMilli()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, failed := mcpCall(t, observer, "get_answer", map[string]any{"id": failureID}); failed || got != "No answer from Claude; the request ran out." {
		t.Fatal(got)
	}
}

// The receipt can be interrupted before the inbox write, after it, or after
// the target posts a result. Only the first case needs another delivery.
func TestAgentRequestAgainAfterInterruptedInboxDelivery(t *testing.T) {
	app := newAgentJoinTestApp(t)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	asker, from := joinRequestAgent(t, app, "Atlas")
	target, to := joinRequestAgent(t, app, "Claude")
	r, err := app.createAgentRequest(from, to, "private-parent", "Atlas asks Claude to read the draft", "the approved paragraph", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	markUnconfirmed := func(reserve bool) {
		t.Helper()
		err := updateAgentRequest(r.ID, time.Now(), func(saved *AgentRequest) error {
			if reserve {
				saved.Answer, saved.AnsweredAt = agentRequestYes, time.Now().UnixMilli()
				saved.TargetHandoffID = workOnItID()
			}
			saved.SendError = agentRequestUnconfirmed
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	markUnconfirmed(true)
	if items := app.workRequestItems(time.Now()); len(items) != 1 || items[0].Request.SendError != agentRequestUnavailable {
		t.Fatalf("interrupted before inbox: %+v", items)
	}
	if err := app.SendAgentRequestAgain(r.ID); err != nil {
		t.Fatal(err)
	}
	items := app.agentInboxService().ForAgent(to, false)
	if len(items) != 1 || items[0].Text != r.Moves || items[0].SourceID != r.ID {
		t.Fatalf("retried inbox: %+v", items)
	}
	markUnconfirmed(false)
	checkDelivered := func() {
		t.Helper()
		if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errAgentRequestNotFailed) {
			t.Fatalf("retried delivered request: %v", err)
		}
		if work := app.workRequestItems(time.Now()); len(work) != 1 || work[0].Request.SendError != "" || work[0].State != workStateAnswered {
			t.Fatalf("delivered reservation: %+v", work)
		}
	}
	checkDelivered()
	if text, failed := mcpCall(t, target, "post_result", map[string]any{"id": items[0].ID, "summary": "Read."}); failed {
		t.Fatal(text)
	}
	checkDelivered()
	if text, failed := mcpCall(t, asker, "get_answer", map[string]any{"id": r.ID}); failed || !strings.Contains(text, "Read.") {
		t.Fatalf("retried result: %q", text)
	}
}
