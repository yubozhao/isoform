package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"wails-gui/internal/pii"
	"wails-gui/storage"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"
)

// An answer about the person's own day says where it came from (phone-chat
// v1.0.0 rules 4 and 5, phone-map B10). The context blocks print a ref for
// each source (memory snippets "ref=memory:<id>", people "Ref: person:<id>").
// After answering, the model calls CiteSources with the refs its answer leans
// on and the phrase in the answer for each. Every ref is read again through
// its own gate, so a forgotten note or a person Isoform may not read is never
// shown; the thread gets a sources line under the answer.

const (
	chatSourcesToolName = "CiteSources"
	chatSourcesLineTool = "main_chat_sources"

	chatSourcesMax     = 4
	chatSourceClaimMax = 300
)

// Source kinds. memory and person are the first two; the others are what the
// model read of the person's day. A phone build that predates a kind skips it.
const (
	chatSourceMemory   = "memory"
	chatSourcePerson   = "person"
	chatSourceActivity = "activity"
	chatSourceCall     = "call"
	chatSourceWatch    = "watch"
)

// Targets a source opens beyond the report targets: an Activity session, and
// the watch's conversations in Activity.
const (
	mainChatTargetActivity = "activity"
	mainChatTargetWatch    = "watch"
)

type chatSource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
	// At is when the source happened (unix ms), for the chip's time; zero
	// for a note or a person. Optional, so an older reader ignores it.
	At     int64          `json:"at,omitempty"`
	Target mainChatTarget `json:"target"`
}

type chatSourceClaim struct {
	Text   string `json:"text"`
	Source int    `json:"source"`
}

// chatSourcesPayload is the sources line's row (its ToolInput). Titles are
// display text, read again at the call; the row is never replayed into a
// prompt.
type chatSourcesPayload struct {
	Sources []chatSource      `json:"sources"`
	Claims  []chatSourceClaim `json:"claims,omitempty"`
}

type chatSourcesTool struct {
	app *App
	tag string
}

var _ tools.AuthorityDescriber = (*chatSourcesTool)(nil)

// mainChatSourcesTools hands CiteSources to a main-chat turn typed on the
// computer, where its line can be shown.
func (a *App) mainChatSourcesTools(tag string, origin promptOrigin) []tools.Tool {
	if !chatB10Origin(origin) || !a.isMainChatSession(context.Background(), tag) {
		return nil
	}
	return []tools.Tool{&chatSourcesTool{app: a, tag: tag}}
}

func (t *chatSourcesTool) Name() string { return chatSourcesToolName }

func (t *chatSourcesTool) Description() string {
	return "After answering a question about the user's own life, say which sources the answer read: the ref of each (memory:<id>, person:<id> or activity:<session id>, exactly as the context or a read printed it) and the exact phrase in your answer that comes from it. Call it once, after the answer, and then stop."
}

func (t *chatSourcesTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sources": map[string]any{
				"type":     "array",
				"maxItems": chatSourcesMax,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"ref":   map[string]any{"type": "string", "description": "The source's ref as printed: memory:<id>, person:<id>, or activity:<session id> for an Activity session, call or watch recording read with an activity tool or the activity CLI."},
						"claim": map[string]any{"type": "string", "description": "The exact phrase in your answer that comes from this source."},
					},
					"required": []any{"ref"},
				},
			},
		},
		"required": []any{"sources"},
	}
}

// NeedsPermission asks nothing: the line only reads what the turn read.
func (t *chatSourcesTool) NeedsPermission(map[string]any) (bool, tools.PermissionPrompt) {
	return false, tools.PermissionPrompt{}
}

func (t *chatSourcesTool) DescribeAuthority(map[string]any) authority.Action {
	return authority.Action{
		Capability: "chat.sources.cite",
		ToolName:   chatSourcesToolName,
		Operation:  "create",
		Effect:     authority.EffectLocalWrite,
		Resource:   "main_chat",
		Reversible: true,
	}
}

func (t *chatSourcesTool) Execute(ctx context.Context, call tools.ToolCall) (tools.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return tools.ToolResult{}, err
	}
	if t.app == nil || t.app.store == nil {
		return tools.ToolResult{Content: "Sources could not be shown. Say nothing more.", IsError: true}, nil
	}
	raw, _ := call.Input["sources"].([]any)
	payload, dropped := t.app.chatSourcesFromInput(ctx, raw)
	if len(payload.Sources) == 0 {
		return tools.ToolResult{Content: "None of those refs could be shown, so no sources line was added. Do not name sources in your answer that you did not read. Say nothing more."}, nil
	}
	id := "sources:" + t.tag + ":" + call.ID
	if call.ID == "" {
		id = "sources:" + uuid.NewString()
	}
	if row, err := t.app.store.LoadStreamItem(ctx, id); err == nil && row != nil {
		return tools.ToolResult{Content: "The sources line is already in the chat. Say nothing more."}, nil
	}
	blob, err := json.Marshal(payload)
	if err != nil || t.app.appendMainChatItem(ctx, t.tag, storage.StreamItemRow{
		ID: id, Kind: "system", ToolName: chatSourcesLineTool, ToolInput: string(blob),
		Content: "Sources for the answer above.",
	}, "sources") == "" {
		return tools.ToolResult{Content: "Sources could not be shown. Say nothing more.", IsError: true}, nil
	}
	t.app.mainChatRowsChanged(t.tag)
	note := "The sources line is under your answer. Say nothing more."
	if dropped > 0 {
		note = "The sources line is under your answer; some refs could not be shown and were left out. Say nothing more."
	}
	return tools.ToolResult{Content: note}, nil
}

// chatSourcesFromInput keeps the refs that resolve and may be shown, each
// once, with the claims that point at them. It returns how many were left out.
func (a *App) chatSourcesFromInput(ctx context.Context, raw []any) (chatSourcesPayload, int) {
	var payload chatSourcesPayload
	dropped := 0
	index := map[string]int{}
	for _, value := range raw {
		entry, _ := value.(map[string]any)
		ref, _ := entry["ref"].(string)
		claim, _ := entry["claim"].(string)
		ref = strings.TrimSpace(ref)
		position, seen := index[ref]
		if !seen {
			if len(payload.Sources) == chatSourcesMax {
				dropped++
				continue
			}
			source, ok := a.resolveChatSource(ctx, ref)
			if !ok {
				dropped++
				continue
			}
			position = len(payload.Sources)
			index[ref] = position
			payload.Sources = append(payload.Sources, source)
		}
		claim = strings.Join(strings.Fields(claim), " ")
		if claim != "" && utf8.RuneCountInString(claim) <= chatSourceClaimMax {
			payload.Claims = append(payload.Claims, chatSourceClaim{Text: claim, Source: position})
		}
	}
	return payload, dropped
}

// resolveChatSource reads a ref through its source's own gate.
func (a *App) resolveChatSource(ctx context.Context, ref string) (chatSource, bool) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return chatSource{}, false
	}
	kind, id, ok := strings.Cut(ref, ":")
	id = strings.TrimSpace(id)
	if !ok || id == "" || len(id) > 200 || strings.ContainsAny(id, "/\\") {
		return chatSource{}, false
	}
	switch kind {
	case "memory":
		policy, err := a.mcpReadPrivacy(ctx)
		if err != nil {
			return chatSource{}, false
		}
		entry, err := a.filteredExternalMemory(id, policy.chatMemoryAllowed)
		if err != nil || entry.ID != id {
			return chatSource{}, false
		}
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			return chatSource{}, false
		}
		return chatSource{Kind: "memory", ID: id, Title: title, Target: mainChatTarget{Kind: "memory", ID: id}}, true
	case "person":
		if a.store == nil {
			return chatSource{}, false
		}
		person, err := a.store.GetPrincipal(ctx, id)
		if err != nil || person.ID != id || !a.peopleProfileAllowsModelRead(ctx, person, nil) {
			return chatSource{}, false
		}
		name := strings.TrimSpace(pii.DisplayText(person.DisplayName))
		if name == "" {
			return chatSource{}, false
		}
		return chatSource{Kind: "person", ID: id, Title: name, Target: mainChatTarget{Kind: "person", ID: id}}, true
	case chatSourceActivity:
		return a.resolveActivityChatSource(ctx, id)
	}
	return chatSource{}, false
}

// resolveActivityChatSource reads an Activity session through the gate every
// model read of a whole session goes through, the activity CLI's
// (activitySessionForExternalRead): Closed Eyes, protected rows, sensitive
// and excluded apps, private windows, people Isoform may not read. A call is
// named by its meeting app, a watch recording by the conversation the
// computer named, any other session by its first app that is not sensitive.
// A screen session in a mail or messaging app is never cited: mail stays out
// of chat (the app check RecentActivity uses, without its per-event identity
// rule, which no watch recording's events can meet).
func (a *App) resolveActivityChatSource(ctx context.Context, id string) (chatSource, bool) {
	if a.activity == nil || a.activity.store == nil {
		return chatSource{}, false
	}
	store := a.activity.store
	session, err := store.GetSessionByID(id)
	if err != nil || session == nil || session.ID != id || !a.activitySessionAllowsModelRead(store, *session) {
		return chatSource{}, false
	}
	policy, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return chatSource{}, false
	}
	if allowed, err := policy.chatSessionAllowed(*session); err != nil || !allowed {
		return chatSource{}, false
	}
	if conv, ok := a.watchConversationOfSession(id); ok {
		return chatSource{Kind: chatSourceWatch, ID: conv.ID, Title: conv.Name, At: conv.StartAt,
			Target: mainChatTarget{Kind: mainChatTargetWatch, ID: conv.ID}}, true
	}
	target := mainChatTarget{Kind: mainChatTargetActivity, ID: id}
	if session.IsMeeting {
		title := strings.TrimSpace(redactActivityModelText(pii.DisplayText(session.MeetingApp)))
		if title == "" || len(pii.FilterSensitiveAppNames([]string{session.MeetingApp})) == 0 {
			return chatSource{}, false
		}
		return chatSource{Kind: chatSourceCall, ID: id, Title: title, At: session.StartTime, Target: target}, true
	}
	var apps []string
	if err := json.Unmarshal([]byte(scrubSensitiveAppNames(session.AppNames)), &apps); err != nil {
		return chatSource{}, false
	}
	for _, app := range apps {
		if title := strings.TrimSpace(redactActivityModelText(app)); title != "" {
			return chatSource{Kind: chatSourceActivity, ID: id, Title: title, At: session.StartTime, Target: target}, true
		}
	}
	return chatSource{}, false
}

// watchConversationOfSession is the named watch conversation a recording's
// session holds: the one with the most words when the hour holds several.
// A recording that is not transcribed, deleted, or past the days the watch
// lists has none, and neither has a conversation not yet named.
func (a *App) watchConversationOfSession(sessionID string) (WatchConversation, bool) {
	store := a.watchRecordingStore()
	if store == nil {
		return WatchConversation{}, false
	}
	recordings, err := store.all()
	if err != nil {
		return WatchConversation{}, false
	}
	recordingID := ""
	for _, rec := range recordings {
		if rec.SessionID == sessionID && rec.State == mobileRecordingTranscribed {
			recordingID = rec.ID
			break
		}
	}
	if recordingID == "" {
		return WatchConversation{}, false
	}
	convs, err := a.watchConversationsOf(store)
	if err != nil {
		return WatchConversation{}, false
	}
	var best WatchConversation
	for _, conv := range convs {
		if containsString(conv.RecordingIDs, recordingID) && strings.TrimSpace(conv.Name) != "" && conv.Words > best.Words {
			best = conv
		}
	}
	best.Name = strings.TrimSpace(best.Name)
	return best, best.ID != ""
}

// memoryIDFromPath is a memory file's id: "memory-123.md" is "memory-123".
func memoryIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// injectChatSourcesContext asks the main chat's model to say where an answer
// about the person's own life came from.
func (a *App) injectChatSourcesContext(tag string, origin promptOrigin) string {
	if !chatB10Origin(origin) || !a.isMainChatSession(context.Background(), tag) {
		return ""
	}
	return `<sources>
When you answer a question about the user's own life from what Isoform saw, write your answer first, then call CiteSources once with one ref per thing the answer read (at most 4) and the exact phrase in your answer that comes from each, then stop. Refs: a note or a person in the context (ref=memory:memory-123, Ref: person:p_1); an Activity session, call or watch recording you read with an activity tool (it prints ref=activity:<id>) or the activity CLI (activity:<the session's id>). Cite only refs printed in this conversation. When you say you did not see something ("I didn't see anything about hiring from Priya this week"), say what you did see instead and cite that. Skip it for small talk and for answers that read no source.
</sources>

`
}
