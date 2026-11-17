package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func verificationCode(t *testing.T, rig *textChannelRig) string {
	t.Helper()
	messages := rig.sender.messages()
	code := regexp.MustCompile(`[0-9]{6}`).FindString(messages[len(messages)-1].body)
	if code == "" {
		t.Fatal("verification text has no code")
	}
	return code
}

func TestMessagesNumberVerificationRequiresReceivedCode(t *testing.T) {
	rig := newTextChannelRig(t)
	ctx := context.Background()
	input := TextChannelInput{Number: testTextNumber, AcceptConsent: true}
	if _, err := rig.ch.SetConfig(input); err != nil {
		t.Fatal(err)
	}
	input.Enabled = true
	if _, err := rig.ch.SetConfig(input); !errors.Is(err, errTextChannelUnverified) {
		t.Fatalf("enable before code: %v", err)
	}
	if rig.sender.callCount() != 0 {
		t.Fatal("saving consent must not send or ask for permission")
	}
	status, err := rig.ch.StartVerification(ctx)
	if err != nil || !status.Verifying || status.Verified {
		t.Fatalf("start = %+v, %v", status, err)
	}
	code := verificationCode(t, rig)
	raw, err := os.ReadFile(filepath.Join(rig.ch.dir, textChannelStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"`+code+`"`) {
		t.Fatal("the received code was stored in plaintext")
	}
	if _, err := rig.ch.StartVerification(ctx); !errors.Is(err, errTextVerificationSoon) {
		t.Fatalf("resend: %v", err)
	}
	if _, err := rig.ch.ConfirmVerification("wrong"); !errors.Is(err, errTextVerificationWrong) {
		t.Fatalf("wrong code: %v", err)
	}
	if status, err = rig.ch.ConfirmVerification(code); err != nil || !status.Verified || status.Verifying {
		t.Fatalf("confirm = %+v, %v", status, err)
	}
	if _, err := rig.ch.ConfirmVerification(code); !errors.Is(err, errTextVerificationSpent) {
		t.Fatalf("replayed code: %v", err)
	}
	if _, err := rig.ch.SetConfig(input); err != nil {
		t.Fatal(err)
	}
	if !rig.ch.Available() {
		t.Fatal("a verified number must become available after enabling")
	}
	input.Number = "+14155550115"
	if _, err := rig.ch.SetConfig(input); !errors.Is(err, errTextChannelUnverified) {
		t.Fatalf("verification inherited by another number: %v", err)
	}
	input.Enabled = false
	if status, err := rig.ch.SetConfig(input); err != nil || status.Verified {
		t.Fatalf("new number = %+v, %v", status, err)
	}
}

func TestMessagesVerificationExpiryAttemptsAndNumberChange(t *testing.T) {
	for _, mode := range []string{"expired", "attempts", "number changed", "number changed and restored"} {
		t.Run(mode, func(t *testing.T) {
			rig := newTextChannelRig(t)
			if _, err := rig.ch.SetConfig(TextChannelInput{Number: testTextNumber, AcceptConsent: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.ch.StartVerification(context.Background()); err != nil {
				t.Fatal(err)
			}
			code := verificationCode(t, rig)
			switch mode {
			case "expired":
				rig.clock.Advance(textVerificationTTL)
			case "attempts":
				for i := 0; i < 5; i++ {
					_, _ = rig.ch.ConfirmVerification("wrong")
				}
			case "number changed", "number changed and restored":
				if _, err := rig.ch.SetConfig(TextChannelInput{Number: "+14155550115", AcceptConsent: true}); err != nil {
					t.Fatal(err)
				}
				if mode == "number changed and restored" {
					if _, err := rig.ch.SetConfig(TextChannelInput{Number: testTextNumber, AcceptConsent: true}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Reopen the same state: a restart cannot reset guessing or expiry.
			reopened := newTextChannel(rig.ch.dir)
			reopened.now = rig.ch.now
			reopened.automation = rig.ch.automation
			if _, err := reopened.ConfirmVerification(code); !errors.Is(err, errTextVerificationSpent) {
				t.Fatalf("spent code: %v", err)
			}
		})
	}
}

// This is a deterministic routing regression using the real engagement store
// and sender boundary. The separately tagged device test uses Messages itself.
// A text goes only where the person chose it (notify v1.0.0 rule 14); when
// chosen, it keeps the caps, the verified number and the private copy. With no
// text chosen, it stays on the computer: no text, no push.
func TestChosenMessagesTextKeepsCapsAndPrivateCopy(t *testing.T) {
	rig := newEngagementTestRig(t)
	messages := newTextChannelRig(t)
	messages.enable(t)
	messages.ch.now = func() time.Time { return rig.clock }
	sms := newSMSChannel(t.TempDir()) // no relay and no phone pairing
	rig.svc.presence = func(time.Time) engagement.Presence {
		return engagement.Presence{MacIdle: time.Hour, PhoneLastSeen: rig.clock, TextAvailable: messages.ch.Available()}
	}
	rig.svc.deliver.Text = func(ctx context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error {
		return sendEngagementText(ctx, sms, messages.ch, row, m, v, text)
	}
	ctx := context.Background()
	submit := func(id string) storage.EngagementMomentRow {
		t.Helper()
		row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: id, DueAt: rig.clock.Add(time.Hour), HardDeadline: true, ExpiresAt: rig.clock.Add(12 * time.Hour), Channels: []engagement.Channel{engagement.ChannelText}, Params: map[string]string{"title": "The Q3 reply to Sam", "question": "your attention."}})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	first := submit("first")
	rig.svc.Pass(ctx)
	if got, _ := rig.store.GetEngagementMoment(ctx, first.ID); got.Channel != "text" || got.State != storage.EngagementStateSent {
		t.Fatalf("delivery = %+v", got)
	}
	second := submit("second")
	rig.clock = rig.clock.Add(2*time.Hour - time.Second)
	rig.svc.Pass(ctx)
	if messages.sender.callCount() != 1 {
		t.Fatal("cooldown boundary was not respected")
	}
	rig.clock = rig.clock.Add(engagementRetryDelay)
	rig.svc.Pass(ctx)
	if got, _ := rig.store.GetEngagementMoment(ctx, second.ID); got.State != storage.EngagementStateSent {
		t.Fatalf("after cooldown = %+v", got)
	}
	if _, err := messages.ch.SetConfig(TextChannelInput{Enabled: true, Number: testTextNumber, IncludeNames: true}); err != nil {
		t.Fatal(err)
	}
	rig.clock = rig.clock.Add(2 * time.Hour)
	submit("third")
	rig.svc.Pass(ctx)
	rig.clock = rig.clock.Add(2 * time.Hour)
	submit("fourth")
	rig.svc.Pass(ctx)
	texts := messages.sender.messages()
	if len(texts) != 3 {
		t.Fatalf("texts = %+v", texts)
	}
	for i, text := range texts {
		if text.to != testTextNumber {
			t.Fatal("text sent to another number")
		}
		if i < 2 && (strings.Contains(text.body, "Sam") || strings.Contains(text.body, "Q3")) {
			t.Fatalf("default copy names work: %q", text.body)
		}
		if !strings.Contains(text.body, "computer") {
			t.Fatalf("missing computer wording: %q", text.body)
		}
	}
	if !strings.Contains(texts[2].body, "Sam") {
		t.Fatal("names switch did not apply")
	}
	if len(rig.pushed) != 0 || len(rig.desktop) != 0 {
		t.Fatal("an unpaired phone or away computer received the moment")
	}
	rig.clock = rig.clock.Add(2 * time.Hour)
	unchosen, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "unchosen", DueAt: rig.clock.Add(time.Hour), HardDeadline: true, ExpiresAt: rig.clock.Add(12 * time.Hour), Params: map[string]string{"title": "The Q3 reply to Sam", "question": "your attention."}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	got, _ := rig.store.GetEngagementMoment(ctx, unchosen.ID)
	if messages.sender.callCount() != 3 || strings.Contains(got.Channel, "text") || len(rig.pushed) != 0 {
		t.Fatalf("a text stood in for the notification: %+v, texts=%d, pushes=%d", got, messages.sender.callCount(), len(rig.pushed))
	}
	if got.Channel != string(engagement.ChannelDesktop) && got.Channel != string(engagement.ChannelTodayQuiet) {
		t.Fatalf("the moment left the computer: %+v", got)
	}
}

func TestMessagesFailuresNeverRetryAutomatically(t *testing.T) {
	for _, failure := range []error{errTextMessagesSignedOut, errTextMessagesNotAllowed, errTextChannelUnverified} {
		t.Run(failure.Error(), func(t *testing.T) {
			rig := newEngagementTestRig(t)
			messages := newTextChannelRig(t)
			messages.enable(t)
			messages.sender.err = failure
			if errors.Is(failure, errTextChannelUnverified) {
				cfg, _ := loadTextChannelConfig(messages.ch.dir)
				cfg.VerifiedAt = time.Time{}
				if err := saveTextChannelConfig(messages.ch.dir, cfg); err != nil {
					t.Fatal(err)
				}
			}
			rig.presence = engagement.Presence{MacIdle: time.Hour, TextAvailable: true}
			rig.svc.deliver.Text = func(ctx context.Context, _ storage.EngagementMomentRow, _ engagement.Moment, _ engagement.Verdict, text engagementText) error {
				return messages.ch.SendMoment(ctx, text)
			}
			// A text goes only where the person chose it (notify v1.0.0 rule 14).
			row, err := rig.svc.Submit(context.Background(), engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: fmt.Sprint(failure), DueAt: rig.clock.Add(time.Hour), HardDeadline: true, Channels: []engagement.Channel{engagement.ChannelText}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				rig.svc.Pass(context.Background())
				rig.clock = rig.clock.Add(engagementRetryDelay)
			}
			wantCalls := 1
			if errors.Is(failure, errTextChannelUnverified) {
				wantCalls = 0
			}
			if messages.sender.callCount() != wantCalls {
				t.Fatalf("sender calls=%d", messages.sender.callCount())
			}
			stored, _ := rig.store.GetEngagementMoment(context.Background(), row.ID)
			if stored.State != storage.EngagementStateSuppressed {
				t.Fatalf("failure kept retrying: %+v", stored)
			}
			if messages.ch.Available() {
				t.Fatal("failure left automatic sending available")
			}
		})
	}
}

func TestPermanentTextFailureKeepsOtherRequestedChannels(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.presence.TextAvailable = true
	texts, desktops := 0, 0
	rig.pushError = errors.New("temporary push failure")
	rig.svc.deliver.Text = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		texts++
		return permanentTextFailure(errTextMessagesSignedOut)
	}
	rig.svc.deliver.Desktop = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		desktops++
		return nil
	}
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "requested-channels", DueAt: rig.clock.Add(time.Hour), HardDeadline: true, Channels: []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelPush, engagement.ChannelText}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	rig.pushError = nil
	rig.clock = rig.clock.Add(engagementRetryDelay)
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored.State != storage.EngagementStateSent || stored.Channel != "desktop+push" || texts != 1 || desktops != 1 || len(rig.pushed) != 1 {
		t.Fatalf("other channels lost or text retried: %+v, texts=%d, desktop=%d, pushes=%d, %v", stored, texts, desktops, len(rig.pushed), err)
	}
}

func TestMessagesReadsAndVerificationNeverAskForAutomation(t *testing.T) {
	rig := newTextChannelRig(t)
	var asks int
	rig.ch.automation = func(ask bool) textAutomationState {
		if ask {
			asks++
		}
		return textAutomationAllowed
	}
	if _, err := rig.ch.SetConfig(TextChannelInput{Number: testTextNumber, AcceptConsent: true}); err != nil {
		t.Fatal(err)
	}
	_, _ = rig.ch.Status()
	_ = rig.ch.Available()
	if _, err := rig.ch.StartVerification(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.ch.ConfirmVerification(verificationCode(t, rig)); err != nil {
		t.Fatal(err)
	}
	if asks != 0 {
		t.Fatal("background read or verification asked for access")
	}
	if !rig.ch.RequestMessagesAutomation() || asks != 1 {
		t.Fatal("explicit permission action must ask once")
	}
}

func TestPermanentTextFailureFinishesAnEarlierPartialDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.presence.TextAvailable = true
	textError := errTextChannelCooldown
	texts, desktops := 0, 0
	rig.svc.deliver.Text = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		texts++
		return textError
	}
	rig.svc.deliver.Desktop = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		desktops++
		return nil
	}
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "earlier-partial", DueAt: rig.clock.Add(time.Hour), HardDeadline: true, Channels: []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelText}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	textError = permanentTextFailure(errTextMessagesSignedOut)
	rig.clock = rig.clock.Add(engagementRetryDelay)
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored.State != storage.EngagementStateSent || stored.Channel != "desktop" || texts != 2 || desktops != 1 || rig.svc.state.SentByKindToday[engagement.KindWorkBlocked] != 1 {
		t.Fatalf("earlier delivery lost or repeated: %+v, texts=%d, desktop=%d, %v", stored, texts, desktops, err)
	}
}

func TestMessagesChineseCopyKeepsNamesOutByDefault(t *testing.T) {
	rig := newTextChannelRig(t)
	rig.enable(t)
	rig.ch.lang = func() string { return "zh-CN" }
	copy, _ := engagement.Copy(engagement.KindWorkBlocked, "zh-CN")
	if err := rig.ch.SendMoment(context.Background(), engagementText{PushBody: copy.PushBody, Title: "给 Sam 的 Q3 回复"}); err != nil {
		t.Fatal(err)
	}
	body := rig.sender.messages()[0].body
	if strings.Contains(body, "Sam") || strings.Contains(body, "Q3") || !strings.Contains(body, "在电脑上打开 Isoform") {
		t.Fatalf("Chinese default text: %q", body)
	}
}
