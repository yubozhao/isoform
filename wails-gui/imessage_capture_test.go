package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/imessage"
	"wails-gui/storage"
)

// imessageFixture is a reader over fake messages with one known person,
// Priya, in a one-to-one conversation, and an AI that answers what reply
// says.
type imessageFixture struct {
	t       *testing.T
	store   *storage.Store
	svc     *imessageService
	msgs    []imessage.Message
	reply   string
	prompts []string
	paused  bool
	enabled bool
	access  imessage.State
	priya   storage.PrincipalRow
	now     time.Time
}

func newIMessageFixture(t *testing.T) *imessageFixture {
	t.Helper()
	st := newContactsTestStore(t)
	ctx := context.Background()
	priya, err := st.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Priya"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	f := &imessageFixture{t: t, store: st, enabled: true, access: imessage.StateAllowed, priya: priya, now: now}
	chat := "iMessage;-;+14155550100"
	f.msgs = []imessage.Message{
		{RowID: 10, Chat: chat, Handle: "+14155550100", Members: []string{"+14155550100"}, At: now.Add(-30 * time.Minute), Text: "Hey! Could you send me the launch deck before Friday's review please"},
		{RowID: 11, Chat: chat, Handle: "+14155550100", Members: []string{"+14155550100"}, FromMe: true, At: now.Add(-20 * time.Minute), Text: "Sure, I'll get it to you"},
	}
	f.reply = `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked you for the launch deck by Friday.","nextStep":"Send Priya the launch deck","action":"reply","refs":["m1"],"confidence":0.9}]}`
	f.svc = &imessageService{
		store:     st,
		now:       func() time.Time { return f.now },
		enabled:   func() bool { return f.enabled },
		protected: func() bool { return false },
		paused:    func() bool { return f.paused },
		pauses:    func() ([]observationPause, error) { return nil, nil },
		access:    func() imessage.State { return f.access },
		read: func(ctx context.Context, after int64, since time.Time) ([]imessage.Message, error) {
			var out []imessage.Message
			for _, m := range f.msgs {
				if m.RowID > after && !m.At.Before(since) {
					out = append(out, m)
				}
			}
			return out, nil
		},
		resolve: func(ctx context.Context, handle string) (imessagePerson, bool) {
			if handle == "+14155550100" {
				return imessagePerson{ID: priya.ID, Name: "Priya"}, true
			}
			return imessagePerson{}, false
		},
		leftOut: func(ctx context.Context) (map[string]bool, error) { return peopleLeftOutSet(ctx, st) },
		extract: func(ctx context.Context, prompt string) (string, error) {
			f.prompts = append(f.prompts, prompt)
			return f.reply, nil
		},
		language: func() string { return "English" },
		kicks:    make(chan string, 1),
	}
	return f
}

func (f *imessageFixture) asks() []storage.MessageAskRow {
	f.t.Helper()
	rows, err := f.store.ListMessageAsks(context.Background(), 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// storedText is every text column of message_asks, joined.
func (f *imessageFixture) storedText() string {
	f.t.Helper()
	var b strings.Builder
	for _, row := range f.asks() {
		b.WriteString(row.Summary + "\n" + row.NextStep + "\n" + row.PersonName + "\n")
	}
	return b.String()
}

func TestIMessageAskIsStoredAsParaphraseOnly(t *testing.T) {
	f := newIMessageFixture(t)
	res, err := f.svc.Sync(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Asks != 1 || len(f.prompts) != 1 {
		t.Fatalf("asks=%d prompts=%d", res.Asks, len(f.prompts))
	}
	rows := f.asks()
	if len(rows) != 1 || rows[0].PrincipalID != f.priya.ID || rows[0].Kind != storage.MessageAskAsked ||
		rows[0].Summary != "Priya asked you for the launch deck by Friday." || rows[0].NextStep != "Send Priya the launch deck" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].At != f.msgs[0].At.UnixMilli() {
		t.Fatalf("at = %d, want the cited message's time", rows[0].At)
	}
	stored := f.storedText()
	for _, m := range f.msgs {
		if strings.Contains(stored, m.Text) || chatTextRepeated(stored, []string{m.Text}) {
			t.Fatalf("message text reached the store: %q", stored)
		}
	}
	// The text went to the AI, labelled by who sent it, and nowhere else.
	if !strings.Contains(f.prompts[0], `"from":"Priya"`) || !strings.Contains(f.prompts[0], `"from":"You"`) {
		t.Fatalf("prompt lacks speaker labels: %s", f.prompts[0])
	}
	if pos, _ := f.store.GetUIState(context.Background(), imessageRowIDKey); pos != "11" {
		t.Fatalf("position = %q", pos)
	}
	// Reading again finds nothing new and asks the AI nothing.
	if _, err := f.svc.Sync(context.Background(), "again"); err != nil || len(f.prompts) != 1 || len(f.asks()) != 1 {
		t.Fatalf("second read: err=%v prompts=%d asks=%d", err, len(f.prompts), len(f.asks()))
	}
}

func TestIMessageRefusesQuotesAndEchoes(t *testing.T) {
	for name, reply := range map[string]string{
		"quoted":       `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked: \"send me the launch deck\".","refs":["m1"],"confidence":0.9}]}`,
		"curly quoted": `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked for “the launch deck” by Friday.","refs":["m1"],"confidence":0.9}]}`,
		"echo":         `{"items":[{"kind":"asked","person":"Priya","summary":"Priya: could you send me the launch deck before Friday.","refs":["m1"],"confidence":0.9}]}`,
		"unknown":      `{"items":[{"kind":"asked","person":"Someone","summary":"Someone asked you for the deck.","refs":["m1"],"confidence":0.9}]}`,
		"wrong side":   `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked you for the launch deck by Friday.","refs":["m2"],"confidence":0.9}]}`,
		"no person":    `{"items":[{"kind":"asked","person":"Priya","summary":"You were asked for the launch deck by Friday.","refs":["m1"],"confidence":0.9}]}`,
		"bad ref":      `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked you for the launch deck by Friday.","refs":["m9"],"confidence":0.9}]}`,
		"unsure":       `{"items":[{"kind":"asked","person":"Priya","summary":"Priya asked you for the launch deck by Friday.","refs":["m1"],"confidence":0.4}]}`,
		"question":     `{"items":[{"kind":"asked","person":"Priya","summary":"Did Priya ask you for the launch deck?","refs":["m1"],"confidence":0.9}]}`,
		"not json":     `Priya wants the deck`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newIMessageFixture(t)
			f.reply = reply
			if _, err := f.svc.Sync(context.Background(), "test"); err != nil {
				t.Fatal(err)
			}
			if rows := f.asks(); len(rows) != 0 {
				t.Fatalf("kept %+v", rows)
			}
		})
	}
}

func TestIMessageCommitmentNeedsTheUsersMessage(t *testing.T) {
	f := newIMessageFixture(t)
	f.reply = `{"items":[{"kind":"committed","person":"Priya","summary":"You told Priya you would send the deck.","nextStep":"Send Priya the deck","action":"reply","refs":["m2"],"confidence":0.8}]}`
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if rows := f.asks(); len(rows) != 1 || rows[0].Kind != storage.MessageAskCommitted {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestIMessageClosedEyesBlocksReading(t *testing.T) {
	f := newIMessageFixture(t)
	f.paused = true
	f.svc.syncIfAllowed(context.Background(), "interval")
	if _, err := f.svc.Sync(context.Background(), "test"); err == nil {
		t.Fatal("a read while observing is paused must stop")
	}
	if len(f.prompts) != 0 || len(f.asks()) != 0 {
		t.Fatalf("prompts=%d asks=%d while paused", len(f.prompts), len(f.asks()))
	}
	if pos, _ := f.store.GetUIState(context.Background(), imessageRowIDKey); pos != "" {
		t.Fatalf("position moved while paused: %q", pos)
	}
}

func TestIMessagePauseLogDropsMessagesSentInsideIt(t *testing.T) {
	f := newIMessageFixture(t)
	f.svc.pauses = func() ([]observationPause, error) {
		return []observationPause{{Start: f.now.Add(-time.Hour).UnixMilli(), End: f.now.Add(-25 * time.Minute).UnixMilli()}}, nil
	}
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if len(f.asks()) != 0 {
		t.Fatalf("an ask from a paused stretch was kept: %+v", f.asks())
	}
	for _, p := range f.prompts {
		if strings.Contains(p, "before Friday's review") {
			t.Fatal("a message from a paused stretch reached the AI")
		}
	}
	f.svc.pauses = func() ([]observationPause, error) { return nil, sql.ErrConnDone }
	f.msgs = append(f.msgs, imessage.Message{RowID: 12, Chat: f.msgs[0].Chat, Handle: f.msgs[0].Handle, Members: f.msgs[0].Members, At: f.now.Add(-time.Minute), Text: "ping"})
	if _, err := f.svc.Sync(context.Background(), "test"); err == nil {
		t.Fatal("an unreadable pause log must store nothing")
	}
}

func TestIMessageLeftOutPersonIsNeverRead(t *testing.T) {
	f := newIMessageFixture(t)
	if err := setPeopleLeftOut(context.Background(), f.store, []string{f.priya.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if len(f.prompts) != 0 || len(f.asks()) != 0 {
		t.Fatalf("a left-out conversation was read: prompts=%d asks=%d", len(f.prompts), len(f.asks()))
	}
	// Leaving someone out after an ask was kept deletes it.
	g := newIMessageFixture(t)
	if _, err := g.svc.Sync(context.Background(), "test"); err != nil || len(g.asks()) != 1 {
		t.Fatalf("setup: %v %d", err, len(g.asks()))
	}
	a := &App{store: g.store, imessage: g.svc}
	if err := a.SetPeopleLeftOut([]string{g.priya.ID}); err != nil {
		t.Fatal(err)
	}
	if len(g.asks()) != 0 {
		t.Fatalf("leaving Priya out kept %+v", g.asks())
	}
}

func TestIMessageLeftOutDuringTheAICallDropsTheAsk(t *testing.T) {
	f := newIMessageFixture(t)
	f.svc.extract = func(ctx context.Context, prompt string) (string, error) {
		if err := setPeopleLeftOut(ctx, f.store, []string{f.priya.ID}); err != nil {
			t.Fatal(err)
		}
		return f.reply, nil
	}
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if len(f.asks()) != 0 {
		t.Fatalf("kept %+v", f.asks())
	}
}

func TestIMessageMissingAccessDegradesQuietly(t *testing.T) {
	for _, state := range []imessage.State{imessage.StateDenied, imessage.StateMissing} {
		f := newIMessageFixture(t)
		f.access = state
		f.svc.read = func(context.Context, int64, time.Time) ([]imessage.Message, error) {
			t.Fatal("read without access")
			return nil, nil
		}
		res, err := f.svc.Sync(context.Background(), "test")
		if err != nil || res.Messages != 0 {
			t.Fatalf("state %v: res=%+v err=%v", state, res, err)
		}
		if _, lastErr, got := f.svc.status(); lastErr != "" || got != state {
			t.Fatalf("state %v: status error %q state %v", state, lastErr, got)
		}
	}
}

func TestIMessageSwitchOffAndProtectGates(t *testing.T) {
	f := newIMessageFixture(t)
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil || len(f.asks()) != 1 {
		t.Fatalf("setup: %v", err)
	}
	f.enabled = false
	f.msgs = append(f.msgs, imessage.Message{RowID: 12, Chat: f.msgs[0].Chat, Handle: f.msgs[0].Handle, Members: f.msgs[0].Members, At: f.now.Add(-time.Minute), Text: "and the notes too"})
	f.svc.syncIfAllowed(context.Background(), "interval")
	if len(f.prompts) != 1 || len(f.asks()) != 1 {
		t.Fatalf("switch off: prompts=%d asks=%d (off keeps what was kept, reads nothing)", len(f.prompts), len(f.asks()))
	}
	f.svc.protected = func() bool { return true }
	f.svc.syncIfAllowed(context.Background(), "interval")
	if len(f.asks()) != 0 {
		t.Fatal("protecting Messages must delete what was kept")
	}
}

func TestIMessageRetentionAndForgettingAPerson(t *testing.T) {
	f := newIMessageFixture(t)
	if _, err := f.svc.Sync(context.Background(), "test"); err != nil || len(f.asks()) != 1 {
		t.Fatalf("setup: %v", err)
	}
	if err := f.store.DeletePrincipal(context.Background(), f.priya.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.asks()) != 0 {
		t.Fatal("deleting a person must delete what was read from their conversations")
	}
	g := newIMessageFixture(t)
	g.svc.retention = func() time.Duration { return time.Minute }
	if _, err := g.svc.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if len(g.asks()) != 0 {
		t.Fatalf("an ask older than retention was kept: %+v", g.asks())
	}
}

func TestIMessageLeftOutListUnreadableStoresNothing(t *testing.T) {
	f := newIMessageFixture(t)
	if err := f.store.SetUIState(context.Background(), peopleLeftOutKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Sync(context.Background(), "test"); err == nil || len(f.prompts) != 0 {
		t.Fatalf("err=%v prompts=%d", err, len(f.prompts))
	}
}
