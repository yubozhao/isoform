package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/mailheaders"
	"wails-gui/storage"
)

// fakeMail plays Mail.app: whether it runs, what macOS says about
// automating it, and what the header script prints.
type fakeMail struct {
	running bool
	state   mailAutomationState
	out     string
	err     error
	runs    int
	scripts []string
}

func (f *fakeMail) run(ctx context.Context, script string) ([]byte, error) {
	f.runs++
	f.scripts = append(f.scripts, script)
	return []byte(f.out), f.err
}

// mailTestPID is the process id the fake Mail runs as.
const mailTestPID = 4242

type mailTestService struct {
	*mailSyncService
	mail     *fakeMail
	enabled  *bool
	paused   *bool
	guarded  *bool
	allowed  *string
	pauseLog *[]observationPause
	synced   *int
}

func newTestMailService(t *testing.T, now time.Time, people map[string]string) mailTestService {
	t.Helper()
	st, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mail := &fakeMail{running: true, state: mailAutomationAllowed}
	enabled, paused, guarded := true, false, false
	allowed := ""
	pauseLog := []observationPause{}
	synced := 0
	svc := &mailSyncService{
		store:     st,
		now:       func() time.Time { return now },
		enabled:   func() (bool, bool) { return enabled, true },
		protected: func() bool { return guarded },
		paused:    func() bool { return paused },
		pauses:    func() ([]observationPause, error) { return pauseLog, nil },
		process: func() int {
			if mail.running {
				return mailTestPID
			}
			return 0
		},
		automation: func() mailAutomationState { return mail.state },
		run:        mail.run,
		resolve: func(ctx context.Context, email string) (string, bool) {
			id, ok := people[email]
			return id, ok
		},
		recordAllowed: func(ok bool) {
			if ok {
				allowed = "true"
			} else {
				allowed = "false"
			}
		},
		onSynced: func(mailSyncResult) { synced++ },
		kicks:    make(chan string, 4),
	}
	return mailTestService{svc, mail, &enabled, &paused, &guarded, &allowed, &pauseLog, &synced}
}

type testHeader struct {
	ID      string   `json:"id"`
	From    string   `json:"from"`
	Subject string   `json:"subject"`
	At      int64    `json:"at"`
	Read    bool     `json:"read"`
	Flagged bool     `json:"flagged"`
	Replied bool     `json:"replied"`
	To      []string `json:"to"`
	CC      []string `json:"cc"`
}

func mailSnapshotJSON(t *testing.T, inbox, sent []testHeader) string {
	t.Helper()
	if inbox == nil {
		inbox = []testHeader{}
	}
	if sent == nil {
		sent = []testHeader{}
	}
	// The observer script always serializes recipient lists as arrays.
	for _, box := range [][]testHeader{inbox, sent} {
		for i := range box {
			if box[i].To == nil {
				box[i].To = []string{}
			}
			if box[i].CC == nil {
				box[i].CC = []string{}
			}
		}
	}
	raw, err := json.Marshal(map[string]any{
		"running":  true,
		"complete": true,
		"me":       []string{"me@example.com"},
		"inbox":    inbox,
		"sent":     sent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func storedMail(t *testing.T, st *storage.Store) map[string]storage.MailMessageRow {
	t.Helper()
	rows, err := st.ListMailMessages(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]storage.MailMessageRow{}
	for _, row := range rows {
		out[row.Subject] = row
	}
	return out
}

func TestMailSyncStoresHeadersAndSightings(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	sam, err := ms.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Sam Lee"})
	if err != nil {
		t.Fatal(err)
	}
	ann, err := ms.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Ann"})
	if err != nil {
		t.Fatal(err)
	}
	people := map[string]string{"sam@example.com": sam.ID, "ann@example.com": ann.ID}
	ms.resolve = func(ctx context.Context, email string) (string, bool) { id, ok := people[email]; return id, ok }
	ms.mail.out = mailSnapshotJSON(t, []testHeader{
		{ID: "<1@x>", From: "Sam Lee <sam@example.com>", Subject: "Re: Q3 deck", At: now.Add(-30 * time.Hour).UnixMilli(), To: []string{"me@example.com"}},
		{ID: "<2@x>", From: "Stranger <who@example.org>", Subject: "Your code is 482913", At: now.Add(-2 * time.Hour).UnixMilli(), To: []string{"me@example.com"}},
		{ID: "<3@x>", From: "list@example.com", Subject: "Digest", At: now.Add(-3 * time.Hour).UnixMilli(), To: []string{"team@example.com"}, CC: []string{"me@example.com"}},
		{ID: "<4@x>", From: "old@example.com", Subject: "Too old", At: now.Add(-9 * 24 * time.Hour).UnixMilli(), To: []string{"me@example.com"}},
	}, []testHeader{
		{ID: "<s1@x>", From: "me@example.com", Subject: "Plan", At: now.Add(-time.Hour).UnixMilli(), To: []string{"ann@example.com"}},
	})
	if _, err := ms.Sync(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	rows := storedMail(t, ms.store)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	q3 := rows["Re: Q3 deck"]
	if !q3.ToMe || q3.PrincipalID != sam.ID || q3.SenderEmail != "sam@example.com" || q3.Mailbox != storage.MailboxInbox {
		t.Fatalf("q3 = %+v", q3)
	}
	for subject := range rows {
		if strings.Contains(subject, "482913") {
			t.Fatalf("codes in a subject must be redacted: %q", subject)
		}
	}
	if digest := rows["Digest"]; digest.ToMe {
		t.Fatal("a copy is not a message to the user directly")
	}
	if *ms.allowed != "true" || *ms.synced != 1 {
		t.Fatalf("allowed %q synced %d", *ms.allowed, *ms.synced)
	}
	if !strings.Contains(ms.mail.scripts[0], "mail.running()") {
		t.Fatal("the header script must be what runs")
	}
	if !strings.Contains(ms.mail.scripts[0], "Application(4242)") {
		t.Fatal("the read must address the Mail process it found running")
	}
	for _, id := range []string{sam.ID, ann.ID} {
		seen, err := ms.store.PrincipalLastSeenAt(ctx, id)
		if err != nil || seen == 0 {
			t.Fatalf("a known sender and a known recipient get a sighting: %d, %v", seen, err)
		}
	}
	// A second read of the same messages adds no sightings and creates no
	// one new.
	if _, err := ms.Sync(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	principals, err := ms.store.ListPrincipals(ctx)
	if err != nil || len(principals) != 2 {
		t.Fatalf("nobody new is created: %d, %v", len(principals), err)
	}
}

func TestMailSyncNeverOpensMailOrAsks(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	ms := newTestMailService(t, now, nil)
	ms.mail.out = mailSnapshotJSON(t, nil, nil)

	ms.mail.running = false
	ms.syncIfAllowed(ctx, "closed")
	if ms.mail.runs != 0 {
		t.Fatal("Mail closed: nothing may be sent to it")
	}
	if _, _, notRunning := ms.status(); !notRunning {
		t.Fatal("the card must learn Mail is closed")
	}

	ms.mail.running = true
	ms.mail.state = mailAutomationUndecided
	ms.syncIfAllowed(ctx, "undecided")
	if ms.mail.runs != 0 {
		t.Fatal("an undecided permission must never be asked by a read")
	}

	ms.mail.state = mailAutomationAllowed
	*ms.paused = true
	ms.syncIfAllowed(ctx, "paused")
	if ms.mail.runs != 0 {
		t.Fatal("nothing is read while observing is paused")
	}
}

func TestMailSyncPausesWhenStoppedOrRefusedAndClearsWhenProtected(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	ms.mail.out = mailSnapshotJSON(t, []testHeader{
		{ID: "<1@x>", From: "sam@example.com", Subject: "Hello", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
	}, nil)
	refill := func() {
		t.Helper()
		if _, err := ms.Sync(ctx, "fill"); err != nil {
			t.Fatal(err)
		}
		if len(storedMail(t, ms.store)) != 1 {
			t.Fatal("expected one stored message")
		}
	}

	refill()
	*ms.enabled = false
	ms.syncIfAllowed(ctx, "off")
	if len(storedMail(t, ms.store)) != 1 {
		t.Fatal("switching off must retain the headers")
	}
	*ms.enabled = true

	refill()
	*ms.guarded = true
	ms.syncIfAllowed(ctx, "protected")
	if len(storedMail(t, ms.store)) != 0 {
		t.Fatal("protecting Mail must delete the headers")
	}
	*ms.guarded = false

	refill()
	ms.mail.state = mailAutomationDenied
	if _, err := ms.Sync(ctx, "denied"); !errors.Is(err, errMailNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if len(storedMail(t, ms.store)) != 1 || *ms.allowed != "false" {
		t.Fatal("a refusal must retain the headers and be remembered")
	}

	ms.mail.state = mailAutomationAllowed
	refill()
	ms.mail.err = errMailNotAllowed
	if _, err := ms.Sync(ctx, "refused by osascript"); !errors.Is(err, errMailNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if len(storedMail(t, ms.store)) != 1 {
		t.Fatal("a refusal seen by the script must retain the headers too")
	}
}

func TestMailSyncOffRetainsDataWhenAccessIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   mailAutomationState
		refused bool
	}{
		{"allowed", mailAutomationAllowed, false},
		{"allowed-after-denial", mailAutomationAllowed, true},
		{"not-requested", mailAutomationUndecided, true},
		{"unknown", mailAutomationUnknown, false},
		{"denied", mailAutomationDenied, false},
		{"remembered-denial", mailAutomationUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().Truncate(time.Second)
			ms := newTestMailService(t, now, nil)
			ms.mail.out = mailSnapshotJSON(t, []testHeader{
				{ID: "<1@x>", From: "sam@example.com", Subject: "Keep this", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
			}, nil)
			if _, err := ms.Sync(ctx, "fill"); err != nil {
				t.Fatal(err)
			}
			*ms.enabled = false
			ms.mail.state = tc.state
			if tc.refused {
				*ms.allowed = "false"
			}
			assertRetained := mailRetainedDataCheck(t, ms.store)
			runs := ms.mail.runs
			ms.syncIfAllowed(ctx, "off")
			assertRetained()
			if ms.mail.runs != runs {
				t.Fatal("switching off must not run a mail read")
			}
			if tc.state == mailAutomationAllowed && *ms.allowed != "true" {
				t.Fatal("a current grant must replace a cached denial")
			}
		})
	}
}

// mailRetainedDataCheck captures saved headers and adds a waiting-on-reply
// moment so refusal tests cover both the source data and the derived words.
func mailRetainedDataCheck(t *testing.T, st *storage.Store) func() {
	t.Helper()
	ctx := context.Background()
	before := storedMail(t, st)
	const key = "waiting_on_reply:retention:thread"
	const params = `{"subject":"Keep this","name":"Sam"}`
	if _, err := st.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(engagement.KindWaitingOnReply), DedupeKey: key, Params: params,
	}); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if after := storedMail(t, st); !reflect.DeepEqual(after, before) {
			t.Fatalf("saved headers changed: before %+v, after %+v", before, after)
		}
		moment, err := st.GetOpenEngagementMomentByKey(ctx, key)
		if err != nil || moment == nil || moment.Params != params {
			t.Fatalf("saved waiting-on-reply moment changed: %+v, %v", moment, err)
		}
	}
}

func TestMailSyncRefusalRetainsHeadersAndWaitingMoments(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     mailAutomationState
		running   bool
		scriptErr error
		wantErr   error
		wantReads int
	}{
		{"permission-refused", mailAutomationDenied, true, nil, errMailNotAllowed, 0},
		{"script-refused", mailAutomationAllowed, true, errMailNotAllowed, errMailNotAllowed, 1},
		{"closed-after-refusal", mailAutomationUnknown, false, nil, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().Truncate(time.Second)
			ms := newTestMailService(t, now, nil)
			ms.mail.out = mailSnapshotJSON(t, []testHeader{
				{ID: "<kept@x>", From: "sam@example.com", Subject: "Keep this", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
			}, nil)
			if _, err := ms.Sync(ctx, "fill"); err != nil {
				t.Fatal(err)
			}
			assertRetained := mailRetainedDataCheck(t, ms.store)
			ms.mail.state, ms.mail.running, ms.mail.err = tc.state, tc.running, tc.scriptErr
			*ms.allowed = "false"
			runs := ms.mail.runs
			if _, err := ms.Sync(ctx, "refused"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Sync error = %v, want %v", err, tc.wantErr)
			}
			assertRetained()
			if got := ms.mail.runs - runs; got != tc.wantReads {
				t.Fatalf("header reads = %d, want %d", got, tc.wantReads)
			}
			if *ms.allowed != "false" {
				t.Fatal("the last refusal must remain recorded")
			}
		})
	}
}

func TestMailSyncLeavesOutPausesAndForgetsWhatLeftTheInbox(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	*ms.pauseLog = []observationPause{{Start: now.Add(-3 * time.Hour).UnixMilli(), End: now.Add(-2 * time.Hour).UnixMilli()}}
	keep := testHeader{ID: "<1@x>", From: "sam@example.com", Subject: "Kept", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}}
	archived := testHeader{ID: "<2@x>", From: "ann@example.com", Subject: "Archived later", At: now.Add(-5 * time.Hour).UnixMilli(), To: []string{"me@example.com"}}
	inPause := testHeader{ID: "<3@x>", From: "bob@example.com", Subject: "In a pause", At: now.Add(-150 * time.Minute).UnixMilli(), To: []string{"me@example.com"}}
	sentInPause := testHeader{ID: "<s@x>", From: "me@example.com", Subject: "Sent in a pause", At: now.Add(-140 * time.Minute).UnixMilli(), To: []string{"bob@example.com"}}
	ms.mail.out = mailSnapshotJSON(t, []testHeader{keep, archived, inPause}, []testHeader{sentInPause})
	if _, err := ms.Sync(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	rows := storedMail(t, ms.store)
	if len(rows) != 2 {
		t.Fatalf("nothing received or sent inside a pause is kept: %v", rows)
	}
	// Archived in Mail: gone at the next read.
	ms.mail.out = mailSnapshotJSON(t, []testHeader{keep}, nil)
	if _, err := ms.Sync(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if rows := storedMail(t, ms.store); len(rows) != 1 {
		t.Fatalf("a message that left the inbox must go: %v", rows)
	}
	// One empty answer is not trusted; two are.
	ms.mail.out = mailSnapshotJSON(t, nil, nil)
	if _, err := ms.Sync(ctx, "empty once"); err != nil {
		t.Fatal(err)
	}
	if rows := storedMail(t, ms.store); len(rows) != 1 {
		t.Fatal("a single empty read must keep the headers")
	}
	if _, err := ms.Sync(ctx, "empty twice"); err != nil {
		t.Fatal(err)
	}
	if rows := storedMail(t, ms.store); len(rows) != 0 {
		t.Fatal("two empty reads in a row mean the inbox is empty")
	}
}

func TestMailWaitingOnReply(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	q3 := mailThreadKey("Q3 deck")
	inbox := func(id, from, subject string, ago time.Duration, mod func(*storage.MailMessageRow)) storage.MailMessageRow {
		row := storage.MailMessageRow{ID: id, Mailbox: storage.MailboxInbox, SenderEmail: from, SenderName: "Name",
			ToMe: true, Subject: subject, ThreadKey: mailThreadKey(subject), At: at(ago), PrincipalID: "p-" + from}
		if mod != nil {
			mod(&row)
		}
		return row
	}
	sent := func(id, to, subject string, ago time.Duration) storage.MailMessageRow {
		return storage.MailMessageRow{ID: id, Mailbox: storage.MailboxSent, SenderEmail: "me@example.com", To: to,
			Subject: subject, ThreadKey: mailThreadKey(subject), At: at(ago)}
	}
	rows := []storage.MailMessageRow{
		// Waiting: Sam asked 30 hours ago, two messages in the thread.
		inbox("a1", "sam@example.com", "Q3 deck", 40*time.Hour, nil),
		inbox("a2", "sam@example.com", "Re: Q3 deck", 30*time.Hour, nil),
		// Too fresh.
		inbox("b", "ann@example.com", "Lunch", 3*time.Hour, nil),
		// Answered by a sent message to Ann after it.
		inbox("c", "ann@example.com", "Budget", 48*time.Hour, nil),
		sent("cs", "ann@example.com", "Re: Budget", 47*time.Hour),
		// A sent message in the same thread but to someone else does not count.
		inbox("d", "bob@example.com", "Offsite", 50*time.Hour, nil),
		sent("ds", "carol@example.com", "Re: Offsite", 45*time.Hour),
		// Marked replied in Mail.
		inbox("e", "dan@example.com", "Contract", 50*time.Hour, func(r *storage.MailMessageRow) { r.Replied = true }),
		// Unknown sender.
		inbox("f", "who@example.org", "Cold pitch", 50*time.Hour, func(r *storage.MailMessageRow) { r.PrincipalID = "" }),
		// Only copied.
		inbox("g", "eve@example.com", "FYI", 50*time.Hour, func(r *storage.MailMessageRow) { r.ToMe = false }),
		// No subject.
		inbox("h", "fay@example.com", "", 50*time.Hour, nil),
		// Read but not answered still waits.
		inbox("i", "gus@example.com", "Invoice", 26*time.Hour, func(r *storage.MailMessageRow) { r.Read = true }),
		// A reply sent before the latest message does not answer it.
		inbox("j1", "hal@example.com", "Hiring", 60*time.Hour, nil),
		sent("js", "hal@example.com", "Re: Hiring", 55*time.Hour),
		inbox("j2", "hal@example.com", "Re: Hiring", 30*time.Hour, nil),
	}
	got := mailWaitingOnReply(rows, now)
	bySender := map[string]mailWaitingThread{}
	for _, thread := range got {
		bySender[thread.PrincipalID] = thread
	}
	if len(got) != 4 {
		t.Fatalf("waiting = %+v", got)
	}
	samThread := bySender["p-sam@example.com"]
	if samThread.ThreadKey != q3 || samThread.Subject != "Q3 deck" || !samThread.At.Equal(time.UnixMilli(at(30*time.Hour))) {
		t.Fatalf("one moment per thread, by its latest message: %+v", samThread)
	}
	for _, want := range []string{"p-bob@example.com", "p-gus@example.com", "p-hal@example.com"} {
		if _, ok := bySender[want]; !ok {
			t.Fatalf("%s should be waiting: %+v", want, got)
		}
	}
	moment := mailWaitingMoment(samThread, "Sam", now)
	if moment.Kind != engagement.KindWaitingOnReply || moment.DedupeKey != "waiting_on_reply:p-sam@example.com:"+q3 ||
		moment.Params["name"] != "Sam" || moment.Params["subject"] != "Q3 deck" {
		t.Fatalf("moment = %+v", moment)
	}
	set, ok := engagement.Copy(engagement.KindWaitingOnReply, "en")
	if !ok || engagement.Render(set.Sealed, moment.Params) != "Sam is waiting on your reply." {
		t.Fatalf("copy = %q", engagement.Render(set.Sealed, moment.Params))
	}
	if spec, _ := engagement.Spec(engagement.KindWaitingOnReply); !spec.Digestable ||
		spec.Interruption(moment, now) != engagement.InterruptionPassive || spec.Cooldown != 0 {
		t.Fatalf("waiting on reply is passive, digestable and said once per thread: %+v", spec)
	}
}

func TestMailScriptOutputMapsThroughTheReader(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	snap, err := mailheaders.Parse([]byte(`{"running":true,"me":["Me@Example.com"],
		"inbox":[{"id":"<1@x>","from":"Me <me@example.com>","subject":"Note to self","at":` +
		jsonInt(now.Add(-time.Hour).UnixMilli()) + `,"to":["me@example.com"]}],"sent":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := mailRows(snap, nil, now.Add(-7*24*time.Hour), now)
	if len(inbox) != 1 || inbox[0].ToMe {
		t.Fatalf("a note to self never waits on a reply: %+v", inbox)
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestMailWaitingReplyGenericCopyDoesNotReachTheMainChatPrompt(t *testing.T) {
	app := mainChatTestApp(t)
	set, _ := engagement.Copy(engagement.KindWaitingOnReply, "en")
	params := map[string]string{"name": "Sam", "subject": "Severance terms"}
	app.postEngagementReport(engagement.Moment{Kind: engagement.KindWaitingOnReply, DedupeKey: "waiting_on_reply:p:t"},
		engagementText{Title: engagement.Render(set.Sealed, params), PushBody: set.PushBody})
	reports := mainChatStructuredRows(t, app, mainChatProactiveTool)
	if len(reports) != 0 {
		t.Fatalf("generic-only mail card reached the main chat prompt: %+v", reports)
	}
}

func TestMailProtectedForgetsWaitingMoments(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	ms.mail.out = mailSnapshotJSON(t, []testHeader{
		{ID: "<1@x>", From: "sam@example.com", Subject: "Hello", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
	}, nil)
	if _, err := ms.Sync(ctx, "fill"); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(engagement.KindWaitingOnReply), DedupeKey: "waiting_on_reply:p:t", Params: `{"subject":"Hello"}`,
	}); err != nil {
		t.Fatal(err)
	}
	*ms.guarded = true
	ms.syncIfAllowed(ctx, "protected")
	if len(storedMail(t, ms.store)) != 0 {
		t.Fatal("explicitly protecting Mail must delete the headers")
	}
	// The moment keeps its key, so reading again later does not announce
	// the conversation a second time, but loses its words and is never
	// delivered.
	if seen, _ := ms.store.EngagementMomentKeyExists(ctx, "waiting_on_reply:p:t"); !seen {
		t.Fatal("clearing must keep the moment's key")
	}
	if open, _ := ms.store.GetOpenEngagementMomentByKey(ctx, "waiting_on_reply:p:t"); open != nil {
		t.Fatalf("a cleared moment must not be delivered: %+v", open)
	}
	moments, err := ms.store.ListEngagementMoments(ctx, storage.EngagementStateSuppressed, 0)
	if err != nil || len(moments) != 1 || moments[0].Params != "{}" || strings.Contains(moments[0].Params, "Hello") {
		t.Fatalf("clearing must forget the subject the moment quoted: %+v, %v", moments, err)
	}
	if ms.mail.runs != 1 {
		t.Fatal("nothing may be sent to a protected Mail")
	}
}

func TestMailEmptyMailboxesCountOnTheirOwn(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	in := testHeader{ID: "<1@x>", From: "sam@example.com", Subject: "Kept", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}}
	out := testHeader{ID: "<s@x>", From: "me@example.com", Subject: "Sent", At: now.Add(-time.Hour).UnixMilli(), To: []string{"ann@example.com"}}
	ms.mail.out = mailSnapshotJSON(t, []testHeader{in}, []testHeader{out})
	if _, err := ms.Sync(ctx, "fill"); err != nil {
		t.Fatal(err)
	}
	// The inbox answers empty once while the sent mailbox does not.
	ms.mail.out = mailSnapshotJSON(t, nil, []testHeader{out})
	if _, err := ms.Sync(ctx, "inbox loading"); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedMail(t, ms.store)["Kept"]; !ok {
		t.Fatal("one empty inbox read must not delete the inbox while the sent mailbox answered")
	}
}

func TestMailWaitingKeepsPeopleApartAndFindsTheUserWithoutAccounts(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	key := mailThreadKey("Quick question")
	rows := []storage.MailMessageRow{
		{ID: "a", Mailbox: storage.MailboxInbox, SenderEmail: "sam@example.com", ToMe: true, Subject: "Quick question",
			ThreadKey: key, At: now.Add(-30 * time.Hour).UnixMilli(), PrincipalID: "p-sam"},
		{ID: "b", Mailbox: storage.MailboxInbox, SenderEmail: "ann@example.com", ToMe: true, Subject: "Quick question",
			ThreadKey: key, At: now.Add(-28 * time.Hour).UnixMilli(), PrincipalID: "p-ann", Replied: true},
	}
	got := mailWaitingOnReply(rows, now)
	if len(got) != 1 || got[0].PrincipalID != "p-sam" {
		t.Fatalf("Ann's answered message must not hide Sam's waiting one: %+v", got)
	}
	if mailThreadKey("Invoice 1234") == mailThreadKey("Invoice 5678") {
		t.Fatal("two invoices are two conversations")
	}

	// Mail gave no account addresses: the sent mailbox tells who the user is.
	snap, err := mailheaders.Parse([]byte(`{"running":true,"me":[],
		"inbox":[{"id":"<1@x>","from":"Sam <sam@example.com>","subject":"Q3","at":` + jsonInt(now.Add(-time.Hour).UnixMilli()) + `,"to":["me@example.com"]}],
		"sent":[{"id":"<s@x>","from":"Me <me@example.com>","subject":"Other","at":` + jsonInt(now.Add(-2*time.Hour).UnixMilli()) + `,"to":["ann@example.com"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := mailRows(snap, nil, now.Add(-7*24*time.Hour), now)
	if len(inbox) != 1 || !inbox[0].ToMe {
		t.Fatalf("inbox = %+v", inbox)
	}
}

func TestMailQuitDuringReadIsMailClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	ms.mail.out = mailSnapshotJSON(t, []testHeader{
		{ID: "<1@x>", From: "sam@example.com", Subject: "Hello", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
	}, nil)
	if _, err := ms.Sync(ctx, "fill"); err != nil {
		t.Fatal(err)
	}
	ms.mail.err = errMailClosed
	if _, err := ms.Sync(ctx, "quit meanwhile"); err != nil {
		t.Fatalf("Mail quitting is not an error: %v", err)
	}
	if _, lastErr, notRunning := ms.status(); !notRunning || lastErr != "" {
		t.Fatalf("the card must learn Mail is closed: %q %v", lastErr, notRunning)
	}
	if len(storedMail(t, ms.store)) != 1 {
		t.Fatal("Mail quitting keeps what was read")
	}
}

func TestMailBacksOffAfterSlowReadsAndWaitsForRefresh(t *testing.T) {
	ctx := context.Background()
	start := time.Now().Truncate(time.Second)
	clock := start
	ms := newTestMailService(t, start, nil)
	ms.now = func() time.Time { return clock }
	ms.mail.out = mailSnapshotJSON(t, nil, nil)
	ms.mail.err = errMailSlow
	at := func(d time.Duration) { clock = start.Add(d) }

	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 1 {
		t.Fatalf("runs = %d", ms.mail.runs)
	}
	// Twice the interval after the first slow read.
	at(10 * time.Minute)
	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 1 {
		t.Fatal("the next read must wait after Mail was slow")
	}
	at(20 * time.Minute)
	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 2 {
		t.Fatalf("runs = %d", ms.mail.runs)
	}
	// Four times the interval after the second.
	at(50 * time.Minute)
	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 2 {
		t.Fatal("the wait must grow")
	}
	at(60 * time.Minute)
	ms.syncIfAllowed(ctx, "enabled")
	if ms.mail.runs != 3 || !ms.stalled() {
		t.Fatalf("the third slow read in a row stops reading: runs %d stalled %v", ms.mail.runs, ms.stalled())
	}
	if _, lastErr, _ := ms.status(); lastErr != errMailStalled.Error() {
		t.Fatalf("lastErr = %q", lastErr)
	}
	at(30 * time.Hour)
	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 3 {
		t.Fatal("after too many slow reads only Refresh reads")
	}
	// Refresh reads, and a read Mail answers resets the wait.
	ms.mail.err = nil
	if _, err := ms.Sync(ctx, "refresh"); err != nil {
		t.Fatal(err)
	}
	if ms.stalled() {
		t.Fatal("a read Mail answered must lift the stop")
	}
	at(30*time.Hour + time.Minute)
	ms.syncIfAllowed(ctx, "interval")
	if ms.mail.runs != 5 {
		t.Fatalf("reading resumes at the usual pace: runs %d", ms.mail.runs)
	}
	// Switching off keeps previously recorded moments while waiting.
	ms.mail.err = errMailSlow
	at(31 * time.Hour)
	ms.syncIfAllowed(ctx, "interval")
	if _, err := ms.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(engagement.KindWaitingOnReply), DedupeKey: "waiting_on_reply:p:t", Params: `{"subject":"Hello"}`,
	}); err != nil {
		t.Fatal(err)
	}
	*ms.enabled = false
	ms.syncIfAllowed(ctx, "off")
	if open, _ := ms.store.GetOpenEngagementMomentByKey(ctx, "waiting_on_reply:p:t"); open == nil {
		t.Fatal("switching off must retain saved moments while reading waits")
	}
}

func TestMailWaitingSkipsMailAProgramWrote(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	row := func(id, from, subject string) storage.MailMessageRow {
		return storage.MailMessageRow{ID: id, Mailbox: storage.MailboxInbox, SenderEmail: from, SenderName: "Sam",
			ToMe: true, Subject: subject, ThreadKey: mailThreadKey(subject), At: now.Add(-30 * time.Hour).UnixMilli(),
			PrincipalID: "p-" + id}
	}
	rows := []storage.MailMessageRow{
		row("a", "sam@example.com", "Accepted: Budget review"),
		row("b", "sam@example.com", "Automatic reply: Q3 deck"),
		row("c", "sam@example.com", "Read: Q3 deck"),
		row("d", "sam@example.com", "已接受: 预算评审"),
		row("e", "noreply@example.com", "Your invoice"),
		row("f", "sam@example.com", "Budget review"),
	}
	got := mailWaitingOnReply(rows, now)
	if len(got) != 1 || got[0].PrincipalID != "p-f" {
		t.Fatalf("only the person's own message waits: %+v", got)
	}
}

func TestMailWaitingIsWithdrawnOnceAnsweredOrGone(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	app := mainChatTestApp(t)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
	inbox := func(id, from, subject string, ago time.Duration) storage.MailMessageRow {
		return storage.MailMessageRow{ID: id, Mailbox: storage.MailboxInbox, SenderEmail: from, SenderName: "Name",
			ToMe: true, Subject: subject, ThreadKey: mailThreadKey(subject), At: now.Add(-ago).UnixMilli(),
			PrincipalID: "p-" + from}
	}
	upsert := func(rows ...storage.MailMessageRow) {
		t.Helper()
		if _, err := app.store.UpsertMailMessages(ctx, rows, now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	key := func(from, subject string) string {
		return engagement.DedupeKey(engagement.KindWaitingOnReply, "p-"+from, mailThreadKey(subject))
	}
	open := func(k string) bool {
		t.Helper()
		row, err := app.store.GetOpenEngagementMomentByKey(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		return row != nil
	}
	upsert(
		inbox("a", "sam@example.com", "Q3 deck", 30*time.Hour),
		inbox("b", "ann@example.com", "Budget", 30*time.Hour),
		inbox("c", "bob@example.com", "Offsite", 30*time.Hour),
	)
	app.produceMailWaiting(ctx, now)
	for _, k := range []string{key("sam@example.com", "Q3 deck"), key("ann@example.com", "Budget"), key("bob@example.com", "Offsite")} {
		if !open(k) {
			t.Fatalf("%s should wait to be delivered", k)
		}
	}

	// Sam is answered from this Mac, Ann's message was archived, Bob wrote
	// again (still waiting, just not for a day yet).
	upsert(storage.MailMessageRow{ID: "s", Mailbox: storage.MailboxSent, SenderEmail: "me@example.com", To: "sam@example.com",
		Subject: "Re: Q3 deck", ThreadKey: mailThreadKey("Q3 deck"), At: now.Add(-time.Hour).UnixMilli()})
	if _, err := app.store.DeleteMailMessagesGone(ctx, storage.MailboxInbox, 0, map[string]bool{"a": true, "c": true}); err != nil {
		t.Fatal(err)
	}
	upsert(inbox("c2", "bob@example.com", "Re: Offsite", time.Hour))
	app.produceMailWaiting(ctx, now)
	if open(key("sam@example.com", "Q3 deck")) {
		t.Fatal("an answered conversation must be withdrawn before the digest")
	}
	if open(key("ann@example.com", "Budget")) {
		t.Fatal("a conversation that left the inbox must be withdrawn")
	}
	if !open(key("bob@example.com", "Offsite")) {
		t.Fatal("a person who wrote again is still waiting")
	}
}

func TestMailProtectedClearsAtOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	ms := newTestMailService(t, now, nil)
	ms.mail.out = mailSnapshotJSON(t, []testHeader{
		{ID: "<1@x>", From: "sam@example.com", Subject: "Hello", At: now.Add(-time.Hour).UnixMilli(), To: []string{"me@example.com"}},
	}, nil)
	if _, err := ms.Sync(ctx, "fill"); err != nil {
		t.Fatal(err)
	}
	app := &App{mailSync: ms.mailSyncService}
	app.mailProtectionChanged()
	if len(storedMail(t, ms.store)) != 1 {
		t.Fatal("protecting another app keeps the mail")
	}
	*ms.guarded = true
	app.mailProtectionChanged()
	if len(storedMail(t, ms.store)) != 0 {
		t.Fatal("protecting Mail must delete what was kept from it at once")
	}

	// The same through the Privacy page's binding, with the real check.
	*ms.guarded = false
	if _, err := ms.Sync(ctx, "fill again"); err != nil {
		t.Fatal(err)
	}
	ms.protected = newMailSyncService(&App{}).protected
	t.Cleanup(func() { pii.RemoveUserSensitiveApp(mailheaders.BundleID) })
	if err := app.AddSensitiveApp(mailheaders.BundleID, "custom"); err != nil {
		t.Fatal(err)
	}
	if len(storedMail(t, ms.store)) != 0 {
		t.Fatal("adding Mail on the Privacy page must delete what was kept from it at once")
	}
}

func TestMailClearStoredDeletesOnlyMailOutboundSnapshots(t *testing.T) {
	ms := newTestMailService(t, time.Now(), nil)
	for _, send := range []storage.OutboundSendRow{
		{MomentID: "mail", Revision: "revision", Channel: "mail", DraftTo: "dana@example.test", Text: "mail draft"},
		{MomentID: "slack", Revision: "revision", Channel: "slack", DraftTo: "U123", Text: "slack draft"},
	} {
		if _, ok, err := ms.store.ClaimOutboundSend(t.Context(), send, 100); err != nil || !ok {
			t.Fatalf("claim %s: %v %v", send.Channel, ok, err)
		}
	}
	if _, err := ms.store.UpsertMailMessages(t.Context(), []storage.MailMessageRow{{ID: "mail-row", Mailbox: storage.MailboxInbox, At: 100}}, 100); err != nil {
		t.Fatal(err)
	}
	ms.clearStored(t.Context(), "test")
	if rows := storedMail(t, ms.store); len(rows) != 0 {
		t.Fatalf("mail rows retained: %+v", rows)
	}
	mailSend, err := ms.store.LoadOutboundSend(t.Context(), "mail", "revision")
	if err != nil || mailSend != nil {
		t.Fatalf("Mail snapshot retained: %+v %v", mailSend, err)
	}
	slackSend, err := ms.store.LoadOutboundSend(t.Context(), "slack", "revision")
	if err != nil || slackSend == nil || slackSend.Text != "slack draft" {
		t.Fatalf("non-Mail snapshot removed: %+v %v", slackSend, err)
	}
}
