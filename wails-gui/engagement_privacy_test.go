package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

func TestEngagementRedactsBeforeStorageAndDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	params := map[string]string{"title": "Card 4111 1111 1111 1111", "question": "password: secretvalue OTP: 123456"}
	// Routed to the phone, so it rings (notify v1.0.0 B3 keeps blocked work quiet otherwise).
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "redact", Params: params, Channels: []engagement.Channel{engagement.ChannelPush}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"4111 1111 1111 1111", "secretvalue", "123456"} {
		if strings.Contains(row.Params, secret) {
			t.Fatalf("stored secret %q in %s", secret, row.Params)
		}
	}
	if !strings.Contains(params["title"], "4111") {
		t.Fatal("sanitization must not mutate the producer's parameters")
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 || !strings.Contains(rig.pushed[0].Title, "[REDACTED:") {
		t.Fatalf("redacted delivery: %+v", rig.pushed)
	}
}

func TestEngagementSuppressesSensitiveSourcesBeforeStorage(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	before := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(before) })
	pii.SetUserSensitiveApps(map[string]string{"com.apple.mail": "Mail", "org.private.journal": "Private Journal"})
	for i, m := range []engagement.Moment{
		{Kind: engagement.KindWorkBlocked, Params: map[string]string{"app_bundle_id": "com.1password.desktop", "title": "vault secret"}},
		{Kind: engagement.KindWorkBlocked, Params: map[string]string{"app_name": "Private Journal", "title": "private entry"}},
		{Kind: engagement.KindWorkBlocked, Params: map[string]string{"apps": `["Safari", "Private Journal"]`, "title": "private entry"}},
		{Kind: engagement.KindWaitingOnReply, Params: map[string]string{"name": "Dana", "subject": "private subject"}},
	} {
		m.SubjectID = fmt.Sprint(i)
		row, err := rig.svc.Submit(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != storage.EngagementStateSuppressed || row.Params != "{}" {
			t.Fatalf("sensitive source persisted: %+v", row)
		}
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatal("sensitive source delivered")
	}
}

func TestEngagementRedactsLegacyRowsBeforeDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	row, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(engagement.KindWorkFinished), DedupeKey: "legacy:redact", SubjectID: "legacy", CreatedAt: rig.clock.UnixMilli(),
		Params: `{"title":"Card 4111 1111 1111 1111","question":"your answer."}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored == nil || strings.Contains(stored.Params, "4111") || len(rig.pushed) != 1 || strings.Contains(rig.pushed[0].Title, "4111") {
		t.Fatalf("legacy redaction failed: %+v %+v %v", stored, rig.pushed, err)
	}
}

func TestEngagementToastIsGenericWhileLocked(t *testing.T) {
	text := engagementText{PushBody: "Work is waiting on you.", Title: "The private acquisition needs an answer."}
	title, body := engagementToastCopy(text, true)
	if title != text.PushBody || body != "" {
		t.Fatalf("locked toast exposed specifics: %q %q", title, body)
	}
	if title, body := engagementToastCopy(text, false); title != text.PushBody || body != text.Title {
		t.Fatal("unlocked toast lost its specific body")
	}
}

func TestEngagementRenderedCopyFallsBackWhenUnsafe(t *testing.T) {
	for _, body := range []string{"Yansu is ready", "Ready!", "An urgent update", strings.Repeat("word ", 61), "Ready \U0001f600"} {
		got := sanitizeEngagementText(engagementText{PushBody: "Your brief is ready.", Title: body})
		if got.Title != got.PushBody {
			t.Fatalf("unsafe body survived: %q", got.Title)
		}
	}
}

func TestEngagementTextsAreGenericWithoutNamesForEveryKind(t *testing.T) {
	sms := newSMSRig(t)
	sms.subscribe(t)
	messages := newTextChannelRig(t)
	ctx := context.Background()
	for _, kind := range engagement.AllKinds() {
		m := engagement.Moment{Kind: kind, Params: map[string]string{"title": "Private plan", "name": "Dana", "subject": "Private subject"}}
		if err := sendEngagementText(ctx, sms.ch, messages.ch, storage.EngagementMomentRow{ID: string(kind)}, m, engagement.Verdict{}, engagementText{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, kind := range engagement.AllKinds() {
		want, _ := engagement.GenericText(kind, "en")
		if got := sms.relay.sent()[i]; got != want {
			t.Fatalf("%s text = %q, want %q", kind, got, want)
		}
	}
	if err := saveTextChannelConfig(messages.ch.dir, TextChannelConfig{Enabled: true, IncludeNames: true}); err != nil {
		t.Fatal(err)
	}
	m := engagement.Moment{Kind: engagement.KindWaitingOnReply, Params: map[string]string{"name": "Dana", "subject": "Private plan"}}
	if err := sendEngagementText(ctx, sms.ch, messages.ch, storage.EngagementMomentRow{ID: "names-on"}, m, engagement.Verdict{}, engagementText{}); err != nil {
		t.Fatal(err)
	}
	texts := sms.relay.sent()
	if !strings.Contains(texts[len(texts)-1], "Dana") {
		t.Fatal("enabled names should retain specifics")
	}
}

func TestEngagementMessagesFallbackKeepsGenericCopyWhenNamesTurnOff(t *testing.T) {
	sms := newSMSRig(t)
	sms.subscribe(t)
	messages := newTextChannelRig(t)
	messages.enable(t)
	cfg, err := loadTextChannelConfig(messages.ch.dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.IncludeNames = true
	if err := saveTextChannelConfig(messages.ch.dir, cfg); err != nil {
		t.Fatal(err)
	}
	switched := false
	// Active checks the relay after sendEngagementText reads NamesAllowed.
	// Revoking names here deterministically exercises the later settings read
	// in Messages, without a timer or a competing goroutine.
	sms.ch.relay = func() smsRelay {
		cfg.IncludeNames = false
		if err := saveTextChannelConfig(messages.ch.dir, cfg); err != nil {
			t.Fatal(err)
		}
		switched = true
		return nil
	}
	text := engagementText{PushBody: "Work is waiting on you.", Title: "Dana needs your private acquisition plan."}
	m := engagement.Moment{Kind: engagement.KindWorkBlocked}
	v := engagement.Verdict{Mode: engagement.ModeInterrupt, Interruption: engagement.InterruptionTimeSensitive}
	if err := sendEngagementText(context.Background(), sms.ch, messages.ch, storage.EngagementMomentRow{ID: "names-revoked"}, m, v, text); err != nil {
		t.Fatal(err)
	}
	want, err := textChannelBody(text, false)
	if err != nil {
		t.Fatal(err)
	}
	sent := messages.sender.messages()
	if !switched || len(sent) != 1 || sent[0].body != want || strings.Contains(sent[0].body, "Dana") {
		t.Fatalf("fallback exposed names after revocation: switched=%t messages=%+v", switched, sent)
	}
}

func TestEngagementRelayTextsStayGenericWhileMessagesIsDisabled(t *testing.T) {
	sms := newSMSRig(t)
	sms.subscribe(t)
	messages := newTextChannelRig(t)
	if err := saveTextChannelConfig(messages.ch.dir, TextChannelConfig{Enabled: false, IncludeNames: true}); err != nil {
		t.Fatal(err)
	}
	if messages.ch.NamesAllowed() {
		t.Error("disabled Messages retained permission to name things")
	}
	m := engagement.Moment{Kind: engagement.KindWorkBlocked, Params: map[string]string{"title": "Private acquisition", "question": "Dana's decision."}}
	if err := sendEngagementText(context.Background(), sms.ch, messages.ch, storage.EngagementMomentRow{ID: "messages-disabled"}, m, engagement.Verdict{}, engagementText{}); err != nil {
		t.Fatal(err)
	}
	want, _ := engagement.GenericText(m.Kind, "en")
	if sent := sms.relay.sent(); len(sent) != 1 || sent[0] != want {
		t.Fatalf("relay used names while Messages was disabled: %q", sent)
	}
}

func TestEngagementRoutineStatusDoesNotRingADoorbell(t *testing.T) {
	rig := newEngagementTestRig(t)
	reports := 0
	rig.svc.deliver.Report = func(m engagement.Moment, _ engagementText) bool {
		if m.ID == "" {
			t.Fatal("report lost its stored moment id")
		}
		reports++
		return true
	}
	ctx := context.Background()
	for _, kind := range []engagement.Kind{engagement.KindCapturePaused, engagement.KindBriefMissed, engagement.KindPlaybookResult, engagement.KindWorkFinished} {
		if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: kind, SubjectID: string(kind), CreatedAt: rig.clock.Add(-20 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	rig.svc.Pass(ctx)
	if reports != 4 || len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatalf("reports=%d push=%d desktop=%d", reports, len(rig.pushed), len(rig.desktop))
	}
}

func TestEngagementRoutineResultPreservesEveryRequestedChannel(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.presence.SMSActive, rig.presence.EmailActive = true, true
	reports, texts, emails := 0, 0, 0
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool { reports++; return true }
	rig.svc.deliver.Text = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		texts++
		return nil
	}
	rig.svc.deliver.Email = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		emails++
		return nil
	}
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindPlaybookResult, SubjectID: "requested-results", Channels: engagement.ChoosableChannels()})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored == nil || stored.State != storage.EngagementStateSent {
		t.Fatalf("requested result state: %+v %v", stored, err)
	}
	for _, channel := range engagement.ChoosableChannels() {
		if !strings.Contains("+"+stored.Channel+"+", "+"+string(channel)+"+") {
			t.Errorf("requested channel %s missing from %q", channel, stored.Channel)
		}
	}
	if reports != 1 || texts != 1 || emails != 1 || len(rig.pushed) != 1 || len(rig.desktop) != 1 {
		t.Fatalf("requested deliveries report=%d push=%d desktop=%d text=%d email=%d", reports, len(rig.pushed), len(rig.desktop), texts, emails)
	}
}

func TestEngagementRoutineResultRetriesOnlyTheRequestedChannelThatFailed(t *testing.T) {
	rig := newEngagementTestRig(t)
	reports := 0
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool { reports++; return true }
	rig.pushError = fmt.Errorf("phone offline")
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindPlaybookResult, SubjectID: "requested-retry", Channels: []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelPush}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored == nil || stored.State != storage.EngagementStateScheduled || stored.Channel != "desktop" {
		t.Fatalf("partial requested result: %+v %v", stored, err)
	}
	rig.pushError = nil
	rig.clock = rig.clock.Add(engagementRetryDelay)
	rig.svc.Pass(ctx)
	stored, err = rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored == nil || stored.State != storage.EngagementStateSent || stored.Channel != "desktop+push" {
		t.Fatalf("retried requested result: %+v %v", stored, err)
	}
	if reports != 1 || len(rig.pushed) != 1 || len(rig.desktop) != 1 {
		t.Fatalf("duplicate requested deliveries report=%d push=%d desktop=%d", reports, len(rig.pushed), len(rig.desktop))
	}
}
