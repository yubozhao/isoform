package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// Ideas project existing suggestions and finds. They never approve a schedule
// or start work: the phone's Do it action only prepares an unsent chat draft.
type mobileOffer struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Why          string            `json:"why"`
	Prompt       string            `json:"prompt"`
	Version      string            `json:"version"`
	WhyLocalized map[string]string `json:"whyLocalized,omitempty"`
	// Evidence is the fact Isoform saw, with where and when (Phone Threads
	// §2). An offer without one is dropped before it reaches the phone.
	Evidence *mobileOfferEvidence `json:"evidence,omitempty"`
	// DeckKind labels the card: reply, your_day or claude_code; empty when
	// the offer is none of those.
	DeckKind string                     `json:"deckKind,omitempty"`
	Hold     *mobileHoldProposal        `json:"hold,omitempty"`
	Sources  *mobileOfferSourceSnapshot `json:"-"`
}

// mobileOfferEvidence is one or two sentences of what Isoform saw, the source
// it saw it in (mail, calendar, claude_code, codex, call or screen) and when.
type mobileOfferEvidence struct {
	Text          string            `json:"text"`
	TextLocalized map[string]string `json:"textLocalized,omitempty"`
	Source        string            `json:"source"`
	At            string            `json:"at"`
}

// mobileOfferSourceName maps an observation's source kind onto the names the
// phone words ("From Mail", "From your screen").
func mobileOfferSourceName(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "mail", "email", "gmail":
		return "mail"
	case "calendar":
		return "calendar"
	case "claude", "claude_code", "claude-code":
		return "claude_code"
	case "codex":
		return "codex"
	case "meeting", "call", "zoom":
		return "call"
	}
	return "screen"
}

// mobileOfferDeckKind labels a card by where its fact came from.
func mobileOfferDeckKind(source string) string {
	switch source {
	case "mail":
		return "reply"
	case "calendar":
		return "your_day"
	case "claude_code", "codex":
		return "claude_code"
	}
	return ""
}

// mobileSuggestionEvidence states the pattern Isoform saw and when it last saw
// it. Without a dated observation there is no fact, and no card.
func mobileSuggestionEvidence(times, days int, observations []InitiativeObservation) *mobileOfferEvidence {
	var latest InitiativeObservation
	for _, observation := range observations {
		if observation.ObservedAt > latest.ObservedAt {
			latest = observation
		}
	}
	if latest.ObservedAt <= 0 || times <= 0 || days <= 0 {
		return nil
	}
	en := fmt.Sprintf("You did this %d times across %d days.", times, days)
	if times == 1 {
		en = "You did this once."
	}
	zh := fmt.Sprintf("你在 %d 天里做了 %d 次。", days, times)
	if times == 1 {
		zh = "你做过一次。"
	}
	return &mobileOfferEvidence{
		Text: en, TextLocalized: map[string]string{"en": en, "zh-CN": zh},
		Source: mobileOfferSourceName(latest.SourceKind),
		At:     time.UnixMilli(latest.ObservedAt).UTC().Format(time.RFC3339),
	}
}

// This reference survives edits and queuing on the phone. It identifies the
// original evidence association; editing a draft does not make it user-sourced.
type mobileOfferReference struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// The admitted source graph outlives the waiting offer. It contains source identities and
// a digest of their original provenance, never the captured prose or search queries.
type mobileOfferSourceReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type mobileOfferSourceSnapshot struct {
	Reference mobileOfferReference         `json:"reference"`
	Sources   []mobileOfferSourceReference `json:"sources"`
	Resolved  string                       `json:"resolved"`
	Find      *mobileOfferFindSources      `json:"find,omitempty"`
}

type mobileOfferFindSources struct {
	RunID   string      `json:"runId"`
	TopicID string      `json:"topicId"`
	URLHash string      `json:"urlHash"`
	Sent    []topicSent `json:"sent"`
}

type mobileOfferChatSources struct {
	OfferReferences []mobileOfferReference      `json:"offerReferences"`
	OfferSources    []mobileOfferSourceSnapshot `json:"offerSources"`
}

const mobileOfferReferencesMax = 16

var errMobileOfferUnavailable = errors.New("this idea changed or is no longer available; refresh Ideas")

func mobileOfferVersion(id, prompt string, provenance any) string {
	raw, _ := json.Marshal(struct {
		Domain     string
		ID         string
		Prompt     string
		Provenance any
	}{"mobile-offer-v1", id, redactActivityModelText(prompt), provenance})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func mobileOfferReader(p *mcpReadPrivacy) *mcpBriefReader {
	// Each digest covers this offer's transitive sources, not the sources of
	// whichever unrelated card happened to be resolved before it.
	privacy := *p
	privacy.sessions = map[string]bool{}
	return &mcpBriefReader{p: &privacy, cache: map[string]mcpBriefCachedRead{}, checked: map[string]bool{}}
}

func mobileOfferResolvedSources(reader *mcpBriefReader) (string, error) {
	sources := map[string]activitySourceProvenance{}
	for id, allowed := range reader.p.sessions {
		if !allowed {
			continue
		}
		row, err := reader.p.app.activity.store.GetSessionByID(id)
		if err != nil || row == nil {
			return "", errMobileStoreUnavailable
		}
		source, err := reader.p.app.activity.store.activitySessionProvenance(*row)
		if err != nil {
			return "", errMobileStoreUnavailable
		}
		sources[id] = source
	}
	for id, allowed := range reader.p.memorySessions {
		if !allowed || reader.p.sessions[id] {
			continue
		}
		var raw string
		if err := reader.p.app.activity.store.db.QueryRowContext(reader.p.ctx, `SELECT provenance FROM activity_retained_source_privacy WHERE session_id=?`, id).Scan(&raw); err != nil {
			return "", errMobileStoreUnavailable
		}
		var source activitySourceProvenance
		if json.Unmarshal([]byte(raw), &source) != nil {
			return "", errMobileStoreUnavailable
		}
		sources[id] = source
	}
	references := map[string]bool{}
	for key, checked := range reader.checked {
		kind, _, _ := strings.Cut(key, "\x00")
		if kind != mobileBriefFindSource {
			references[key] = checked
		}
	}
	return mobileOfferVersion("resolved-sources", "", struct {
		References map[string]bool
		Sources    map[string]activitySourceProvenance
		Memory     map[string]memoryReadProvenance
	}{references, sources, reader.p.memoryProvenance}), nil
}

func mobileOfferReferencesValid(refs []mobileOfferReference) bool {
	if len(refs) > mobileOfferReferencesMax {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		kind, id, ok := strings.Cut(ref.ID, ".")
		version, err := hex.DecodeString(ref.Version)
		if !ok || (kind != "suggestion" && kind != "find") || id == "" || !mobileValidID(ref.ID) || err != nil || len(version) != sha256.Size || ref.Version != strings.ToLower(ref.Version) || seen[ref.ID] {
			return false
		}
		seen[ref.ID] = true
	}
	return true
}

func mobileOfferSnapshot(reader *mcpBriefReader, reference mobileOfferReference, find *mobileOfferFindSources) (*mobileOfferSourceSnapshot, error) {
	resolved, err := mobileOfferResolvedSources(reader)
	if err != nil {
		return nil, err
	}
	result := &mobileOfferSourceSnapshot{Reference: reference, Resolved: resolved, Find: find, Sources: []mobileOfferSourceReference{}}
	for key := range reader.checked {
		kind, id, ok := strings.Cut(key, "\x00")
		if !ok {
			return nil, errMobileOfferUnavailable
		}
		if kind != mobileBriefFindSource {
			result.Sources = append(result.Sources, mobileOfferSourceReference{Kind: kind, ID: id})
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		return result.Sources[i].Kind+"\x00"+result.Sources[i].ID < result.Sources[j].Kind+"\x00"+result.Sources[j].ID
	})
	return result, nil
}

// All edited text stays source-derived. Admission records the already-validated graph
// independently of suggestion state, find expiry and the next topic search.
func (a *App) mobileOfferChatAdmission(ctx context.Context, prompt string, refs []mobileOfferReference) (string, string, error) {
	if len(refs) == 0 {
		return prompt, "", nil
	}
	if !mobileOfferReferencesValid(refs) {
		return "", "", errMobileOfferUnavailable
	}
	view, err := a.mobileOffers(ctx)
	if err != nil {
		return "", "", err
	}
	current := make(map[string]mobileOffer, len(view.Offers))
	for _, offer := range view.Offers {
		current[offer.ID] = offer
	}
	provenance := mobileOfferChatSources{OfferReferences: refs, OfferSources: []mobileOfferSourceSnapshot{}}
	for _, ref := range refs {
		offer, ok := current[ref.ID]
		if !ok || offer.Version != ref.Version || offer.Sources == nil {
			return "", "", errMobileOfferUnavailable
		}
		provenance.OfferSources = append(provenance.OfferSources, *offer.Sources)
	}
	subjects, err := mobileGoalSubjects(a)
	if err != nil {
		return "", "", errMobileStoreUnavailable
	}
	if mobileOfferEchoesMail(mobileOffer{Prompt: prompt}, subjects) {
		return "", "", errMobileOfferUnavailable
	}
	metadata, err := json.Marshal(provenance)
	if err != nil {
		return "", "", err
	}
	return redactActivityModelText(prompt), string(metadata), nil
}

func (a *App) mobileOfferChatText(ctx context.Context, prompt string, refs []mobileOfferReference) (string, error) {
	text, _, err := a.mobileOfferChatAdmission(ctx, prompt, refs)
	return text, err
}

// Source metadata is a read dependency, not a grant. Malformed or older records
// that name an offer without its frozen graph cannot be replayed as ordinary text.
func mobileOfferMetadataPresent(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		// Invalid metadata cannot prove the absence of an escaped dependency.
		// Preserve it in storage and withhold its suffix until it can be resolved.
		return true
	}
	for key := range fields {
		if strings.EqualFold(key, "offerReferences") || strings.EqualFold(key, "offerSources") {
			return true
		}
	}
	return false
}

func mobileOfferURLHash(raw string) string {
	key := topicLinkKey(raw)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func (a *App) mobileOfferFindSourcesReadable(reader *mcpBriefReader, find *mobileOfferFindSources) bool {
	if find == nil || len(find.Sent) == 0 || len(find.Sent) > topicFindsTopics || !a.topicLabelsMayLeave() {
		return false
	}
	dir, err := topicsStateDir()
	if err != nil {
		return false
	}
	var current topicsStateFile
	if !mcpReadBriefJSON(filepath.Join(dir, topicsStateFileName), &current) || !current.enabled() {
		return false
	}
	matched := false
	for _, sent := range find.Sent {
		if current.Overrides[sent.ID].Removed || !reader.interestAllowed(dir, sent, 0) {
			return false
		}
		matched = matched || sent.ID == find.TopicID
	}
	if !matched {
		return false
	}
	run, err := a.store.ReadMCPRun(reader.p.ctx, find.RunID)
	if err != nil || run.Status != "ok" || !reader.p.timeAllowed(run.StartedAt, run.FinishedAt) {
		return false
	}
	_, meta, ok := splitResearchMeta(run.ResultSummary)
	if !ok || find.URLHash == "" {
		return false
	}
	for _, raw := range meta.URLs {
		if mobileOfferURLHash(raw) == find.URLHash {
			return true
		}
	}
	return false
}

// A batch owns no locks: dispatch may already hold the publication barriers.
// Revisions include ordinary activity protection writes as well as deletions.
// A concurrent mutation conservatively rejects the source-derived suffix.
type mobileOfferReadStamp struct {
	activity storage.ReadRevision
	main     storage.ReadRevision
	files    [sha256.Size]byte
	policy   [sha256.Size]byte
	privacy  uint64
}

func (a *App) mobileOfferReadStamp(ctx context.Context) (mobileOfferReadStamp, error) {
	var stamp mobileOfferReadStamp
	if a == nil || a.store == nil || a.activity == nil || a.activity.store == nil {
		return stamp, errMobileStoreUnavailable
	}
	stamp.privacy = a.activity.store.readPrivacyRevision.Load()
	if stamp.privacy%2 != 0 {
		return stamp, errMobileOfferUnavailable
	}
	var err error
	stamp.activity, err = storage.DatabaseRevision(ctx, a.activity.store.db)
	if err == nil {
		stamp.main, err = a.store.ReadRevision(ctx)
	}
	if err == nil {
		stamp.files, err = a.mobileOfferSourceFiles(ctx, a)
	}
	if err == nil {
		// Settings updates these live values before persisting ui_state. Their
		// canonical snapshot closes that interval without taking publication
		// barriers recursively when dispatch already owns them.
		var raw []byte
		raw, err = json.Marshal(struct {
			Sensitive  map[string]string
			VoiceInput map[string]string
			ClosedEyes bool
		}{pii.GetUserSensitiveApps(), GetUserVoiceInputApps(), a.IsClosedEyesModeEnabled()})
		if err == nil {
			stamp.policy = sha256.Sum256(raw)
		}
	}
	return stamp, err
}

// Validate each frozen graph only once per history projection; descendants use
// that proof and the same current mail hold, never a global authorization cache.
func (a *App) mobileOfferStoredProof(ctx context.Context, raw string) bool {
	var provenance mobileOfferChatSources
	if len(raw) > 2<<20 || json.Unmarshal([]byte(raw), &provenance) != nil || len(provenance.OfferReferences) == 0 ||
		!mobileOfferReferencesValid(provenance.OfferReferences) || len(provenance.OfferSources) != len(provenance.OfferReferences) ||
		a == nil || a.activity == nil || a.activity.store == nil {
		return false
	}
	p, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return false
	}
	before, err := p.mcpBriefSupplementalPolicy()
	if err != nil {
		return false
	}
	for index, snapshot := range provenance.OfferSources {
		if snapshot.Reference != provenance.OfferReferences[index] || len(snapshot.Sources) > 256 ||
			(len(snapshot.Sources) == 0 && snapshot.Find == nil) {
			return false
		}
		reader := mobileOfferReader(p)
		for _, source := range snapshot.Sources {
			if source.Kind == mobileBriefFindSource {
				return false
			}
			if _, ok := reader.reference(source.Kind, source.ID, 0); !ok {
				return false
			}
		}
		if snapshot.Find != nil && !a.mobileOfferFindSourcesReadable(reader, snapshot.Find) {
			return false
		}
		resolved, err := mobileOfferResolvedSources(reader)
		if err != nil || reader.limited || resolved != snapshot.Resolved {
			return false
		}
	}
	current, err := a.mcpReadPrivacy(ctx)
	if err != nil || current.calendar != p.calendar || !reflect.DeepEqual(current.hidden, p.hidden) ||
		!reflect.DeepEqual(current.ignored, p.ignored) || !reflect.DeepEqual(current.pauses, p.pauses) {
		return false
	}
	after, err := current.mcpBriefSupplementalPolicy()
	if err != nil || before != after {
		return false
	}
	return true
}

func (a *App) mobileOfferStoredText(ctx context.Context, text, raw string) (string, bool) {
	if !mobileOfferMetadataPresent(raw) {
		return text, true
	}
	before, err := a.mobileOfferReadStamp(ctx)
	if err != nil || !a.mobileOfferStoredProof(ctx, raw) {
		return "", false
	}
	subjects, err := mobileGoalSubjects(a)
	if err != nil || mobileOfferEchoesMail(mobileOffer{Prompt: text}, subjects) {
		return "", false
	}
	after, err := a.mobileOfferReadStamp(ctx)
	if err != nil || before != after {
		return "", false
	}
	return redactActivityModelText(text), true
}

// Callers must supply the complete stored chat before pagination or compaction.
// Any later turn can depend on an earlier source, so denial truncates the suffix.
func (a *App) mobileOfferHistoryRows(ctx context.Context, rows []storage.StreamItemRow) []storage.StreamItemRow {
	if len(rows) > 0 && rows[0].SessionLocalID != "" {
		session, err := a.store.LoadSession(ctx, rows[0].SessionLocalID)
		if err != nil {
			return nil
		}
		if session != nil && session.Kind == storage.SessionKindSide && session.ParentID != "" {
			if _, _, err := a.sourceBoundChatHistory(ctx, session.ParentID); err != nil {
				return nil
			}
		}
	}
	return a.mobileOfferHistoryRowsDirect(ctx, rows)
}

func (a *App) mobileOfferHistoryRowsDirect(ctx context.Context, rows []storage.StreamItemRow) []storage.StreamItemRow {
	hasSources := false
	for _, row := range rows {
		hasSources = hasSources || mobileOfferMetadataPresent(row.AutomationContext)
	}
	if !hasSources {
		return rows
	}
	before, err := a.mobileOfferReadStamp(ctx)
	if err != nil {
		return filterSourceBoundChatRows(rows, nil)
	}
	proofs := map[string]bool{}
	for _, row := range rows {
		if !mobileOfferMetadataPresent(row.AutomationContext) {
			continue
		}
		if _, checked := proofs[row.AutomationContext]; !checked {
			proofs[row.AutomationContext] = a.mobileOfferStoredProof(ctx, row.AutomationContext)
			if !proofs[row.AutomationContext] {
				break
			}
		}
	}
	// Read subjects after graph traversal: a late policy mutation must invalidate
	// the whole projection. Mail checks inspect original text before redaction.
	subjects, err := mobileGoalSubjects(a)
	if err != nil {
		return filterSourceBoundChatRows(rows, nil)
	}
	filtered := filterSourceBoundChatRows(rows, func(text, metadata string) (string, bool) {
		return text, proofs[metadata] && !mobileOfferEchoesMail(mobileOffer{Prompt: text}, subjects)
	})
	after, err := a.mobileOfferReadStamp(ctx)
	if err != nil || before != after {
		return filterSourceBoundChatRows(rows, nil)
	}
	return filtered
}

// The admission transaction holds the main database writer lock. Its final
// source read must never borrow the normal Store's sole connection: another
// writer can own that connection while waiting for admission to commit.
// Construct only the dependencies used by source readers, without copying App
// mutexes, atomics, runners or services. Current activity and file stores remain
// shared; SQLite policy/chat/calendar reads use the dedicated read-only handle.
func (a *App) mobileOfferAdmissionReader(ctx context.Context) *App {
	if a == nil {
		return nil
	}
	// Capture before beginning the writer transaction. A service holding mu
	// may itself need the normal database connection to finish its work.
	a.mu.Lock()
	defer a.mu.Unlock()
	return &App{ctx: ctx, activity: a.activity, memoryStore: a.memoryStore, handoffStore: a.handoffStore}
}

func (a *App) mobileOfferChatTextReadOnly(ctx context.Context, prompt string, refs []mobileOfferReference, reader *App) (string, error) {
	if a == nil || reader == nil || reader.store == nil {
		return "", errMobileStoreUnavailable
	}
	if a.IsClosedEyesModeEnabled() {
		return "", errMobileOfferUnavailable
	}
	text, err := reader.mobileOfferChatText(ctx, prompt, refs)
	// Closed-eyes mode is live state on the real App, not a copied atomic.
	// Recheck it after all source traversal before allowing the row to commit.
	if a.IsClosedEyesModeEnabled() {
		return "", errMobileOfferUnavailable
	}
	return text, err
}

type mobileOfferList struct {
	Offers        []mobileOffer `json:"offers"`
	NextAllowedAt string        `json:"nextAllowedAt,omitempty"`
}

// Serialize eligibility and feedback across phones, in addition to each
// device's request worker. A second phone must not reweight the same find.
var mobileOfferDecisionMu sync.Mutex

// Existing offers have no action type for holds. Suppress the explicit
// calendar-hold titles until the typed action can be handled safely.
var mobileCalendarHoldOffer = regexp.MustCompile(`(?i)^\s*(?:hold|reserve|block|set aside)\b.*\b(?:time|block|calendar|today|tomorrow|monday|tuesday|wednesday|thursday|friday|saturday|sunday|morning|afternoon|evening|\d{1,2}(?::\d{2})?\s*(?:am|pm))\b|^\s*(?:为.{0,40})?(?:留出|预留|占用).*(?:时间|日历|点|周[一二三四五六日天]|星期|上午|下午|早上|今天|明天)|^\s*(?:把)?(?:今天|明天|周[一二三四五六日天]|星期[一二三四五六日天]).*留给`)

func mobileOfferText(text string, limit int) string {
	return mobileBriefText(strings.Join(strings.Fields(redactActivityModelText(text)), " "), limit)
}

func mobileOfferEchoesMail(offer mobileOffer, subjects []string) bool {
	texts := []string{offer.Title, offer.Why, offer.Prompt}
	for _, text := range offer.WhyLocalized {
		texts = append(texts, text)
	}
	for _, text := range texts {
		folded := mobileGoalFold(text)
		for _, subject := range subjects {
			if strings.Contains(folded, subject) || (len([]rune(folded)) >= mobileGoalSubjectMinRunes && strings.Contains(subject, folded)) {
				return true
			}
		}
	}
	return false
}

// Check complete source prose before redaction or display truncation: either
// transformation can otherwise disguise a mail subject or split a credential.
func mobileOfferForPhone(offer mobileOffer, subjects []string) (mobileOffer, bool) {
	return mobileOfferForPhoneWithHolds(offer, subjects, false)
}

func mobileOfferForPhoneWithHolds(offer mobileOffer, subjects []string, holds bool) (mobileOffer, bool) {
	if (mobileCalendarHoldOffer.MatchString(offer.Title) && (!holds || mobileHoldOfferThing(offer.Title) == "")) || mobileOfferEchoesMail(offer, subjects) {
		return mobileOffer{}, false
	}
	offer.Title = mobileOfferText(offer.Title, 200)
	offer.Why = mobileOfferText(offer.Why, 160)
	offer.Prompt = mobileOfferText(offer.Prompt, 1000)
	for locale, text := range offer.WhyLocalized {
		offer.WhyLocalized[locale] = mobileOfferText(text, 160)
	}
	return offer, offer.Title != "" && mobileValidID(offer.ID)
}

// Reuse the source reader's bounded traversal, current capture/privacy policy,
// corrected-memory filter and frozen topic-search provenance. In particular a
// cached title or the existence of a brief is never a sharing grant.
func (a *App) mobileOffers(ctx context.Context) (mobileOfferList, error) {
	return a.mobileOffersWithHolds(ctx, false)
}

func (a *App) mobileOffersWithHolds(ctx context.Context, holds bool) (mobileOfferList, error) {
	result := mobileOfferList{Offers: []mobileOffer{}}
	if a == nil || a.store == nil || !a.mobileStoreReady() {
		return result, errMobileStoreUnavailable
	}
	if a.activity == nil || a.activity.store == nil {
		return result, errMobileStoreUnavailable
	}
	sourceRevision := a.activity.store.readPrivacyRevision.Load()
	if sourceRevision%2 != 0 {
		return result, errMobileStoreUnavailable
	}
	p, err := a.mcpReadPrivacy(ctx)
	if errors.Is(err, errMCPReadPaused) {
		return result, nil
	}
	if err != nil {
		return result, errMobileStoreUnavailable
	}
	before, err := p.mcpBriefSupplementalPolicy()
	if err != nil {
		return result, errMobileStoreUnavailable
	}
	subjects, err := mobileGoalSubjects(a)
	if err != nil {
		return result, errMobileStoreUnavailable
	}
	now := time.Now()
	view, err := a.GetTodaySuggestion()
	if err != nil {
		return result, errMobileStoreUnavailable
	}
	if view.Available {
		reader := mobileOfferReader(p)
		if item, observations := a.mobileSuggestionReadable(reader, view.ID, now); item != nil {
			resolved, err := mobileOfferResolvedSources(reader)
			if err != nil {
				return result, err
			}
			// GetTodaySuggestion's card copy is already shortened. Use the
			// original display fields so privacy checks see every character.
			plan := item.ExecutionPlan.Automation
			title := firstNonEmpty(plan.Name, item.Title)
			prompt := title + "\n\n" + firstNonEmpty(plan.Description, item.Description)
			why := fmt.Sprintf("Isoform noticed this pattern %d times across %d days.", view.Times, view.Days)
			offer, allowed := mobileOfferForPhoneWithHolds(mobileOffer{
				ID: "suggestion." + view.ID, Kind: "suggestion", Title: title,
				Why: why, WhyLocalized: map[string]string{"en": why, "zh-CN": fmt.Sprintf("Isoform 在 %d 天内观察到了 %d 次这一模式。", view.Days, view.Times)},
				Prompt:   prompt,
				Evidence: mobileSuggestionEvidence(view.Times, view.Days, observations),
				Version: mobileOfferVersion("suggestion."+view.ID, prompt, struct {
					SourceKind   string
					SourceID     string
					Evidence     []InitiativeEvidence
					Observations []InitiativeObservation
					Resolved     string
				}{item.SourceKind, item.SourceID, item.Evidence, observations, resolved}),
			}, subjects, holds)
			if offer.Evidence == nil {
				allowed = false
			} else {
				offer.DeckKind = mobileOfferDeckKind(offer.Evidence.Source)
			}
			if allowed {
				offer.Sources, err = mobileOfferSnapshot(reader, mobileOfferReference{ID: offer.ID, Version: offer.Version}, nil)
				if err != nil {
					return result, err
				}
				result.Offers = append(result.Offers, offer)
			}
		}
	}
	dir, err := topicsStateDir()
	if err != nil {
		return result, errMobileStoreUnavailable
	}
	var state topicsStateFile
	if a.topicLabelsMayLeave() && mcpReadBriefJSON(filepath.Join(dir, topicsStateFileName), &state) {
		for _, find := range topicFindsFresh(state, now) {
			if len(result.Offers) >= topicFindsMax+1 {
				break
			}
			if find.Feedback == "less" || state.Overrides[find.TopicID].Removed {
				continue
			}
			reader := mobileOfferReader(p)
			source, allowed := reader.reference(mobileBriefFindSource, find.ID, 0)
			if !allowed {
				continue
			}
			resolved, err := mobileOfferResolvedSources(reader)
			if err != nil {
				return result, err
			}
			observation := find
			observation.WhatsNew = ""
			why := topicFindWhy(observation, now, time.Local)
			offer, allowed := mobileOfferForPhoneWithHolds(mobileOffer{
				ID: "find." + find.ID, Kind: "topic_find", Title: find.Title,
				Why: why, WhyLocalized: map[string]string{"en": why, "zh-CN": "你关注的话题有了新动态：" + find.Topic},
				Prompt: find.Title + "\n\n" + find.WhatsNew,
			}, subjects, holds)
			looked, lookedErr := time.Parse(time.RFC3339, find.LookedAt)
			if !allowed || lookedErr != nil {
				// A find with no record of when you looked at its topic has no fact to show.
				continue
			}
			offer.Evidence = &mobileOfferEvidence{
				Text: offer.Why, TextLocalized: offer.WhyLocalized, Source: "screen",
				At: looked.UTC().Format(time.RFC3339),
			}
			if source.URL != nil {
				offer.Prompt += "\n\n" + source.URL.Text
			}
			offer.Version = mobileOfferVersion(offer.ID, find.Title+"\n\n"+find.WhatsNew, struct {
				RunID    string
				TopicID  string
				URL      string
				Sent     []topicSent
				Resolved string
			}{state.Finds.RunID, find.TopicID, find.URL, state.Finds.Sent, resolved})
			if !mobileOfferEchoesMail(offer, subjects) {
				// Only identities and source memberships are durable. Search query prose is unnecessary.
				sent := make([]topicSent, 0, len(state.Finds.Sent))
				for _, topic := range state.Finds.Sent {
					frozen := topicSent{ID: topic.ID, ReadSources: topic.ReadSources}
					if len(topic.Queries) != 0 {
						frozen.Queries = []string{"present"}
					}
					sent = append(sent, frozen)
				}
				offer.Sources, err = mobileOfferSnapshot(reader, mobileOfferReference{ID: offer.ID, Version: offer.Version},
					&mobileOfferFindSources{RunID: state.Finds.RunID, TopicID: find.TopicID, URLHash: mobileOfferURLHash(find.URL), Sent: sent})
				if err != nil {
					return result, err
				}
				result.Offers = append(result.Offers, offer)
			}
		}
	}
	// Match other external reads: an in-flight pause, hide or topic removal
	// suppresses the whole response instead of leaking already copied prose.
	current, err := a.mcpReadPrivacy(ctx)
	if err != nil || current.calendar != p.calendar || !reflect.DeepEqual(current.hidden, p.hidden) || !reflect.DeepEqual(current.ignored, p.ignored) || !reflect.DeepEqual(current.pauses, p.pauses) {
		return mobileOfferList{Offers: []mobileOffer{}}, errMobileStoreUnavailable
	}
	after, err := current.mcpBriefSupplementalPolicy()
	if err != nil || before != after || sourceRevision != a.activity.store.readPrivacyRevision.Load() {
		return mobileOfferList{Offers: []mobileOffer{}}, errMobileStoreUnavailable
	}
	for _, offer := range result.Offers {
		noteProposalMade(judgmentOffer, offer.ID)
	}
	return result, nil
}

func (a *App) mobileSuggestionReadable(reader *mcpBriefReader, id string, now time.Time) (*Initiative, []InitiativeObservation) {
	moment, err := a.latestSuggestionMoment(reader.p.ctx)
	if err != nil || moment == nil || moment.SubjectID != id || (moment.ExpiresAt > 0 && moment.ExpiresAt <= now.UnixMilli()) {
		return nil, nil
	}
	item, err := a.activity.store.GetInitiative(id)
	if err != nil || item.Status != InitiativeStatusProposed || item.ObservationCount < suggestionMinObservations || item.ExecutionPlan.Automation == nil {
		return nil, nil
	}
	observations, err := a.activity.store.ListInitiativeObservations(id)
	if err != nil || len(observations) > 100 || len(item.Evidence) > 100 {
		return nil, nil
	}
	readable := func(kind, sourceID string) bool {
		if kind == "activity" {
			kind = "activity_session"
		}
		_, allowed := reader.reference(kind, sourceID, 0)
		return allowed
	}
	evidenceReadable := func(evidence []InitiativeEvidence, kind, sourceID string) bool {
		if len(evidence) > 100 || !readable(kind, sourceID) {
			return false
		}
		for _, ref := range evidence {
			if !readable(firstNonEmpty(ref.SourceKind, ref.Kind, kind), firstNonEmpty(ref.SourceID, sourceID)) {
				return false
			}
		}
		return true
	}
	if !evidenceReadable(item.Evidence, item.SourceKind, item.SourceID) {
		return nil, nil
	}
	for _, observation := range observations {
		if !evidenceReadable(observation.Evidence, observation.SourceKind, observation.SourceID) {
			return nil, nil
		}
	}
	if reader.limited {
		return nil, nil
	}
	return &item, observations
}

var (
	errOfferFindMoreNotStarted   = errors.New("the search could not be started")
	errOfferFindMoreStillRunning = errors.New("the search is still running")
)

// findMoreOffers runs a manual search for new ideas when one is allowed today
// (no cooldown and not already run), waits up to two minutes for it, and
// returns the ideas with the time the next search is allowed. The phone's
// Find more and the Mac's Tasks › Up next share it, so both keep one cooldown.
func (a *App) findMoreOffers(ctx context.Context) (mobileOfferList, error) {
	svc := a.topicsSvc()
	if svc.topicFindsCooldownUntil().IsZero() && svc.store.load().Finds.RanOn != svc.now().In(time.Local).Format("2006-01-02") {
		before := svc.store.load().Finds
		status, err := a.startTopicFinds("manual")
		if err != nil || status == "starting" || status == "unavailable" {
			return mobileOfferList{}, errOfferFindMoreNotStarted
		}
		if status == "started" || status == "running" {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(2 * time.Minute)
			defer timeout.Stop()
			for svc.store.load().Finds.RunID == before.RunID && svc.store.load().Finds.RanAt == before.RanAt {
				select {
				case <-ctx.Done():
					return mobileOfferList{}, ctx.Err()
				case <-timeout.C:
					return mobileOfferList{}, errOfferFindMoreStillRunning
				case <-ticker.C:
				}
			}
		}
	}
	view, err := a.mobileOffers(ctx)
	if err != nil {
		return mobileOfferList{}, errMobileStoreUnavailable
	}
	if until := svc.topicFindsCooldownUntil(); !until.IsZero() {
		view.NextAllowedAt = until.UTC().Format(time.RFC3339)
	}
	return view, nil
}

func (bridge *mobileBridge) serveOfferFindMore(w http.ResponseWriter, r *http.Request) {
	view, err := bridge.app.findMoreOffers(r.Context())
	switch {
	case err == nil:
		mobileJSON(w, http.StatusOK, view, "")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	default:
		mobileJSON(w, http.StatusServiceUnavailable, nil, err.Error())
	}
}

func (bridge *mobileBridge) serveOfferList(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	view, err := bridge.mobileOfferListForDevice(r.Context(), device, r.URL.Query().Get("holds") == "1", calendarAccessGranted())
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func (bridge *mobileBridge) mobileOfferListForDevice(ctx context.Context, device mobileDeviceRecord, optedIn, granted bool) (mobileOfferList, error) {
	holds := optedIn && mobileHasScope(device.Scopes, mobileScopeHandoff) && bridge.mobileHoldsAvailable(granted)
	view, err := bridge.app.mobileOffersWithHolds(ctx, holds)
	if err != nil {
		return mobileOfferList{}, err
	}
	if holds {
		filtered := view.Offers[:0]
		for _, offer := range view.Offers {
			if mobileCalendarHoldOffer.MatchString(offer.Title) {
				thing := HoldThing{Title: mobileHoldOfferThing(offer.Title), SourceKind: "up_next", SourceID: offer.ID}
				if thing.Title == "" || offer.Evidence == nil {
					continue
				}
				proposal, proposeErr := bridge.mobileBuildHoldProposal(device, thing, "", offer.Evidence.Text, granted)
				if proposeErr != nil || len(proposal.Proposal.Slots) == 0 || proposal.Proposal.Dismissed || proposal.Proposal.Existing != nil {
					continue
				}
				offer.Hold = &proposal
			}
			filtered = append(filtered, offer)
		}
		view.Offers = filtered
	}
	return view, nil
}

func (bridge *mobileBridge) serveOfferDecline(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, offerID string) {
	if !mobileValidID(offerID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the idea id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Decision       string `json:"decision"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.Decision != "dismiss" && input.Decision != "not_now" {
		mobileJSON(w, http.StatusBadRequest, nil, "decision must be dismiss or not_now")
		return
	}
	mobileOfferDecisionMu.Lock()
	defer mobileOfferDecisionMu.Unlock()
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		holds := mobileHasScope(device.Scopes, mobileScopeHandoff) && bridge.mobileHoldsAvailable(calendarAccessGranted())
		err := bridge.app.decideTaskSuggestionWithHolds(r.Context(), offerID, input.Decision, "Not for me in Ideas on the phone.", engagement.ChannelPush, holds)
		switch {
		case errors.Is(err, errTaskSuggestionNotFound):
			mobileJSON(w, http.StatusNotFound, nil, "this idea is no longer on your computer")
		case err != nil:
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		default:
			mobileJSON(w, http.StatusOK, map[string]bool{"declined": true}, "")
		}
	})
}
