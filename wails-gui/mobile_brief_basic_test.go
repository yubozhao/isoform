package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The same fixture runs through build, seal, recording and the phone route in
// both modes. With the relay enabled the GET is encrypted by a paired device.
func TestBriefExtrasPackageGolden(t *testing.T) {
	relay := ""
	if os.Getenv("YANSU_MOBILE_E2E") == "1" {
		relay = startMobileE2ERelay(t)
	}
	for _, kind := range []string{"morning", "evening", "made_ahead", "calendar_only"} {
		t.Run(kind, func(t *testing.T) {
			var expected []MobileBriefPara
			var expectedSpeech []string
			for _, setting := range []string{"brief-extras", ""} {
				t.Run("features="+setting, func(t *testing.T) {
					t.Setenv(featureSwitchesEnv, setting)
					doc, speech, data := generateBasicBriefFixture(t, kind, relay)
					if setting != "" {
						expected = doc.Narrative
						expectedSpeech = speech
						if kind != "calendar_only" {
							if !strings.Contains(expected[len(expected)-1].Text, "?") || len(expectedSpeech) < 2 {
								t.Fatalf("missing rollback question %v %v", expected, expectedSpeech)
							}
							expected = expected[:len(expected)-1]
							expectedSpeech = expectedSpeech[:len(expectedSpeech)-1]
						}
					} else {
						if !reflect.DeepEqual(doc.Narrative, expected) || !reflect.DeepEqual(speech, expectedSpeech) {
							t.Fatalf("text %v want %v; speech %v want %v", doc.Narrative, expected, speech, expectedSpeech)
						}
						if strings.Contains(string(data), "fixture-chat-link") || len(doc.Cards) > 0 || doc.YourWeek != nil || doc.WeekQuestion != nil || len(doc.Sections.Watching) != 0 || !strings.Contains(string(data), `"watching":[]`) {
							t.Fatalf("extras in package: %s", data)
						}
					}
				})
			}
		})
	}
}

func basicBriefPhoneCopy(t *testing.T, app *App, phone *mobileE2EPhone, briefID string) (MobileBriefV1, json.RawMessage) {
	t.Helper()
	path := "/briefs/" + briefID
	var doc MobileBriefV1
	var data json.RawMessage
	if phone != nil {
		code, body := phone.request(t, "GET", path, "")
		var response struct{ Data json.RawMessage }
		if code != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil {
			t.Fatalf("phone: %d %s", code, body)
		}
		data = response.Data
	} else {
		rec := httptest.NewRecorder()
		newMobileBriefPrivacyBridge(t, app).serveBrief(rec, httptest.NewRequest(http.MethodGet, path, nil), briefID)
		data = packageRouteData(t, rec)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc, data
}

func generateBasicBriefFixture(t *testing.T, kind, relay string) (MobileBriefV1, []string, json.RawMessage) {
	t.Helper()
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	app := svc.app
	app.mobileBriefSvc = svc
	app.activity = &activityMonitorState{store: newTestStore(t)}
	slot := mobileBriefSlotMorning
	reason := mobileBriefReasonSchedule
	ctx := t.Context()
	var generation *MobileBriefGeneration
	switch kind {
	case "evening":
		slot = mobileBriefSlotEvening
		clock.Set(mobileBriefTestDay(18, 0))
	case "made_ahead":
		tomorrow := clock.Now().AddDate(0, 0, 1)
		ctx = context.WithValue(ctx, mobileBriefAheadDayKey{}, tomorrow)
		reason = mobileBriefReasonAhead
		generation = &MobileBriefGeneration{Reason: mobileBriefGenerationMadeAhead, ScheduledAt: formatMobileBriefTime(tomorrow)}
	case "calendar_only":
		calendarApp, calendarClock, _, _ := dayShapeHoldFixture(t)
		app = calendarApp
		svc.app, app.mobileBriefSvc = app, svc
		svc.now = calendarClock.Now
		svc.compose = nil
		svc.gather = func(_ context.Context, slot string, start, end time.Time) (mobileBriefInput, error) {
			in := mobileBriefInput{Slot: slot, WindowStart: start, AsOf: end, Location: time.UTC}
			collectMobileBriefCalendar(app, &in)
			return in, nil
		}
	}
	if kind != "calendar_only" {
		gather := svc.gather
		svc.gather = func(ctx context.Context, slot string, start, end time.Time) (mobileBriefInput, error) {
			in, err := gather(ctx, slot, start, end)
			in.Moved[0].ChatSessionID = "fixture-chat-link"
			return in, err
		}
		svc.compose = func(_ context.Context, _ *App, b MobileBriefV1, cards []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
			if len(b.Evidence) == 0 {
				t.Fatal("writer received no evidence")
			}
			if !featureOn(featureBriefExtras) && len(b.Sections.Moved) > 0 && b.Sections.Moved[0].ChatSessionID != "" {
				t.Fatal("writer received chat link")
			}
			refs := []string{b.Evidence[0].Ref}
			return []MobileBriefPara{{Text: mobileBriefPublishableTestReading, Refs: refs}, {Text: "Want to review the plan?", Refs: refs}}, "ok", nil
		}
	}
	var speech []string
	svc.speaker = fakeSpeaker("en", nil, nil)
	svc.speaker.synthesize = func(_ context.Context, _, words string) ([]byte, error) {
		speech = append(speech, words)
		return testWAV(0.25), nil
	}
	var phone *mobileE2EPhone
	if relay != "" {
		t.Setenv("ISOFORM_RELAY_URL", "")
		phone = pairMobileE2EPhone(t, app, relay)
		t.Cleanup(app.mobileBridgeForUse().Stop)
	}
	b, err := svc.generateWithMetadata(ctx, slot, reason, generation)
	if err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	sealed, ok, err := svc.store.loadFinishedRead(b.BriefID)
	if !ok || err != nil {
		t.Fatalf("seal missing for %s: %v", b.BriefID, err)
	}
	var stored MobileBriefV1
	if err := unmarshalStoredMobileBrief(sealed.Brief, &stored); err != nil {
		t.Fatal(err)
	}
	if !featureOn(featureBriefExtras) && strings.Contains(string(sealed.Brief), "fixture-chat-link") {
		t.Fatal("chat link reached the seal")
	}
	doc, data := basicBriefPhoneCopy(t, app, phone, b.BriefID)
	if len(doc.Narrative) == 0 || !reflect.DeepEqual(doc.Narrative, stored.Narrative) || doc.Slot != slot || mobileBriefIsAhead(b) != (kind == "made_ahead") || doc.LocalDate != b.LocalDate {
		t.Fatalf("build %v seal %v phone %v generation %+v", b.Narrative, stored.Narrative, doc.Narrative, doc.Generation)
	}
	if kind == "calendar_only" {
		if len(doc.Sections.DayAhead) == 0 || doc.NarrativeStatus != "skipped" || len(speech) != 0 || doc.Audio != nil && doc.Audio.Bytes > 0 {
			t.Fatalf("calendar fallback missing: %+v", doc)
		}
	} else {
		if len(speech) == 0 || doc.Audio == nil || len(doc.Audio.Paragraphs) != len(doc.Narrative) {
			t.Fatalf("audio missing: %+v script %v", doc.Audio, speech)
		}
		if phone != nil {
			checkMobileE2ERecording(t, phone, doc.Audio)
		}
		marks := mobileBriefAudioMarks(doc)
		if len(marks) == 0 {
			t.Fatal("no read-along marks")
		}
		for _, m := range marks {
			if m.Paragraph < 0 || m.Paragraph >= len(doc.Narrative) || m.From < 0 || m.From >= m.To || m.To > len(doc.Narrative[m.Paragraph].Text) {
				t.Fatalf("invalid mark %+v", m)
			}
		}
	}
	return doc, speech, data
}

func TestBriefExtrasSourceBoundary(t *testing.T) {
	featuresOff(t)
	in := observedBriefFixture()
	chat := mobileBriefCandidate{SourceKind: "chat_message", SourceID: "chat-only-7", Title: "Marigold chat report", At: in.AsOf.Add(-time.Minute)}
	in.Watching = []mobileBriefCandidate{{SourceKind: mobileBriefFindSource, SourceID: "web-only-8", Title: "Marigold web finding"}}
	in.OpenLoops = []mobileBriefCandidate{chat, {SourceKind: "purpose", SourceID: "release", Title: "Review the release", At: chat.At, ChatSessionID: "thread-only-9", ChatMessageID: chat.SourceID, Evidence: []mobileBriefCandidate{chat}}}
	in.Conversations = []mobileBriefCandidate{chat}
	basic := mobileBriefBasicInput(in)
	b := buildMobileBriefV1(basic)
	material, sources := newBriefMaterial(basic, b, "", "")
	if len(material.Items) == 0 || len(b.Evidence) == 0 || len(basic.OpenLoops) != 1 || basic.OpenLoops[0].SourceID != "release" {
		t.Fatal("retained material missing")
	}
	for _, source := range sources {
		if source.kind == "chat_message" {
			t.Fatal("chat source reached writer")
		}
	}
	for _, value := range []any{b, material} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, cut := range []string{"chat-only-7", "web-only-8", "thread-only-9", "Marigold"} {
			if strings.Contains(string(raw), cut) {
				t.Fatalf("cut source %s reached brief or writer", cut)
			}
		}
	}
}

func TestBriefExtrasEntryPoints(t *testing.T) {
	featuresOff(t)
	app := &App{}
	if app.GetFeatureSwitches().BriefExtras {
		t.Fatal("brief extras enabled by default")
	}
	if status, err := app.startTopicFinds("daily"); status != "off" || err != nil {
		t.Fatalf("daily search: %s %v", status, err)
	}
	app.maybeStartDailyTopicFinds("brief")
	if app.topics != nil {
		t.Fatal("daily search initialized the topic service")
	}
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)})
	settings := svc.config.load()
	settings.ShadowCompose, settings.NewBrief = true, false
	if err := svc.config.save(settings); err != nil {
		t.Fatal(err)
	}
	svc.startBriefShadow(MobileBriefV1{})
	if svc.shadowCtx != nil || !svc.newBriefEnabled() {
		t.Fatal("legacy writer or shadow enabled in basic mode")
	}
	b := cardsFixture(t, "English")
	question, offers := mobileBriefClosing(b, "English")
	if question.Text != "" || len(offers) == 0 || len(mobileBriefPatternCards(b, "English")) > 0 {
		t.Fatal("basic brief needs offers without a question or cards")
	}
	p := []MobileBriefPara{{Text: "Review the release.", Refs: offers[0].Refs}}
	if workOfferParagraph(p, offers[0].Refs) != nil {
		t.Fatal("basic work offer has a paragraph")
	}
	t.Setenv(featureSwitchesEnv, "brief-extras")
	if !app.GetFeatureSwitches().BriefExtras || svc.newBriefEnabled() {
		t.Fatal("rollback did not restore previous writer setting")
	}
	question, restored := mobileBriefClosing(b, "English")
	if question.Text == "" || !reflect.DeepEqual(offers, restored) || len(mobileBriefPatternCards(b, "English")) == 0 || workOfferParagraph(p, offers[0].Refs) == nil {
		t.Fatal("rollback failed to restore questions, cards or placement")
	}
}
