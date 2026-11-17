package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const testTextNumber = "+14155550123"

var testMomentText = engagementText{
	PushBody: "Isoform needs a decision.",
	Title:    "Approve sending the Q3 reply to Sam?",
}

type fakeTextClock struct {
	now time.Time
}

func (c *fakeTextClock) Now() time.Time          { return c.now }
func (c *fakeTextClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type fakeTextMessage struct {
	to   string
	body string
}

type fakeTextSender struct {
	mu    sync.Mutex
	err   error
	calls int
	sent  []fakeTextMessage
}

func (s *fakeTextSender) Send(_ context.Context, to, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, fakeTextMessage{to: to, body: body})
	return nil
}

func (s *fakeTextSender) messages() []fakeTextMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeTextMessage(nil), s.sent...)
}

func (s *fakeTextSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// textChannelRig is a text channel with a fake clock, sender, support flag and
// Messages permission, all in a temporary state directory.
type textChannelRig struct {
	ch         *textChannel
	sender     *fakeTextSender
	clock      *fakeTextClock
	supported  bool
	automation textAutomationState
}

func newTextChannelRig(t *testing.T) *textChannelRig {
	t.Helper()
	rig := &textChannelRig{
		sender:     &fakeTextSender{},
		clock:      &fakeTextClock{now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)},
		supported:  true,
		automation: textAutomationAllowed,
	}
	rig.ch = &textChannel{
		dir:        t.TempDir(),
		now:        rig.clock.Now,
		sender:     rig.sender,
		supported:  func() bool { return rig.supported },
		automation: func(bool) textAutomationState { return rig.automation },
	}
	return rig
}

// enable turns the channel on for testTextNumber with consent accepted.
func (r *textChannelRig) enable(t *testing.T) {
	t.Helper()
	if err := saveTextChannelConfig(r.ch.dir, TextChannelConfig{Number: testTextNumber, VerifiedAt: r.clock.now}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ch.SetConfig(TextChannelInput{Enabled: true, Number: testTextNumber, AcceptConsent: true}); err != nil {
		t.Fatalf("enable the text channel: %v", err)
	}
}

func (r *textChannelRig) status(t *testing.T) TextChannelStatus {
	t.Helper()
	status, err := r.ch.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return status
}

func (r *textChannelRig) sendMoment(t *testing.T) {
	t.Helper()
	if err := r.ch.SendMoment(context.Background(), testMomentText); err != nil {
		t.Fatalf("send a moment text: %v", err)
	}
}

func TestTextChannelConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	empty, err := loadTextChannelConfig(dir)
	if err != nil || empty.Enabled || empty.Version != textChannelFileVersion {
		t.Fatalf("a missing file must load as defaults: %+v %v", empty, err)
	}

	consent := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	want := TextChannelConfig{Enabled: true, Number: testTextNumber, IncludeNames: true, ConsentAcceptedAt: consent}
	if err := saveTextChannelConfig(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadTextChannelConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Number != want.Number || !got.IncludeNames || !got.ConsentAcceptedAt.Equal(consent) || got.Version != textChannelFileVersion {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	info, err := os.Stat(filepath.Join(dir, textChannelConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
}

func TestTextChannelConfigRejectsUnreadableFiles(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"version":1,"enabled":true,"extra":true}`,
		"old version":   `{"version":0,"enabled":true}`,
		"not json":      `enabled`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, textChannelConfigFileName), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadTextChannelConfig(dir); err == nil {
				t.Fatal("a bad file must not load")
			}
		})
	}
}

func TestTextChannelSettingsAreValidated(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		input   TextChannelInput
		wantErr error
	}{
		{"e164 number with consent", TextChannelInput{Number: "+14155550123", AcceptConsent: true}, nil},
		{"shortest e164 number", TextChannelInput{Number: "+12345678", AcceptConsent: true}, nil},
		{"surrounding spaces are trimmed", TextChannelInput{Number: "  +14155550123 ", AcceptConsent: true}, nil},
		{"missing plus", TextChannelInput{Number: "14155550123", AcceptConsent: true}, errTextChannelNumber},
		{"seven digits", TextChannelInput{Number: "+1234567", AcceptConsent: true}, errTextChannelNumber},
		{"sixteen digits", TextChannelInput{Number: "+1234567890123456", AcceptConsent: true}, errTextChannelNumber},
		{"inner spaces", TextChannelInput{Number: "+1 415 555 0123", AcceptConsent: true}, errTextChannelNumber},
		{"letters", TextChannelInput{Number: "+1415555CALL", AcceptConsent: true}, errTextChannelNumber},
		{"enable without a number", TextChannelInput{Enabled: true, AcceptConsent: true}, errTextChannelNoNumber},
		{"enable without consent", TextChannelInput{Enabled: true, Number: testTextNumber}, errTextChannelNoConsent},
		{"enable before verification", TextChannelInput{Enabled: true, Number: testTextNumber, AcceptConsent: true}, errTextChannelUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := nextTextChannelConfig(TextChannelConfig{}, tc.input, now)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestTextChannelRefusedSettingsAreNotSaved(t *testing.T) {
	rig := newTextChannelRig(t)
	if _, err := rig.ch.SetConfig(TextChannelInput{Enabled: true, Number: "not a number", AcceptConsent: true}); !errors.Is(err, errTextChannelNumber) {
		t.Fatalf("invalid number error = %v", err)
	}
	status := rig.status(t)
	if status.Enabled || status.Number != "" || status.ConsentAccepted {
		t.Fatalf("a refused change must not be saved: %+v", status)
	}
}

func TestTextChannelConsentBelongsToOneNumber(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)
	if !rig.status(t).ConsentAccepted {
		t.Fatal("accepting consent must be recorded")
	}

	// A new number with the channel on needs consent for that number.
	if _, err := rig.ch.SetConfig(TextChannelInput{Enabled: true, Number: "+442079460100"}); !errors.Is(err, errTextChannelNoConsent) {
		t.Fatalf("a new number without consent must be refused: %v", err)
	}
	// With the channel off, a new number is saved and its consent is cleared.
	if _, err := rig.ch.SetConfig(TextChannelInput{Enabled: false, Number: "+442079460100"}); err != nil {
		t.Fatalf("saving a new number with the channel off: %v", err)
	}
	if status := rig.status(t); status.ConsentAccepted || status.Number != "+442079460100" {
		t.Fatalf("a changed number must clear consent: %+v", status)
	}

	// Turning the channel off keeps the consent for the same number.
	rig.enable(t)
	if _, err := rig.ch.SetConfig(TextChannelInput{Enabled: false, Number: testTextNumber}); err != nil {
		t.Fatal(err)
	}
	if !rig.status(t).ConsentAccepted {
		t.Fatal("turning the channel off must keep consent for the same number")
	}
}

func TestTextChannelDailyCapAndCooldown(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)

	rig.sendMoment(t)
	if err := rig.ch.SendMoment(context.Background(), testMomentText); !errors.Is(err, errTextChannelCooldown) {
		t.Fatalf("a second text inside two hours must wait: %v", err)
	}
	if rig.sender.callCount() != 1 {
		t.Fatalf("a refused text reached the sender: %d calls", rig.sender.callCount())
	}

	rig.clock.Advance(2 * time.Hour)
	rig.sendMoment(t)
	rig.clock.Advance(2 * time.Hour)
	rig.sendMoment(t)
	rig.clock.Advance(2 * time.Hour)
	if err := rig.ch.SendMoment(context.Background(), testMomentText); !errors.Is(err, errTextChannelDailyLimit) {
		t.Fatalf("a fourth text on the same day must be refused: %v", err)
	}
	if rig.sender.callCount() != 3 {
		t.Fatalf("sender calls = %d, want 3", rig.sender.callCount())
	}
	if status := rig.status(t); status.SentToday != 3 || status.LastSentAt == "" {
		t.Fatalf("status after three texts = %+v", status)
	}

	// The count resets with the local day.
	rig.clock.now = time.Date(2026, 10, 13, 0, 30, 0, 0, time.Local)
	rig.sendMoment(t)
	if status := rig.status(t); status.SentToday != 1 {
		t.Fatalf("the next day must start at one: %+v", status)
	}
}

func TestTextChannelFailedSendsAreNotCounted(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)
	rig.sender.err = errors.New("Messages could not send the text (error -1728)")

	if err := rig.ch.SendMoment(context.Background(), testMomentText); err == nil {
		t.Fatal("a failed send must return its error without retrying")
	}
	status := rig.status(t)
	if status.SentToday != 0 || status.LastError != "Messages could not send the text (error -1728)" {
		t.Fatalf("status after a failed send = %+v", status)
	}

	rig.sender.err = nil
	if rig.ch.Available() {
		t.Fatal("failed sends must pause automatic delivery")
	}
	if _, err := rig.ch.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	rig.sendMoment(t)
	if status := rig.status(t); status.LastError != "" || status.SentToday != 1 {
		t.Fatalf("a failed send must not start the cooldown: %+v", status)
	}
}

func TestTextChannelSendsOnlyWhenReady(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, rig *textChannelRig)
		wantErr error
	}{
		{"platform cannot send", func(t *testing.T, rig *textChannelRig) {
			rig.supported = false
		}, errTextChannelUnsupported},
		{"Messages automation is not allowed", func(t *testing.T, rig *textChannelRig) {
			rig.automation = textAutomationNotAllowed
		}, errTextMessagesNotAllowed},
		{"the channel is off", func(t *testing.T, rig *textChannelRig) {
			if _, err := rig.ch.SetConfig(TextChannelInput{Enabled: false, Number: testTextNumber}); err != nil {
				t.Fatal(err)
			}
		}, errTextChannelOff},
		{"no consent", func(t *testing.T, rig *textChannelRig) {
			if err := saveTextChannelConfig(rig.ch.dir, TextChannelConfig{Enabled: true, Number: testTextNumber}); err != nil {
				t.Fatal(err)
			}
		}, errTextChannelNoConsent},
		{"no number", func(t *testing.T, rig *textChannelRig) {
			if err := saveTextChannelConfig(rig.ch.dir, TextChannelConfig{Enabled: true, ConsentAcceptedAt: rig.clock.now}); err != nil {
				t.Fatal(err)
			}
		}, errTextChannelNoNumber},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTextChannelRig(t)
			rig.enable(t)
			tc.setup(t, rig)
			if err := rig.ch.SendMoment(context.Background(), testMomentText); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if rig.ch.Available() {
				t.Fatal("Available must agree with SendMoment")
			}
			if rig.sender.callCount() != 0 {
				t.Fatalf("a text reached the sender while not ready")
			}
		})
	}
}

func TestTextChannelKeepsItsPermissionWhileMessagesIsClosed(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.automation = textAutomationUnknown
	rig.enable(t)
	if rig.ch.Available() {
		t.Fatal("a permission that was never granted must not be assumed")
	}

	rig.automation = textAutomationAllowed
	if !rig.ch.Available() {
		t.Fatal("a granted permission must make the channel available")
	}

	// Messages is closed, so macOS cannot answer; the recorded grant stands.
	rig.automation = textAutomationUnknown
	if !rig.ch.Available() {
		t.Fatal("a closed Messages app must not turn the channel off")
	}
	rig.sendMoment(t)

	// The user revokes the permission and Messages is open: the channel stops.
	rig.automation = textAutomationNotAllowed
	if rig.ch.Available() {
		t.Fatal("a revoked permission must turn the channel off")
	}
	rig.automation = textAutomationUnknown
	if rig.ch.Available() {
		t.Fatal("a revoked permission must stay off while Messages is closed")
	}
}

func TestTextChannelDenialFromTheSenderClearsThePermission(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)
	rig.sender.err = errTextMessagesNotAllowed
	if err := rig.ch.SendMoment(context.Background(), testMomentText); !errors.Is(err, errTextMessagesNotAllowed) {
		t.Fatalf("denied send error = %v", err)
	}
	rig.automation = textAutomationUnknown
	if rig.ch.Available() {
		t.Fatal("a refused Apple event must clear the recorded permission")
	}
}

func TestTextChannelTestTextIsOutsideTheDailyCap(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)
	ctx := context.Background()

	for i := 0; i < textChannelDailyLimit; i++ {
		if i > 0 {
			rig.clock.Advance(2 * time.Hour)
		}
		rig.sendMoment(t)
	}
	// The daily cap is spent and the cooldown is running; the test text still goes.
	status, err := rig.ch.SendTest(ctx)
	if err != nil {
		t.Fatalf("a test text is outside the daily cap: %v", err)
	}
	if got := rig.sender.messages(); got[len(got)-1].body != textChannelTestBody {
		t.Fatalf("test body = %q", got[len(got)-1].body)
	}
	if status.SentToday != textChannelDailyLimit {
		t.Fatalf("a test text must not count toward the cap: %+v", status)
	}

	if _, err := rig.ch.SendTest(ctx); !errors.Is(err, errTextChannelTestSoon) {
		t.Fatalf("a second test within a minute must wait: %v", err)
	}
	rig.clock.Advance(time.Minute)
	if _, err := rig.ch.SendTest(ctx); err != nil {
		t.Fatalf("a test after a minute: %v", err)
	}
}

func TestTextChannelBodyIsGenericByDefault(t *testing.T) {
	body, err := textChannelBody(testMomentText, false)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Isoform needs a decision. Open Isoform on your computer." {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, "Sam") || strings.Contains(body, "Q3") {
		t.Fatalf("raw content leaked into the default text: %q", body)
	}
}

func TestTextChannelBodyNamesOnlyWhenAllowed(t *testing.T) {
	body, err := textChannelBody(testMomentText, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Approve sending the Q3 reply to Sam? Open Isoform on your computer."; body != want {
		t.Fatalf("named body = %q, want %q", body, want)
	}

	body, err = textChannelBody(engagementText{PushBody: "Isoform needs a decision."}, true)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Isoform needs a decision. Open Isoform on your computer." {
		t.Fatalf("without a title the generic lead must be used: %q", body)
	}
}

func TestTextChannelBodyNeverExceedsTheLimit(t *testing.T) {
	cases := map[string]string{
		"ascii": strings.Repeat("Approve the reply to a very long subject ", 20),
		"cjk":   strings.Repeat("批准发送给同事的回复", 40),
	}
	for name, title := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := textChannelBody(engagementText{PushBody: "x", Title: title}, true)
			if err != nil {
				t.Fatal(err)
			}
			n := utf8.RuneCountInString(body)
			if n > textChannelBodyLimit || n < textChannelBodyLimit-10 {
				t.Fatalf("body has %d characters, want at most %d: %q", n, textChannelBodyLimit, body)
			}
			if !strings.HasSuffix(body, "... "+textChannelSignOff) {
				t.Fatalf("a cut body must keep the sign-off: %q", body)
			}
		})
	}
}

func TestTextChannelBodyIsOneLine(t *testing.T) {
	body, err := textChannelBody(engagementText{PushBody: "Isoform\nneeds\ta decision.\x00"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Isoform needs a decision. Open Isoform on your computer." {
		t.Fatalf("body = %q", body)
	}
	if _, err := textChannelBody(engagementText{}, true); !errors.Is(err, errTextChannelEmptyCopy) {
		t.Fatalf("empty copy error = %v", err)
	}
}

func TestTextChannelStatusReportsSettingsAndSends(t *testing.T) {
	rig := newTextChannelRig(t)
	if status := rig.status(t); !status.Supported || status.Enabled || status.Number != "" || status.ConsentAccepted {
		t.Fatalf("a fresh channel status = %+v", status)
	}

	rig.enable(t)
	rig.sendMoment(t)
	status := rig.status(t)
	if !status.Supported || !status.Enabled || status.Number != testTextNumber || !status.ConsentAccepted {
		t.Fatalf("settings in status = %+v", status)
	}
	if !status.PermissionGranted || status.SentToday != 1 || status.LastError != "" {
		t.Fatalf("send state in status = %+v", status)
	}
	if _, err := time.Parse(time.RFC3339, status.LastSentAt); err != nil {
		t.Fatalf("last sent time %q is not RFC 3339: %v", status.LastSentAt, err)
	}
}

func TestEngagementRoutesATimeSensitiveMomentToText(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	textRig := newTextChannelRig(t)
	textRig.enable(t)
	channel := textRig.ch
	channel.now = func() time.Time { return rig.clock }

	rig.svc.deliver.Text = func(ctx context.Context, _ storage.EngagementMomentRow, _ engagement.Moment, _ engagement.Verdict, text engagementText) error {
		return channel.SendMoment(ctx, text)
	}
	// The phone was pushed and the push failed, so the time-sensitive blocked
	// work falls to the text channel.
	rig.presence = engagement.Presence{
		MacIdle:         time.Hour,
		PhoneRegistered: false,
		// An old paired phone was removed recently; its last-seen time is irrelevant.
		PhoneLastSeen: rig.clock,
		TextAvailable: channel.Available(),
	}
	// A text goes only where the person chose it (notify v1.0.0 rule 14).
	row, err := rig.svc.Submit(ctx, engagement.Moment{
		Kind: engagement.KindWorkBlocked, SubjectID: "hf-9", Channels: []engagement.Channel{engagement.ChannelText},
		DueAt: rig.clock.Add(time.Hour), HardDeadline: true,
		Params: map[string]string{"title": "The Q3 reply to Sam", "question": "your attention."},
	})
	if err != nil {
		t.Fatal(err)
	}

	rig.svc.Pass(ctx)
	if len(rig.pushed) != 0 {
		t.Fatalf("a failed push must not be sent again: %d pushes", len(rig.pushed))
	}
	messages := textRig.sender.messages()
	if len(messages) != 1 || messages[0].to != testTextNumber || messages[0].body != "Work is waiting on you. Open Isoform on your computer." {
		t.Fatalf("texts sent = %+v", messages)
	}
	got, _ := rig.store.GetEngagementMoment(ctx, row.ID)
	if got.State != storage.EngagementStateSent || got.Channel != "text" {
		t.Fatalf("stored delivery = %+v", got)
	}
}
