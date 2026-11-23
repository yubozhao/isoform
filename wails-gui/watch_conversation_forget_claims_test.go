package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

func indexWatchClaims(t *testing.T, a *App) {
	t.Helper()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewMemoryStore(filepath.Dir(dir), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	a.memoryStore = store
	t.Cleanup(func() { store.Close() })
	if err := store.Sync("fixture"); err != nil {
		t.Fatal(err)
	}
}
func writeWatchClaim(t *testing.T, id, content string) string {
	t.Helper()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestWatchConversationClaimsSelective(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	session := "rec-" + f.recording
	row, err := f.app.activity.store.GetSessionByID(session)
	if err != nil {
		t.Fatal(err)
	}
	group := sessionGroup{Sessions: []ActivitySessionRow{*row}}
	triager := ActivityTriager{app: f.app}
	id, err := triager.saveActivityMemoryContext(context.Background(), *row, group, &mcMemoryGenerationResponse{Title: "Friday availability", Dimension: "user_insight", MemoryPoints: []string{"Friday works for me"}, WatchSpeechQuotes: []string{"Friday works for me"}})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.app.GetMemoryEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := parseOKFDocument(entry.Content)
	links := okfStringSlice(doc.Frontmatter, watchEvidenceKey)
	if len(links) != 1 {
		t.Fatal(links)
	}
	partial := renderOKFDocument(map[string]any{"type": "memory", "source": "user_edit", "confirmed_at": "2026-01-01T00:00:00Z", "merged_from": []string{id}, "merged_from_refs": []string{"memory:" + id}, "evidence_sessions": []string{session, "other"}, watchEvidenceKey: links}, "# A shared claim\n\nThe claim.")
	writeWatchClaim(t, "shared", partial)
	neighbor, err := triager.linkWatchMemory(context.Background(), renderOKFDocument(map[string]any{"type": "memory", "evidence_sessions": []string{session}}, "# Neighbor"), "", group, []string{"neighbor's private words"})
	if err != nil {
		t.Fatal(err)
	}
	writeWatchClaim(t, "neighbor", neighbor)
	indexWatchClaims(t, f.app)
	self := seedMemoryForgetSelf(t, f.app.activity.store, id, "A derived preference")
	goal := mobileTestPurpose(t, NewPurposeMapStore(f.app.activity.store.db), "pricing-topic", PurposeKindGoal, "Source topic", false, func(in *CreatePurposeOpenLoopInput) {
		in.Evidence[0].SourceKind = "memory_markdown"
		in.Evidence[0].SourceID = id
	})
	_, err = f.app.topicsSvc().store.update(func(s *topicsStateFile) {
		s.Projects = []topicProject{{ID: "project", Name: "Source topic", GoalIDs: []string{goal.OpenLoop.ID}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.app.GetWatchConversationForgetPreview(f.target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Claims) != 1 || p.Claims[0].ID != id || len(p.ClaimsLosingSupport) != 1 || p.ClaimsLosingSupport[0].ID != "shared" || p.Count != 4+len(p.ClaimDerivatives) {
		t.Fatalf("%+v", p)
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.GetMemoryEntry(id); !os.IsNotExist(err) {
		t.Fatal("erased claim", err)
	}
	if _, err := f.app.activity.store.GetSelfModelEntryByID(self.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("derivative", err)
	}
	state := f.app.topicsSvc().store.load()
	if len(state.Projects) != 1 || state.Projects[0].Name != "" || len(state.Projects[0].GoalIDs) > 0 {
		t.Fatal(state.Projects)
	}
	shared, err := f.app.GetMemoryEntry("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(memoryDerivedSourceIDs(shared.Content)) > 0 || len(memoryDerivedSourceRefs(shared.Content)) > 0 || !slices.Equal(memoryEvidenceSessionIDs(shared.Content), []string{"other"}) || strings.Contains(shared.Content, session) || memoryConfirmedAt(shared.Content) != memoryConfirmedAt(partial) || !isUserEditedMemory(shared.Content) {
		t.Fatal(shared.Content)
	}
	kept, err := f.app.GetMemoryEntry("neighbor")
	if err != nil || kept.Content != neighbor {
		t.Fatal(kept, err)
	}
	for _, table := range []string{"memory_chunks", "memory_chunks_fts", "memory_files"} {
		var n int
		if err := f.app.memoryStore.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE path=?", id+".md").Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	var text string
	if err := f.app.memoryStore.db.QueryRow("SELECT text FROM memory_chunks WHERE path='shared.md'").Scan(&text); err != nil || strings.Contains(text, session) || !strings.Contains(text, "other") {
		t.Fatal(text, err)
	}
}
func TestWatchConversationClaimsRecovery(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before receipt", true: "after receipt"}[after], func(t *testing.T) {
			t.Setenv(agentHomeEnv, t.TempDir())
			a, _, s, id := watchDeskFixture(t)
			cs, err := a.GetWatchConversations()
			if err != nil || len(cs) != 1 {
				t.Fatal(cs, err)
			}
			rec := s.recordings[id]
			audio := s.partPaths(*rec)[0]
			raw, err := os.ReadFile(audio)
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := storage.New(filepath.Join(t.TempDir(), "sessions.db"))
			if err != nil {
				t.Fatal(err)
			}
			a.store = sessions
			t.Cleanup(func() { sessions.Close() })
			content := renderOKFDocument(map[string]any{"type": "memory", "evidence_sessions": []string{rec.SessionID}}, "# Sole claim")
			path := writeWatchClaim(t, "claim", content)
			indexWatchClaims(t, a)
			self := seedMemoryForgetSelf(t, a.activity.store, "claim", "Derived from the watch conversation")
			notes := map[string]watchConversationNotesEntry{cs[0].ID: {Hash: watchConversationHash(cs[0]), Summary: "The watch note", RecordingIDs: cs[0].RecordingIDs}}
			if err := writeWatchConversationNotes(s.dir, notes); err != nil {
				t.Fatal(err)
			}
			sessions.RecordContextUses("memory", []string{"claim"}, "chat", "")
			deadline := time.Now().Add(3 * time.Second)
			for {
				uses, err := sessions.EntryUses(t.Context(), "memory", "claim")
				if err != nil {
					t.Fatal(err)
				}
				if uses.Total == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("missing use in fixture")
				}
				time.Sleep(time.Millisecond)
			}
			p, err := a.watchForgetPlan(cs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			err = a.commitWatchCut(s, p, func() bool { n++; return !after || n == 2 })
			if !errors.Is(err, errWatchCutInterrupted) {
				t.Fatal(err)
			}
			var seq int
			var name, activityPath string
			if err := a.activity.store.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &activityPath); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YANSU_MOBILE_STATE_DIR", filepath.Dir(s.dir))
			if err := recoverStartupWatchCut(filepath.Dir(activityPath), sessions); err != nil {
				t.Fatal(err)
			}
			reopened := newMobileRecordingStore(filepath.Dir(s.dir))
			reopened.cutDB = a.activity.store.db
			entries, err := reopened.all()
			if err != nil {
				t.Fatal(err)
			}
			_, selfErr := a.activity.store.GetSelfModelEntryByID(self.ID)
			if after && !errors.Is(selfErr, sql.ErrNoRows) || !after && selfErr != nil {
				t.Fatal(selfErr)
			}
			keptNotes, err := readWatchForgetIndex[watchConversationNotesEntry](s.dir, watchConversationNotesFile)
			if err != nil {
				t.Fatal(err)
			}
			if after && len(keptNotes) != 0 || !after && !reflect.DeepEqual(keptNotes, notes) {
				t.Fatal(keptNotes)
			}
			uses, err := sessions.EntryUses(t.Context(), "memory", "claim")
			if err != nil || after && uses.Total != 0 || !after && uses.Total != 1 {
				t.Fatal(uses, err)
			}
			var words int
			if err := a.activity.store.db.QueryRow("SELECT count(*) FROM activity_events WHERE session_id=?", rec.SessionID).Scan(&words); err != nil || after && words != 0 || !after && words != 2 {
				t.Fatal(words, err)
			}
			got, err := os.ReadFile(path)
			sound, soundErr := os.ReadFile(audio)
			var count int
			if err := a.memoryStore.db.QueryRow("SELECT count(*) FROM memory_files WHERE path='claim.md'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			dir, _ := memoryDir()
			suppressed, e := isMemoryForgotten(dir, "claim", content, time.Now())
			if e != nil || suppressed != after {
				t.Fatal(suppressed, e)
			}
			if after {
				if len(entries) != 0 || !os.IsNotExist(err) || !os.IsNotExist(soundErr) || count != 0 {
					t.Fatal(entries, err, soundErr, count)
				}
			} else if len(entries) != 1 || err != nil || string(got) != content || soundErr != nil || !bytes.Equal(raw, sound) || count != 1 {
				t.Fatal(entries, err, soundErr, count)
			}
		})
	}
}
func TestWatchConversationSameHourClaimCanBeLearnedFromNeighbor(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	session := "rec-" + f.recording
	row, err := f.app.activity.store.GetSessionByID(session)
	if err != nil {
		t.Fatal(err)
	}
	triager := ActivityTriager{app: f.app}
	group := sessionGroup{Sessions: []ActivitySessionRow{*row}}
	memory := mcMemoryGenerationResponse{Title: "Availability", Dimension: "user_insight", MemoryPoints: []string{"Available on Friday"}}
	id, err := triager.saveActivityMemoryContext(t.Context(), *row, group, &memory)
	if err != nil {
		t.Fatal(err)
	}
	neighbor, err := triager.linkWatchMemory(t.Context(), renderOKFDocument(map[string]any{"type": "memory", "evidence_sessions": []string{session}}, "# Neighbor"), "", group, []string{"neighbor's private words"})
	if err != nil {
		t.Fatal(err)
	}
	writeWatchClaim(t, "neighbor", neighbor)
	p, err := f.app.GetWatchConversationForgetPreview(f.target.ID)
	if err != nil || len(p.SameHourClaims) != 1 || p.SameHourClaims[0].ID != id {
		t.Fatal(p, err)
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.GetMemoryEntry(id); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	kept, err := f.app.GetMemoryEntry("neighbor")
	if err != nil || kept.Content != neighbor {
		t.Fatal(kept, err)
	}
	memory.WatchSpeechQuotes = []string{"neighbor's private words"}
	again, err := triager.saveActivityMemoryContext(t.Context(), *row, group, &memory)
	if err != nil || again != id {
		t.Fatalf("relearned=%q err=%v", again, err)
	}
}

func TestWatchConversationClaimKeepsUnmatchedQuoteSupport(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	session := "rec-" + f.recording
	row, err := f.app.activity.store.GetSessionByID(session)
	if err != nil {
		t.Fatal(err)
	}
	triager := ActivityTriager{app: f.app}
	id, err := triager.saveActivityMemoryContext(t.Context(), *row, sessionGroup{Sessions: []ActivitySessionRow{*row}}, &mcMemoryGenerationResponse{Title: "Friday availability", Dimension: "user_insight", MemoryPoints: []string{"Available on Friday"}, WatchSpeechQuotes: []string{"neighbor's private words", "no matching speech"}})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.app.GetMemoryEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := parseOKFDocument(entry.Content)
	links := okfStringSlice(doc.Frontmatter, watchEvidenceKey)
	if len(links) != 1 || !slices.Equal(okfStringSlice(doc.Frontmatter, watchHourEvidenceKey), []string{session}) {
		t.Fatal(doc.Frontmatter)
	}
	p, err := f.app.GetWatchConversationForgetPreview(f.target.ID)
	if err != nil || len(p.ClaimsLosingSupport) != 1 || p.ClaimsLosingSupport[0].ID != id || len(p.SameHourClaims) != 0 {
		t.Fatal(p, err)
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err != nil {
		t.Fatal(err)
	}
	entry, err = f.app.GetMemoryEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = parseOKFDocument(entry.Content)
	if !slices.Equal(okfStringSlice(doc.Frontmatter, watchEvidenceKey), links) || len(okfStringSlice(doc.Frontmatter, watchHourEvidenceKey)) != 0 {
		t.Fatal(doc.Frontmatter)
	}
}
func TestWatchConversationClaimsIndexFailureRestoresCut(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	a, _, s, id := watchDeskFixture(t)
	cs, err := a.GetWatchConversations()
	if err != nil || len(cs) != 1 {
		t.Fatal(cs, err)
	}
	r := s.recordings[id]
	audio := s.partPaths(*r)[0]
	before, err := os.ReadFile(audio)
	if err != nil {
		t.Fatal(err)
	}
	content := renderOKFDocument(map[string]any{"type": "memory", "evidence_sessions": []string{r.SessionID}}, "# Claim")
	path := writeWatchClaim(t, "claim", content)
	indexWatchClaims(t, a)
	if _, err := a.memoryStore.db.Exec("CREATE TRIGGER keep_chunks BEFORE DELETE ON memory_chunks BEGIN SELECT RAISE(ABORT,'index failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetWatchConversation(cs[0].ID); err == nil || !strings.Contains(err.Error(), "index failure") {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatal(err)
	}
	sound, err := os.ReadFile(audio)
	if err != nil || !bytes.Equal(sound, before) {
		t.Fatal(err)
	}
	now, err := a.GetWatchConversations()
	if err != nil || !reflect.DeepEqual(now, cs) {
		t.Fatal(now, err)
	}
	dir, _ := memoryDir()
	if _, err := os.Stat(filepath.Join(dir, memoryTombstonesFile)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestWatchConversationSameHourClaimRecovery(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before receipt", true: "after receipt"}[after], func(t *testing.T) {
			f := newWatchForgetFixture(t, false)
			session := "rec-" + f.recording
			raw := renderOKFDocument(map[string]any{"type": "memory", "evidence_sessions": []string{session}}, "# From this hour")
			path := writeWatchClaim(t, "same-hour", raw)
			indexWatchClaims(t, f.app)
			plan, err := f.app.watchForgetPlan(f.target.ID)
			if err != nil || len(plan.preview.SameHourClaims) != 1 {
				t.Fatal(plan, err)
			}
			n := 0
			if err := f.app.commitWatchCut(f.store, plan, func() bool { n++; return !after || n == 2 }); !errors.Is(err, errWatchCutInterrupted) {
				t.Fatal(err)
			}
			// Trigger recording-store recovery before inspecting either store.
			cs, err := f.app.GetWatchConversations()
			if err != nil {
				t.Fatal(err)
			}
			var indexed bool
			if err := f.app.memoryStore.db.QueryRow("SELECT EXISTS(SELECT 1 FROM memory_files WHERE path='same-hour.md')").Scan(&indexed); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if after {
				if !os.IsNotExist(err) || indexed || !reflect.DeepEqual(cs, []WatchConversation{f.neighbor}) {
					t.Fatal(cs, indexed, err)
				}
			} else if err != nil || string(got) != raw || !indexed || len(cs) != 2 {
				t.Fatal(cs, indexed, err)
			}
		})
	}
}
