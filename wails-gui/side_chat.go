package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// Side chats split a topic off the main chat. The renderer creates the side
// session (kind "side", parent_id = the main chat) and, when it branches from
// a message, stores that message as the side chat's opening `system` item
// with ToolName sideChatOriginTool. The first turn of a side chat gets a
// context block with the main chat's recent turns and that message;
// "Bring back to the main chat" posts a one-line summary into the main chat.

// sideChatOriginTool marks the opening item that quotes the branched message.
const sideChatOriginTool = "side_chat_origin"

const (
	// sideChatContextTurns is how many recent main-chat user/assistant rows
	// are quoted in the side chat's first turn.
	sideChatContextTurns = 8
	// sideChatContextItemMax caps each quoted row.
	sideChatContextItemMax = 1200
	// sideChatSummaryMax caps the bring-back line.
	sideChatSummaryMax = 240
	// sideChatSummaryTimeout bounds the model call that writes the summary.
	sideChatSummaryTimeout = 20 * time.Second
)

// sideChatOrigin is the ToolInput of the opening item.
type sideChatOrigin struct {
	ParentID string `json:"parentId"`
	ItemID   string `json:"itemId"`
}

// loadSideSession returns the session when it is a side chat with a parent.
func (a *App) loadSideSession(ctx context.Context, tag string) (*storage.SessionRow, bool) {
	if a == nil || a.store == nil || strings.TrimSpace(tag) == "" {
		return nil, false
	}
	row, err := a.store.LoadSession(ctx, tag)
	if err != nil || row == nil || row.Kind != storage.SessionKindSide || row.ParentID == "" {
		return nil, false
	}
	return row, true
}

// sideChatOriginExcerpt returns the branched message quoted in the side chat's
// opening item, or "" when the side chat has none.
func (a *App) sideChatOriginExcerpt(ctx context.Context, sideID string) string {
	items, err := a.store.LoadStreamItems(ctx, sideID)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if item.Kind == "system" && item.ToolName == sideChatOriginTool {
			return strings.TrimSpace(item.Content)
		}
	}
	return ""
}

// sideChatHasReply reports whether the side chat already holds an assistant
// reply. Until it does, every attempt is the first turn, so a prompt that
// failed before any reply keeps its context on the retry.
func (a *App) sideChatHasReply(ctx context.Context, sideID string) bool {
	items, err := a.store.LoadStreamItems(ctx, sideID)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.Kind == "assistant" && strings.TrimSpace(item.Content) != "" {
			return true
		}
	}
	return false
}

// injectSideChatContext is the composePrompt producer for side chats. Only
// the first turn gets the block; later turns already carry it in history.
func (a *App) injectSideChatContext(tag string) string {
	ctx := context.Background()
	side, ok := a.loadSideSession(ctx, tag)
	if !ok {
		return ""
	}
	if a.sideChatHasReply(ctx, side.LocalID) {
		return ""
	}
	parent, err := a.store.LoadSession(ctx, side.ParentID)
	if err != nil || parent == nil {
		return ""
	}
	recent, _, err := a.sourceBoundChatHistory(ctx, side.ParentID)
	if err != nil {
		log.Printf("[side-chat] parent history for %s: %v", side.LocalID, err)
		return ""
	}
	recent = recentChatConversationRows(recent, sideChatContextTurns)
	excerpt := a.sideChatOriginExcerpt(ctx, side.LocalID)
	return buildSideChatContext(parent.Label, recent, excerpt)
}

// buildSideChatContext renders the block the model sees ahead of the user's
// first side-chat message.
func buildSideChatContext(parentLabel string, recent []storage.StreamItemRow, excerpt string) string {
	if len(recent) == 0 && excerpt == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<side-chat-context>\n")
	b.WriteString("This is a side chat split off from the user's main chat")
	if label := strings.TrimSpace(parentLabel); label != "" {
		fmt.Fprintf(&b, " (%q)", label)
	}
	b.WriteString(". Treat the main chat's recent turns below as background the user already shares with you; focus this side chat on the topic they bring up here.\n")
	if len(recent) > 0 {
		b.WriteString("\n<main-chat-recent-turns>\n")
		for _, row := range recent {
			role := "User"
			if row.Kind == "assistant" {
				role = "Isoform"
			}
			fmt.Fprintf(&b, "%s: %s\n\n", role, trimSideChatText(row.Content, sideChatContextItemMax))
		}
		b.WriteString("</main-chat-recent-turns>\n")
	}
	if excerpt != "" {
		b.WriteString("\n<branched-from-message>\n")
		b.WriteString(trimSideChatText(excerpt, sideChatContextItemMax*2))
		b.WriteString("\n</branched-from-message>\n")
	}
	b.WriteString("</side-chat-context>\n\n")
	return b.String()
}

// trimSideChatText collapses whitespace and cuts long rows in the middle so
// both the opening and the conclusion survive.
func trimSideChatText(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	return string(runes[:head]) + " [...] " + string(runes[len(runes)-tail:])
}

// errNotSideChat is returned when bring-back is asked of a non-side session.
var errNotSideChat = errors.New("this chat is not a side chat of Chat")

// BringBackToMainChat posts a one-line summary of the side chat into the main
// chat with a link back to the side chat. It returns the summary line. The
// side chat itself is left untouched and stays in Recent as history.
func (a *App) BringBackToMainChat(sideSessionID string) (string, error) {
	if err := chatFeatureErr(); err != nil {
		return "", err
	}
	if a == nil || a.store == nil {
		return "", fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	side, ok := a.loadSideSession(ctx, sideSessionID)
	if !ok {
		return "", errNotSideChat
	}
	history, bound, err := a.sourceBoundChatHistory(ctx, side.LocalID)
	if err != nil {
		return "", fmt.Errorf("load side chat: %w", err)
	}
	// A plain main-chat report cannot retain the side chat's source proof.
	// Keep source-bound answers in their guarded conversation.
	if bound {
		return "", errChatSourceAccessChanged
	}
	history = recentChatConversationRows(history, sideChatContextTurns)
	summary := a.summarizeSideChat(side.Label, history)
	if summary == "" {
		return "", errors.New("the side chat has nothing to bring back yet")
	}
	label := strings.TrimSpace(side.Label)
	if label == "" {
		label = "Side chat"
	}
	text := fmt.Sprintf("Brought back from the side chat %q: %s", label, summary)
	// Every bring-back is a new moment, so the key is unique rather than
	// derived from the side chat.
	a.postToMainChat(text, mainChatReport{
		Source:  mainChatSourceSideChat,
		Key:     mainChatReportKey(mainChatSourceSideChat, side.LocalID, uuid.NewString()),
		Action:  mainChatActionOpen,
		Target:  mainChatTarget{Kind: mainChatTargetSession, ID: side.LocalID},
		Title:   label,
		Status:  "brought_back",
		Summary: summary,
	})
	return summary, nil
}

// summarizeSideChat asks the light model for one sentence and falls back to
// the side chat's last reply when the model is unavailable.
func (a *App) summarizeSideChat(label string, history []storage.StreamItemRow) string {
	if a.IsClosedEyesModeEnabled() {
		return ""
	}
	fallback := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == "assistant" && strings.TrimSpace(history[i].Content) != "" {
			fallback = firstSentence(pii.DisplayModelText(history[i].Content, a.modelDisplayLanguage()), sideChatSummaryMax)
			break
		}
	}
	if len(history) == 0 {
		return ""
	}
	var transcript strings.Builder
	for _, row := range history {
		role := "User"
		if row.Kind == "assistant" {
			role = "Assistant"
		}
		fmt.Fprintf(&transcript, "%s: %s\n\n", role, trimSideChatText(row.Content, sideChatContextItemMax))
	}
	prompt := pii.ModelOutputInstructions + fmt.Sprintf(`Summarize the outcome of this side conversation titled %q in ONE plain sentence (at most 30 words) the user can read in their main chat as a reminder of what was decided or produced. Reply with the sentence only, no preamble, no quotes.

%s`, strings.TrimSpace(label), transcript.String())
	out, err := a.runEphemeralHaiku(prompt, sideChatSummaryTimeout)
	if err != nil {
		log.Printf("[side-chat] summary model unavailable, using the last reply: %v", err)
		return fallback
	}
	out = strings.TrimSpace(strings.Trim(strings.TrimSpace(out), "\"'"))
	if out == "" {
		return fallback
	}
	return firstSentence(pii.DisplayModelText(out, a.modelDisplayLanguage()), sideChatSummaryMax)
}

// firstSentence returns the first line of s, cut at max runes.
func firstSentence(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	runes := []rune(s)
	if len(runes) > max {
		return strings.TrimSpace(string(runes[:max-1])) + "…"
	}
	return s
}
