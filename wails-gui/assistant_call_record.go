package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/topicday"
	"wails-gui/storage"

	"yansu-agent/agent"
)

// The record behind Your data's "See them" (computer-data v1.1.0; the call
// record is storage/assistant_calls.go). Each piece of work that gives the
// assistant the person's data opens a scope on its context. Every call the
// router runs under that scope counts once (issueAgentRunScope), the work
// adds its sources by kind and opaque id as it reads them, and the scope is
// kept when the work ends, only if at least one call was made. Never text:
// the store refuses anything that isn't an id.

type assistantCallScopeKey struct{}

// assistantCallWrites tracks entries being written, for tests.
var assistantCallWrites sync.WaitGroup

type assistantCallScope struct {
	mu      sync.Mutex
	call    storage.AssistantCall
	times   int
	made    int
	sources map[[2]string]int
}

// withAssistantCallScope opens a scope for one piece of work. A context
// that already carries a scope for the same purpose keeps it, so a stretch
// of the day read in several passes is one entry; work of another purpose
// inside it (the activity a brief runs first) gets its own.
func withAssistantCallScope(ctx context.Context, purpose, ref string) (context.Context, *assistantCallScope, bool) {
	if s := assistantCallScopeFrom(ctx); s != nil && s.call.Purpose == purpose && s.call.Ref == ref {
		return ctx, s, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s := &assistantCallScope{call: storage.AssistantCall{Purpose: purpose, Ref: ref}, sources: map[[2]string]int{}}
	return context.WithValue(ctx, assistantCallScopeKey{}, s), s, true
}

func assistantCallScopeFrom(ctx context.Context) *assistantCallScope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(assistantCallScopeKey{}).(*assistantCallScope)
	return s
}

// add notes a source the work gave the assistant. ref is an opaque id, kept
// once however often it is read, or empty for a count of a kind that adds
// up (WeChat windows).
func (s *assistantCallScope) add(kind, ref string, n int) {
	if s == nil || !storage.AssistantCallSourceKind(kind) || (ref != "" && !storage.ContextUseID(ref)) {
		return
	}
	if n <= 0 {
		n = 1
	}
	key := [2]string{kind, ref}
	s.mu.Lock()
	if ref == "" {
		// A count of a kind adds up (three WeChat windows).
		s.sources[key] += n
	} else if s.sources[key] == 0 {
		// A named source is one source, however many passes read it.
		s.sources[key] = n
	}
	s.mu.Unlock()
}

// span widens the stretch of time the work read.
func (s *assistantCallScope) span(start, end int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if start > 0 && (s.call.SpanStart == 0 || start < s.call.SpanStart) {
		s.call.SpanStart = start
	}
	if end > s.call.SpanEnd {
		s.call.SpanEnd = end
	}
	s.mu.Unlock()
}

// madeClaims counts claims to check the work made (new lines on You).
func (s *assistantCallScope) madeClaims(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.mu.Lock()
	s.made += n
	s.mu.Unlock()
}

// calls reports how many calls were made under the scope.
func (s *assistantCallScope) calls() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.times
}

// called counts one call to the assistant under the scope.
func (s *assistantCallScope) called() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.times++
	s.mu.Unlock()
}

// noteAssistantCall counts a routed call against the scope in ctx, if any.
func noteAssistantCall(ctx context.Context) {
	assistantCallScopeFrom(ctx).called()
}

// finishAssistantCall keeps the scope's entry when at least one call was
// made. It never blocks the work: the write runs on its own.
func (a *App) finishAssistantCall(s *assistantCallScope) {
	if a == nil || s == nil {
		return
	}
	s.mu.Lock()
	call := s.call
	call.Times = s.times
	call.Made = s.made
	for key, n := range s.sources {
		call.Sources = append(call.Sources, storage.AssistantCallSource{Kind: key[0], Ref: key[1], Count: n})
	}
	s.mu.Unlock()
	if call.Times == 0 {
		return
	}
	a.recordAssistantCall(call)
}

// recordAssistantCall keeps one entry now. A Forget everything in progress
// keeps nothing.
func (a *App) recordAssistantCall(call storage.AssistantCall) {
	if a == nil || a.store == nil || derivedSavesQuiet() {
		return
	}
	if call.At == 0 {
		call.At = time.Now().UnixMilli()
	}
	store := a.store
	assistantCallWrites.Add(1)
	go func() {
		defer assistantCallWrites.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := store.RecordAssistantCall(ctx, call); err != nil {
			log.Printf("[assistant-calls] a %s entry was not kept: %v", call.Purpose, err)
		}
	}()
}

// forgetAssistantCallsForSource removes the entries written from one
// source, with the source.
func (a *App) forgetAssistantCallsForSource(kind, ref string) {
	if a == nil || a.store == nil || ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.store.ForgetAssistantCallsForSource(ctx, kind, ref); err != nil {
		log.Printf("[assistant-calls] entries for a forgotten %s were not removed: %v", kind, err)
	}
}

// forgetAssistantCall removes one answer's, brief's or proposal's entry.
func (a *App) forgetAssistantCall(purpose, ref string) {
	if a == nil || a.store == nil || ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.store.ForgetAssistantCall(ctx, purpose, ref); err != nil {
		log.Printf("[assistant-calls] a %s entry was not removed: %v", purpose, err)
	}
}

// activitySessionSourceKind is the What Isoform sees name of a session's
// source: a call or meeting, or the screen.
func activitySessionSourceKind(s ActivitySessionRow) string {
	if s.IsMeeting || strings.TrimSpace(s.MeetingApp) != "" {
		return "call"
	}
	return "screen"
}

// withDayScope opens (or joins) the scope for one stretch of the day: the
// group's sessions are its sources and its span.
func withDayScope(ctx context.Context, group sessionGroup) (context.Context, *assistantCallScope, bool) {
	ctx, scope, opened := withAssistantCallScope(ctx, storage.AssistantCallDay, "")
	for _, s := range group.Sessions {
		scope.add(activitySessionSourceKind(s), s.ID, 1)
		scope.span(s.StartTime, s.EndTime)
	}
	return ctx, scope, opened
}

// briefEvidenceSourceKind maps a brief's evidence kind to the name See them
// uses; the evidence's id is the ref, its label (a title) is never kept.
func briefEvidenceSourceKind(kind string) string {
	if mapped, ok := briefEvidenceKindMapped(kind); ok {
		return mapped
	}
	// A kind added after this list still reads as the day's activity, never
	// "Other"; TestEveryBriefEvidenceKindIsNamed keeps the known ones mapped.
	return "activity"
}

// briefEvidenceKindMapped names a known evidence kind; false for one this
// list doesn't know.
func briefEvidenceKindMapped(kind string) (string, bool) {
	switch {
	case kind == "mail":
		return "mail", true
	case strings.HasPrefix(kind, "calendar"), kind == "meeting_attendance", kind == "schedule", kind == "schedule_run":
		return "calendar", true
	case strings.HasPrefix(kind, "meeting"):
		return "call", true
	case kind == "session", kind == "watching", kind == "activity_session":
		return "screen", true
	case strings.HasPrefix(kind, "activity"), kind == "summary", kind == "thread_outcome", kind == "engagement_moment":
		return "activity", true
	case strings.HasPrefix(kind, "memory"), kind == "learning", kind == "self_model_entry":
		return "memory", true
	case strings.HasPrefix(kind, "knowledge"):
		return "knowledge", true
	case kind == "principal":
		return "people", true
	case kind == "purpose", kind == "initiative":
		return "goals", true
	case kind == "chat_message", kind == "brief_waiting_reply", kind == "manual":
		return "chat", true
	case kind == "news", kind == "web":
		return "web", true
	case kind == "deliverable", kind == "handoff", kind == "task":
		return "work", true
	case kind == "file":
		return "files", true
	case kind == "history":
		return "agents_history", true
	default:
		return "", false
	}
}

// recordBriefCall keeps a brief the assistant was given: one entry per
// brief, its evidence by kind and id.
func (a *App) recordBriefCall(brief MobileBriefV1, at time.Time) {
	if brief.BriefID == "" {
		return
	}
	call := storage.AssistantCall{Purpose: storage.AssistantCallBrief, Ref: brief.BriefID, At: at.UnixMilli(), Times: 1}
	for _, ev := range brief.Evidence {
		if ev.SourceID == "" || !storage.ContextUseID(ev.SourceID) {
			call.Sources = append(call.Sources, storage.AssistantCallSource{Kind: briefEvidenceSourceKind(ev.SourceKind), Count: 1})
			continue
		}
		call.Sources = append(call.Sources, storage.AssistantCallSource{Kind: briefEvidenceSourceKind(ev.SourceKind), Ref: ev.SourceID, Count: 1})
	}
	a.recordAssistantCall(call)
}

// recordWatchConversationCall keeps a watch conversation the assistant read
// (to name it): its hours are the sources, its time the span.
func (a *App) recordWatchConversationCall(c WatchConversation) {
	call := storage.AssistantCall{Purpose: storage.AssistantCallDay, Times: 1, SpanStart: c.StartAt, SpanEnd: c.EndAt}
	for _, id := range c.RecordingIDs {
		if storage.ContextUseID(id) {
			call.Sources = append(call.Sources, storage.AssistantCallSource{Kind: "watch", Ref: id, Count: 1})
		}
	}
	if len(call.Sources) == 0 {
		call.Sources = append(call.Sources, storage.AssistantCallSource{Kind: "watch", Count: 1})
	}
	a.recordAssistantCall(call)
}

// withBriefCallScope opens the scope that counts the calls writing a brief.
func withBriefCallScope(ctx context.Context) (context.Context, *assistantCallScope) {
	ctx, scope, _ := withAssistantCallScope(ctx, storage.AssistantCallBrief, "")
	return ctx, scope
}

// answerScopes holds the scope of each chat turn in progress by its tag, so
// a context tool the assistant calls (through the tool server, outside the
// run's context) adds its reads to the answer.
var answerScopes sync.Map // tag -> *assistantCallScope

// beginAnswerCallScope opens the scope for one answer in Chat; questionID
// is the question's item id, for Open, or empty on a surface without one.
func (a *App) beginAnswerCallScope(ctx context.Context, tag, questionID string) (context.Context, *assistantCallScope) {
	if !storage.ContextUseID(questionID) {
		questionID = ""
	}
	ctx, scope, _ := withAssistantCallScope(ctx, storage.AssistantCallAnswer, questionID)
	if tag != "" {
		answerScopes.Store(tag, scope)
	}
	return ctx, scope
}

// endAnswerCallScope keeps the answer's entry when the turn ends.
func (a *App) endAnswerCallScope(tag string, scope *assistantCallScope) {
	if tag != "" {
		answerScopes.CompareAndDelete(tag, scope)
	}
	a.finishAssistantCall(scope)
}

// answerScopeFor returns the scope of the chat turn running under tag.
func answerScopeFor(tag string) *assistantCallScope {
	if v, ok := answerScopes.Load(tag); ok {
		s, _ := v.(*assistantCallScope)
		return s
	}
	return nil
}

// chatContextToolSourceKind is the See them name of what a Chat context
// tool read.
func chatContextToolSourceKind(tool string) string {
	switch tool {
	case "PeopleLookup":
		return "people"
	case "RecentActivity", "ActivityDay", "ActivityList", "ActivitySession", "ActivitySearch":
		return "activity"
	default:
		return "memory"
	}
}

// noteChatContextRead adds one published context read to the answer under
// tag: the entries it returned by kind and id, or the tool's kind when it
// names none.
func noteChatContextRead(tag, tool string, entries []contextUseEntry) {
	scope := answerScopeFor(tag)
	if scope == nil {
		return
	}
	if len(entries) == 0 {
		scope.add(chatContextToolSourceKind(tool), "", 1)
		return
	}
	for _, e := range entries {
		kind := e.Kind
		if !storage.AssistantCallSourceKind(kind) {
			kind = chatContextToolSourceKind(tool)
		}
		scope.add(kind, e.ID, 1)
	}
}

// recordWrittenProposals keeps one entry for each proposal the assistant
// wrote in this build: those whose id the previous copy didn't hold. It
// counts a proposal when it is written, not when it is shown (proposals
// stay offered while their switch is off). The topic is its source.
func (a *App) recordWrittenProposals(previous *topicday.Copy, built topicday.Copy) {
	had := map[string]bool{}
	if previous != nil {
		for _, t := range previous.Topics {
			if t.Proposal != nil {
				had[t.Proposal.ID] = true
			}
		}
	}
	at := built.BuiltAt
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	for _, t := range built.Topics {
		if t.Proposal == nil || t.Proposal.ID == "" || had[t.Proposal.ID] || !storage.ContextUseID(t.Proposal.ID) {
			continue
		}
		src := storage.AssistantCallSource{Kind: "topic_names", Count: 1}
		if storage.ContextUseID(t.ID) {
			src.Ref = t.ID
		}
		a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallProposal, Ref: t.Proposal.ID, At: at, Times: 1, Sources: []storage.AssistantCallSource{src}})
	}
}

// recordMessagesCall keeps one Messages conversation the assistant read for
// asks and commitments; the conversation is named only by its hashed key.
func (a *App) recordMessagesCall(chatKey string, from, to time.Time) {
	src := storage.AssistantCallSource{Kind: "messages", Count: 1}
	if storage.ContextUseID(chatKey) {
		src.Ref = chatKey
	}
	a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallDay, Times: 1, SpanStart: from.UnixMilli(), SpanEnd: to.UnixMilli(), Sources: []storage.AssistantCallSource{src}})
}

// forgetAssistantCallsForSession removes the entries written from one
// activity session, a screen stretch or a call alike.
func (a *App) forgetAssistantCallsForSession(sessionID string) {
	a.forgetAssistantCallsForSource("screen", sessionID)
	a.forgetAssistantCallsForSource("call", sessionID)
}

// forgetAssistantCallsBefore ages entries out with the activity retention.
func (a *App) forgetAssistantCallsBefore(thresholdMs int64) {
	if a == nil || a.store == nil || thresholdMs <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.store.ForgetAssistantCallsBefore(ctx, thresholdMs); err != nil {
		log.Printf("[assistant-calls] expired entries were not removed: %v", err)
	}
}

// forgetAllAssistantCalls is Forget everything's step for the record.
func (a *App) forgetAllAssistantCalls(ctx context.Context) error {
	if a == nil || a.store == nil {
		return nil
	}
	return a.store.ForgetAllAssistantCalls(ctx)
}

// noteRoutedAssistantCall counts one routed call. Under a scope it counts
// against that work. Outside one, a background call is kept as a learning
// entry and a chat call as an answer, so the record misses no call that
// read the person's data. Hand-offs (the agents' own row) and scheduled
// runs (the person's own prompts, and topic searches, which have their
// own destination) aren't the assistant's reading.
func (a *App) noteRoutedAssistantCall(ctx context.Context, input *agent.TaskInput) {
	if scope := assistantCallScopeFrom(ctx); scope != nil {
		scope.called()
		return
	}
	if a == nil || input == nil {
		return
	}
	now := time.Now()
	switch input.BackendHint {
	case agent.BackendHintHandoff, agent.BackendHintCron, agent.BackendHintInitiative, agent.BackendHintComputerUse:
		return
	case agent.BackendHintChat:
		a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallAnswer, At: now.UnixMilli(), Times: 1,
			Sources: []storage.AssistantCallSource{{Kind: "chat", Count: 1}}})
		return
	}
	label := agent.CallerLabel(input)
	kind := learningSourceKind(label)
	// One entry per group per hour: a pass that runs every few minutes is one
	// line with its count, not hundreds. It names no item, so it's counted
	// by the hour (see.byHour).
	hour := now.Truncate(time.Hour)
	a.recordAssistantCall(storage.AssistantCall{
		Purpose: storage.AssistantCallLearning, Ref: learningRef(learningGroupForCall(label, kind), hour),
		At: now.UnixMilli(), SpanStart: hour.UnixMilli(), SpanEnd: hour.Add(time.Hour).UnixMilli(), Times: 1,
		Sources: []storage.AssistantCallSource{{Kind: kind, Count: 1}},
	})
}

// learningSourceKind names what a background call read, from the code that
// made it (agent.CallerLabel: function names only, never content).
func learningSourceKind(label string) string {
	l := strings.ToLower(label)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("person", "people", "principal", "speaker"):
		return "people"
	case has("purpose", "goal", "initiative"):
		return "goals"
	case has("knowledge", "knowhow"):
		return "knowledge"
	case has("memory", "dream", "portrait", "aboutyou", "selfmodel", "workprofile", "tombstone"):
		return "memory"
	default:
		// Everything else a background pass reads is the day's activity.
		return "activity"
	}
}

// See them's groups for learning entries (design T10): what the call made.
const (
	learningGroupSense = "sense" // Making sense of your day: timeline summaries, threads, daily summaries
	learningGroupAbout = "about" // Learning about you: memory, About you, people, playbooks
	learningGroupWork  = "work"  // Work made for you: compose
	learningGroupOther = "other" // no group yet: held back from the sheet
)

func validLearningGroup(group string) bool {
	switch group {
	case learningGroupSense, learningGroupAbout, learningGroupWork, learningGroupOther:
		return true
	default:
		return false
	}
}

// learningRef keys a learning entry: one per group per hour.
func learningRef(group string, hour time.Time) string {
	return "learning:" + group + ":" + hour.UTC().Format("2006010215")
}

// learningGroupOf reads an entry's group back from its ref.
func learningGroupOf(ref string) string {
	parts := strings.Split(ref, ":")
	if len(parts) == 3 && parts[0] == "learning" && validLearningGroup(parts[1]) {
		return parts[1]
	}
	return learningGroupOther
}

// withLearningScope opens (or joins) the scope for one background pass in
// group: an entry per group per hour, naming what the pass read, so
// forgetting any of it removes the entry. The pass adds its sources with
// scope.add, and what it made with scope.madeClaims.
func withLearningScope(ctx context.Context, group string) (context.Context, *assistantCallScope, bool) {
	if !validLearningGroup(group) {
		group = learningGroupOther
	}
	hour := time.Now().Truncate(time.Hour)
	ctx, scope, opened := withAssistantCallScope(ctx, storage.AssistantCallLearning, learningRef(group, hour))
	if opened {
		scope.span(hour.UnixMilli(), hour.Add(time.Hour).UnixMilli())
	}
	return ctx, scope, opened
}

// addSessions names activity sessions a pass read; a forgotten session
// takes the entry with it (forgetAssistantCallsForSession).
func (s *assistantCallScope) addSessions(ids []string) {
	for _, id := range ids {
		s.add("screen", id, 1)
	}
}

// runLearningClaudeModelContext is runLearningClaudeModel on a context that
// carries a See them scope; its parent stays uncancellable as before.
func (a *App) runLearningClaudeModelContext(ctx context.Context, prompt, model string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModelRouted(context.WithoutCancel(ctx), agent.BackendHintLearning, prompt, model, timeout...)
}

// startAssistantCallRecord keeps the record true to its sources: every
// privacy withdrawal of a source, a session or a stretch of time removes
// the entries written from it.
func (a *App) startAssistantCallRecord() {
	assistantCallRecorder.Store(a)
	subscribePrivacyWithdrawals(a.assistantCallsOnWithdrawal)
}

// assistantCallRecorder is the app that keeps the record, for passes that
// run without one in reach (self-dream, purpose extraction, work profile).
var assistantCallRecorder atomic.Pointer[App]

// finishPassScope keeps a pass's entry through the recording app.
func finishPassScope(scope *assistantCallScope) {
	assistantCallRecorder.Load().finishAssistantCall(scope)
}

// withdrawalSourceKind maps a withdrawn source's kind to the record's.
func withdrawalSourceKind(kind string) (string, bool) {
	switch kind {
	case "memory", "memory_markdown":
		return "memory", true
	case "knowledge", "knowledge_markdown":
		return "knowledge", true
	case "principal":
		return "people", true
	case "activity":
		return "activity", true
	case "purpose":
		return "goals", true
	case "topic":
		return "topic_names", true
	case "mail":
		return "mail", true
	default:
		return "", false
	}
}

func (a *App) assistantCallsOnWithdrawal(w privacyWithdrawal) {
	if a == nil || a.store == nil {
		return
	}
	// The publisher doesn't wait on the record's writes.
	assistantCallWrites.Add(1)
	go func() {
		defer assistantCallWrites.Done()
		a.forgetAssistantCallsWithdrawn(w)
	}()
}

func (a *App) forgetAssistantCallsWithdrawn(w privacyWithdrawal) {
	scope := w.Scope
	switch scope.Kind {
	case privacyWithdrawalSession:
		a.forgetAssistantCallsForSession(scope.ID)
	case privacyWithdrawalTimeRange:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := a.store.ForgetAssistantCallsInRange(ctx, scope.StartMs, scope.EndMs); err != nil {
			log.Printf("[assistant-calls] entries for a withdrawn stretch were not removed: %v", err)
		}
	case privacyWithdrawalSource:
		switch scope.SourceKind {
		case "activity_session", "session":
			a.forgetAssistantCallsForSession(scope.ID)
		case "brief":
			a.forgetAssistantCall(storage.AssistantCallBrief, scope.ID)
		default:
			if kind, ok := withdrawalSourceKind(scope.SourceKind); ok {
				a.forgetAssistantCallsForSource(kind, scope.ID)
			}
		}
	}
}

// recordLearningRead keeps a learning entry naming what a pass read, for a
// pass whose model call runs on a context the scope can't reach.
func (a *App) recordLearningRead(group string, sources ...storage.AssistantCallSource) {
	_, scope, _ := withLearningScope(context.Background(), group)
	for _, src := range sources {
		scope.add(src.Kind, src.Ref, src.Count)
	}
	scope.called()
	a.finishAssistantCall(scope)
}

// learningGroupForCall is the See them group of a call outside any scope,
// from the code that made it.
func learningGroupForCall(label, kind string) string {
	l := strings.ToLower(label)
	switch {
	case strings.Contains(l, "compose"):
		return learningGroupWork
	case strings.Contains(l, "suggestion"), strings.Contains(l, "chip"), strings.Contains(l, "recommended"):
		return learningGroupOther
	case strings.Contains(l, "sop"), strings.Contains(l, "playbook"):
		return learningGroupAbout
	}
	switch kind {
	case "activity":
		return learningGroupSense
	case "memory", "knowledge", "people", "goals":
		return learningGroupAbout
	default:
		return learningGroupOther
	}
}

// The passes that read summaries Isoform already wrote, not single moments
// (design T14). Each is one See them row for the week, by name, in Learning
// about you. They name no item, so they go with Forget everything and age
// out with the summaries they read.
const (
	passPurpose   = "purpose"
	passProfile   = "work_profile"
	passQuestions = "questions"
	passSelfDream = "self_dream"
	passWeekly    = "weekly"
	passRecent    = "recent"
)

// learningPasses lists them in See them's order.
var learningPasses = []string{passPurpose, passProfile, passQuestions, passSelfDream, passWeekly, passRecent}

func validLearningPass(key string) bool {
	for _, p := range learningPasses {
		if p == key {
			return true
		}
	}
	return false
}

// withPassScope opens (or joins) the scope for one summary-reading pass.
func withPassScope(ctx context.Context, pass string) (context.Context, *assistantCallScope) {
	if !validLearningPass(pass) {
		pass = passRecent
	}
	hour := time.Now().Truncate(time.Hour)
	ctx, scope, _ := withAssistantCallScope(ctx, storage.AssistantCallLearning, learningRef("pass_"+pass, hour))
	scope.span(hour.UnixMilli(), hour.Add(time.Hour).UnixMilli())
	return ctx, scope
}

// learningPassOf reads a pass entry's name back from its ref, or "".
func learningPassOf(ref string) string {
	parts := strings.Split(ref, ":")
	if len(parts) == 3 && parts[0] == "learning" && strings.HasPrefix(parts[1], "pass_") {
		if key := strings.TrimPrefix(parts[1], "pass_"); validLearningPass(key) {
			return key
		}
	}
	return ""
}

// passScopeFresh is withPassScope on a fresh context, for a pass whose
// model call already ran on context.Background.
func passScopeFresh(pass string) (context.Context, *assistantCallScope) {
	return withPassScope(context.Background(), pass)
}
