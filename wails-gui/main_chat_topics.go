package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	persistencesvc "wails-gui/internal/persistence"
	"wails-gui/internal/topicid"
)

// TopicSource is the registry seam. Implementations return current metadata,
// with every observed field covered by Refs. Refs must name original sources,
// never topic/brief wrappers. Missing registry or provenance fails closed.
// The registry adapter must synchronize its snapshot with admission publication.
// The RC 1 registry installs a source-bound adapter in NewApp.
type TopicSource interface {
	TopicByID(ctx context.Context, id string) (ChatTopic, error)
	TopicKeys(ctx context.Context, id string) (ChatTopicKeys, error)
}

type ChatTopic struct {
	// SourceFingerprint binds every observed field to the exact source state
	// used to derive it (chatTopicSourceFingerprint), including corrections.
	SourceFingerprint string                `json:"sourceFingerprint"`
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Status            string                `json:"status"`
	Latest            string                `json:"latest"`
	Next              string                `json:"next"`
	Refs              []TodayChatContextRef `json:"refs"`
}

type ChatTopicKeys struct {
	// ThreadKeys are canonical threadSureKeys or activity_threads.purpose_key.
	ThreadKeys []string `json:"threadKeys"`
	GoalIDs    []string `json:"goalIds"`
	InterestID string   `json:"interestId,omitempty"`
}

type MainChatTopicSendResult struct {
	SessionID  string `json:"sessionId"`
	UserItemID string `json:"userItemId"`
	SortOrder  int    `json:"sortOrder"`
}

// MainChatTopicItem has kind message or event. Message is exactly the ordinary
// renderer projection; event actor is a stable role code, and text is source
// data. At is Unix milliseconds. No localized presentation copy is generated.
type MainChatTopicItem struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	At      int64           `json:"at"`
	Actor   string          `json:"actor,omitempty"`
	Text    string          `json:"text,omitempty"`
	Message *StreamItemData `json:"message,omitempty"`
}

type MainChatTopicPage struct {
	SessionID  string              `json:"sessionId"`
	TopicID    string              `json:"topicId"`
	Items      []MainChatTopicItem `json:"items"`
	HasMore    bool                `json:"hasMore"`
	NextCursor string              `json:"nextCursor"`
}

type topicTurnContextKey struct{}
type topicTurnBinding struct {
	correctionRef        *TodayChatContextRef
	userItemID, metadata string
	history              topicChatHistoryCache
	pausedAt             atomic.Int64
}

func topicTurnFromContext(ctx context.Context) *topicTurnBinding {
	binding, _ := ctx.Value(topicTurnContextKey{}).(*topicTurnBinding)
	return binding
}
func todayChatTopicID(ref TodayChatContextRef) string {
	if ref.SourceKind == "topic" {
		return ref.SourceID
	}
	return ""
}

// SendMainChatWithTopic sends words to the existing main chat, with a topic
// source revalidated at admission. It never creates or selects a session.
func (a *App) SendMainChatWithTopic(text string, topicId string) (MainChatTopicSendResult, error) {
	return a.sendMainChatWithTopic(text, topicId, promptOriginMac, nil)
}

// The phone shares topic admission while retaining its executor origin and
// registering the turn before streaming events can arrive.
func (a *App) sendMainChatWithTopic(text, topicId string, origin promptOrigin, onAdmitted func(string, int)) (MainChatTopicSendResult, error) {
	if !validChatTopicID(topicId) {
		return MainChatTopicSendResult{}, errTodayChatItemUnavailable
	}
	return a.sendMainChatWithRef(text, TodayChatContextRef{SourceKind: "topic", SourceID: topicId}, origin, onAdmitted)
}

// SendMainChatWithContext sends words to the existing main chat with a Today
// item attached (a brief card, a story, a paused draft), the way the main
// chat's own box sends a prepared request: the item is read and checked
// again at admission. Today's panel uses it for an answer prepared from the
// brief. It never creates or selects a session.
func (a *App) SendMainChatWithContext(text string, ref TodayChatContextRef) (MainChatTopicSendResult, error) {
	return a.sendMainChatWithContext(text, ref, promptOriginMac)
}

// AskMainChatWithContext checks the source and answers without tool authority.
func (a *App) AskMainChatWithContext(text string, ref TodayChatContextRef) (MainChatTopicSendResult, error) {
	return a.sendMainChatWithContext(text, ref, promptOriginPanelAsk)
}

func (a *App) sendMainChatWithContext(text string, ref TodayChatContextRef, origin promptOrigin) (MainChatTopicSendResult, error) {
	if ref.SourceKind == "topic" {
		return a.sendMainChatWithTopic(text, ref.SourceID, origin, nil)
	}
	return a.sendMainChatWithRef(text, ref, origin, nil)
}

func (a *App) sendMainChatWithRef(text string, ref TodayChatContextRef, origin promptOrigin, onAdmitted func(string, int)) (MainChatTopicSendResult, error) {
	var result MainChatTopicSendResult
	if a == nil || a.store == nil || strings.TrimSpace(text) == "" || len(text) > 64<<10 || strings.TrimSpace(ref.SourceKind) == "" {
		return result, errTodayChatItemUnavailable
	}
	ctx := context.Background()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil {
		return result, err
	}
	if main == nil {
		return result, errTodayChatItemUnavailable
	}
	model := main.Model
	if model == "" {
		model = a.chatTaskModel()
	}
	id := uuid.NewString()
	chat, exists, err := a.store.MobileChat(ctx, main.LocalID)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, errTodayChatItemUnavailable
	}
	workDir, err := mobileChatWorkDir(chat)
	if err != nil {
		return result, err
	}
	admitted := func(order int) {
		a.scheduleOpenAnswerAskPromotion(main.LocalID, id, text)
		if onAdmitted != nil {
			onAdmitted(main.LocalID, order)
		}
	}
	order, err := a.sendPromptWithTodayContext(text, workDir, model, main.SessionID, main.LocalID, nil, nil,
		main.PlanMode, main.ThinkingEnabled, "", "", StreamItemData{ID: id, Kind: "user"}, ref, origin, admitted)
	if err != nil {
		return result, err
	}
	return MainChatTopicSendResult{SessionID: main.LocalID, UserItemID: id, SortOrder: order}, nil
}

func validChatTopicID(id string) bool { return topicid.Valid(id) }

// chatTopicSnapshot validates the registry synthesis against its original
// inputs; publication barriers and revisions then keep that snapshot stable.
func (a *App) chatTopicSnapshot(ctx context.Context, id string) (ChatTopic, ChatTopicKeys, error) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return ChatTopic{}, ChatTopicKeys{}, errTodayChatObservationPaused
	}
	sources := a.collectTodayChatSources(ctx, time.Now())
	return a.chatTopicSnapshotFromSources(ctx, id, sources)
}

func (a *App) chatTopicSnapshotFromSources(ctx context.Context, id string, sources todayChatSources) (ChatTopic, ChatTopicKeys, error) {
	if a == nil || a.topicSource == nil || !validChatTopicID(id) {
		return ChatTopic{}, ChatTopicKeys{}, errTodayChatItemUnavailable
	}
	if a.IsClosedEyesModeEnabled() && !pausedTopicReadAllowed(ctx) {
		return ChatTopic{}, ChatTopicKeys{}, errTodayChatObservationPaused
	}
	var topic ChatTopic
	var keys ChatTopicKeys
	var err error
	if combined, ok := a.topicSource.(interface {
		TopicSnapshot(context.Context, string) (ChatTopic, ChatTopicKeys, error)
	}); ok {
		topic, keys, err = combined.TopicSnapshot(ctx, id)
	} else {
		topic, err = a.topicSource.TopicByID(ctx, id)
		if err == nil {
			keys, err = a.topicSource.TopicKeys(ctx, id)
		}
	}
	if err != nil {
		return topic, keys, err
	}
	if topic.ID != id || len(topic.Refs) == 0 || len(topic.Refs) > 64 {
		return ChatTopic{}, ChatTopicKeys{}, errTodayChatItemUnavailable
	}
	resolved := sources.topicResolved
	if resolved == nil {
		resolved = map[string]topicChatFingerprintInput{}
	}
	fingerprint, err := a.chatTopicFingerprintFromSources(ctx, topic.Refs, sources, resolved)
	if err != nil {
		return ChatTopic{}, keys, err
	}
	if fingerprint != topic.SourceFingerprint {
		return ChatTopic{}, keys, errTodayChatItemUnavailable
	}
	return topic, keys, ctx.Err()
}

func (a *App) resolveTodayChatTopic(ctx context.Context, id string, sources todayChatSources) (todayChatItem, error) {
	topic, keys, err := a.chatTopicSnapshotFromSources(ctx, id, sources)
	if err != nil {
		return todayChatItem{}, err
	}

	events, err := a.chatTopicEvents(ctx, keys, topicItemCursor{At: math.MaxInt64, ID: "~"}, 12)
	if err != nil {
		return todayChatItem{}, err
	}
	titles, err := a.chatTopicRecentThreadTitles(ctx, events)
	if err != nil {
		return todayChatItem{}, err
	}
	payload, err := json.Marshal(struct {
		Name               string   `json:"name"`
		Status             string   `json:"status"`
		Latest             string   `json:"latest"`
		Next               string   `json:"next"`
		RecentThreadTitles []string `json:"recentThreadTitles"`
	}{topic.Name, topic.Status, topic.Latest, topic.Next, titles})
	if err != nil {
		return todayChatItem{}, err
	}
	refs, err := a.chatTopicEventRefs(ctx, events)
	if err != nil {
		return todayChatItem{}, err
	}
	refs = append(append([]TodayChatContextRef{}, topic.Refs...), refs...)
	return todayChatItem{Record: todayChatRecord{Kind: "topic", ID: id, Text: string(payload)}, Label: topic.Name, topicRefs: refs}, nil
}

type topicChatFingerprintInput struct {
	Item           todayChatItem
	Facts          []todayChatFact
	SourceIdentity string
}

// chatTopicSourceFingerprint is captured by the registry adapter when deriving
// metadata, never backfilled at read time onto an already-written synthesis.
func (a *App) chatTopicSourceFingerprint(ctx context.Context, refs []TodayChatContextRef) (string, error) {
	if a.IsClosedEyesModeEnabled() {
		return "", errTodayChatObservationPaused
	}
	sources := a.collectTodayChatSources(ctx, time.Now())
	return a.chatTopicFingerprintFromSources(ctx, refs, sources, map[string]topicChatFingerprintInput{})
}
func (a *App) chatTopicFingerprintInputsFromSources(ctx context.Context, refs []TodayChatContextRef, sources todayChatSources, resolved map[string]topicChatFingerprintInput) ([]topicChatFingerprintInput, error) {
	if len(refs) == 0 || len(refs) > 256 {
		return nil, errTodayChatItemUnavailable
	}
	inputs := []topicChatFingerprintInput{}
	for _, ref := range refs {
		if !todayChatRefWithinBounds(ref) {
			return nil, errTodayChatItemUnavailable
		}
		switch ref.SourceKind {
		case "memory", "knowledge", "activity", "principal", "purpose", "find", "calendar", "interest":
		default:
			return nil, errTodayChatItemUnavailable
		}
		key := ref.SourceKind + ":" + ref.SourceID
		input, ok := resolved[key]
		if !ok {
			a.addTodayChatActivityByID(ctx, &sources, ref)
			item, err := a.resolveTodayChatItem(ctx, ref, sources, sources.Now)
			if err != nil {
				return nil, err
			}
			input = topicChatFingerprintInput{Item: item, Facts: item.Record.Facts}
			if ref.SourceKind == "activity" && !strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) && a.activity != nil && a.activity.store != nil {
				row, err := a.activity.store.GetActivityByID(ref.SourceID)
				if err != nil || row == nil {
					return nil, errTodayChatItemUnavailable
				}
				input.SourceIdentity = proactiveVoiceHash(*row)
			}
			if ref.SourceKind == "purpose" {
				for _, detail := range sources.Purposes {
					if detail.OpenLoop.ID == ref.SourceID {
						input.SourceIdentity = proactiveVoiceHash(detail)
						break
					}
				}
			}
			if ref.SourceKind == "find" {
				state := a.topicsSvc().store.load()
				for _, find := range state.Finds.Items {
					if find.ID == ref.SourceID {
						input.SourceIdentity = proactiveVoiceHash(struct {
							Find TopicFind
							Sent []topicSent
						}{find, state.Finds.Sent})
						break
					}
				}
			}
			resolved[key] = input
		}
		inputs = append(inputs, input)
	}
	return inputs, ctx.Err()
}

// chatTopicFingerprintFromSources is the current form: a note's words with
// whitespace normalized and no file time, so a blank line or a restore of the
// same words keeps or returns a proof (see topicChatProofForm).
func (a *App) chatTopicFingerprintFromSources(ctx context.Context, refs []TodayChatContextRef, sources todayChatSources, resolved map[string]topicChatFingerprintInput) (string, error) {
	inputs, err := a.chatTopicFingerprintInputsFromSources(ctx, refs, sources, resolved)
	if err != nil {
		return "", err
	}
	return chatTopicFingerprintHash(topicChatNormalizedInputs(inputs))
}

// chatTopicLegacyFingerprintFromSources is the form proofs stored before the
// change: the memory record's file time is part of it, so it matches only the
// exact file the proof was made from.
func (a *App) chatTopicLegacyFingerprintFromSources(ctx context.Context, refs []TodayChatContextRef, sources todayChatSources, resolved map[string]topicChatFingerprintInput) (string, error) {
	inputs, err := a.chatTopicFingerprintInputsFromSources(ctx, refs, sources, resolved)
	if err != nil {
		return "", err
	}
	return chatTopicFingerprintHash(inputs)
}

func chatTopicFingerprintHash(inputs []topicChatFingerprintInput) (string, error) {
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// topicChatNormalizedInputs keeps what a memory note says and drops how the
// file was saved: the file time goes, and whitespace runs collapse to one space.
func topicChatNormalizedInputs(inputs []topicChatFingerprintInput) []topicChatFingerprintInput {
	out := make([]topicChatFingerprintInput, len(inputs))
	for i, input := range inputs {
		if input.Item.Record.Kind == "memory" {
			input.Item.Record.At = 0
			input.Item.Record.Text = strings.Join(strings.Fields(input.Item.Record.Text), " ")
			// The prefill's occurrence time is the note's file time.
			input.Item.Prefill.OccurredAt = 0
		}
		out[i] = input
	}
	return out
}

type topicItemCursor struct {
	TopicID string `json:"topicId"`
	At      int64  `json:"at"`
	ID      string `json:"id"`
}

var errTopicCursor = errors.New("TOPIC_CURSOR_INVALID")

// ListMainChatTopicItems returns the latest page or rows strictly before an
// opaque cursor. Each page is oldest first; prepend older pages. Privacy is
// rechecked on every call, so withdrawal also applies between pages.
func (a *App) ListMainChatTopicItems(topicId string, cursor string, limit int) (pageResult MainChatTopicPage, pageErr error) {
	defer func() { pageResult = displayModelCopy(pageResult, a.modelDisplayLanguage()) }()
	page := MainChatTopicPage{TopicID: topicId, Items: []MainChatTopicItem{}}
	if a == nil || a.store == nil || !validChatTopicID(topicId) {
		return page, errTodayChatItemUnavailable
	}
	before := topicItemCursor{TopicID: topicId, At: math.MaxInt64, ID: "~"}
	if len(cursor) > 6000 {
		return page, errTopicCursor
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &before) != nil || before.TopicID != topicId || !validTopicCursorID(before.ID) || before.At < 0 {
			return page, errTopicCursor
		}
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.retryWorkPermissionWrites()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return page, err
	}
	page.SessionID = main.LocalID
	rows, err := a.store.LoadTopicStreamItems(ctx, main.LocalID, topicId, before.At, before.ID, limit+1)
	if err != nil {
		return page, err
	}
	// Compute all renderer messages together, including one batch tag read.
	revision, err := a.captureTopicPageRevision(ctx)
	if err != nil {
		return MainChatTopicPage{}, err
	}
	messages := make([]StreamItemData, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, persistencesvc.ToStreamItemData(row))
	}
	a.projectStreamItems(messages)
	viewRevision, err := a.computeMainChatView(ctx, main.LocalID, messages, time.Now())
	if err != nil {
		return MainChatTopicPage{}, err
	}
	if viewRevision != nil && !reflect.DeepEqual(*viewRevision, revision) {
		return MainChatTopicPage{}, errTopicRegistryChanged
	}
	for i, row := range rows {
		// A derived association cannot reveal a hidden source in a topic view.
		if row.Kind == "system" && !topicStringIn(messages[i].TopicIDs, topicId) {
			continue
		}
		page.Items = append(page.Items, MainChatTopicItem{ID: "message:" + row.ID, Kind: "message", At: row.CreatedAt, Message: &messages[i]})
	}
	var scan topicEventScan
	if revision.sources.closedEyes {
		// The desktop can keep an already displayed topic open while paused,
		// but must withdraw it if any original source no longer supports the
		// frozen synthesis. The phone rejects paused topic reads before here.
		if _, ok := a.topicSource.(*registryTopicSource); ok {
			ref := TodayChatContextRef{SourceKind: "topic", SourceID: topicId}
			files, err := a.readTodayChatAdmissionFiles(ctx, ref)
			if err != nil {
				return MainChatTopicPage{}, err
			}
			proofCtx := context.WithValue(ctx, pausedTopicReadKey{}, true)
			if _, _, err := a.loadTodayChatContextItem(proofCtx, ref); err != nil {
				return MainChatTopicPage{}, err
			}
			release, err := a.validateTopicPageRevision(ctx, revision)
			if err != nil {
				return MainChatTopicPage{}, err
			}
			defer release()
			if err := a.validateTodayChatAdmissionFiles(ctx, ref, files); err != nil {
				return MainChatTopicPage{}, err
			}
		}
	} else {
		compute, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		sources, err := a.topicPageSourcesForRevision(compute, revision.sources)
		if err != nil {
			return MainChatTopicPage{}, err
		}
		_, keys, err := a.chatTopicSnapshotFromSources(compute, topicId, sources)
		if err != nil {
			return MainChatTopicPage{}, err
		}
		scan, err = a.scanChatTopicEvents(compute, keys, before, limit+1)
		if err != nil {
			return MainChatTopicPage{}, err
		}
		if a.topicPageBeforePublish != nil {
			a.topicPageBeforePublish()
		}
		release, err := a.validateTopicPageRevision(ctx, revision)
		if err != nil {
			return MainChatTopicPage{}, err
		}
		defer release() // Retain fences through the final bounded response assembly.
		if a.topicPageAfterValidate != nil {
			a.topicPageAfterValidate()
		}
		page.Items = append(page.Items, scan.items...)
	}
	// Both scans may pass hidden rows. Advance through the scanned message
	// range too, retaining events/messages beyond neither unfinished scan.
	messageMore := len(rows) > limit
	boundary := scan.before
	bounded := scan.more
	if messageMore {
		last := rows[len(rows)-1]
		messageBoundary := topicItemCursor{TopicID: topicId, At: last.CreatedAt, ID: "message:" + last.ID}
		if !bounded || messageBoundary.At > boundary.At || (messageBoundary.At == boundary.At && messageBoundary.ID > boundary.ID) {
			boundary = messageBoundary
		}
		bounded = true
	}
	if bounded {
		kept := page.Items[:0]
		for _, item := range page.Items {
			if item.At > boundary.At || (item.At == boundary.At && item.ID >= boundary.ID) {
				kept = append(kept, item)
			}
		}
		page.Items = kept
	}
	sort.Slice(page.Items, func(i, j int) bool {
		if page.Items[i].At != page.Items[j].At {
			return page.Items[i].At > page.Items[j].At
		}
		return page.Items[i].ID > page.Items[j].ID
	})
	truncated := len(page.Items) > limit
	page.HasMore = truncated || bounded
	if truncated {
		page.Items = page.Items[:limit]
	}
	next := boundary
	next.TopicID = topicId
	if truncated || (!bounded && len(page.Items) > 0) {
		last := page.Items[len(page.Items)-1]
		next = topicItemCursor{TopicID: topicId, At: last.At, ID: last.ID}
	}
	if page.HasMore || len(page.Items) > 0 {
		raw, _ := json.Marshal(next)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, ctx.Err()
}

func validTopicCursorID(id string) bool {
	return (strings.HasPrefix(id, "message:") && len(id) > len("message:")) || (strings.HasPrefix(id, "event:") && len(id) > len("event:"))
}
