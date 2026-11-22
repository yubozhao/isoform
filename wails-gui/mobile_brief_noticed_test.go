package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedBriefNoticed(t *testing.T, app *App, date string, count int) []NoticedSuggestion {
	t.Helper()
	day, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	var raws []noticedRaw
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("noticed-%s-%d", date, i)
		saveNoticedMoment(t, app, id, "Comparing Halcyon Arc desk lamps for Priya", "Safari", `[{"type":"person","name":"Priya"}]`, day.Add(time.Duration(10+i)*time.Hour))
		raw := noticedRaw{Kind: noticedKindFollowUp, MomentID: id, Text: fmt.Sprintf("Ask Priya about the %s Halcyon Arc lamp from %s", []string{"linen", "sage", "clay"}[i], day.Weekday()), Why: "Priya asked which finish fits the room."}
		if i == 1 {
			raw = noticedRoutineRaw(id, "Open the expense sheet", "Add the receipts")
		}
		raws = append(raws, raw)
	}
	gen, err := app.activity.store.saveSummaryWithSources(date, "daily", "The day.", "test", false, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	app.saveNoticed(date, "en", gen, raws)
	items, err := app.GetDayNoticed(date)
	if err != nil || len(items) != count {
		t.Fatalf("suggestions: %+v, %v", items, err)
	}
	return items
}

func briefWithNoticed(t *testing.T, app *App, now time.Time) MobileBriefV1 {
	t.Helper()
	in := mobileBriefTodayRefreshInput(app, now, time.Local)
	in.Slot = mobileBriefSlotMorning
	in.WindowStart = now.Add(-24 * time.Hour)
	collectMobileBriefToday(app, &in)
	brief := buildMobileBriefV1(in)
	brief.Offers = mobileBriefConversationOffers(brief, "English")
	brief.Cards = mobileBriefPatternCards(brief, "English")
	return brief
}

func TestMobileBriefNoticedCollectionTodayYesterdayAndCap(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	today := noticedTestDay
	now := noticedTestAt(20, 0)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	seedBriefNoticed(t, app, now.AddDate(0, 0, -2).Format("2006-01-02"), 1)
	seedBriefNoticed(t, app, yesterday, 3)
	seedBriefNoticed(t, app, today, 1)
	brief := briefWithNoticed(t, app, now)
	if len(brief.Sections.Noticed) != 2 || len(mobileBriefPicks(brief)) != 2 {
		t.Fatalf("noticed collection: %+v", brief)
	}
	refs := mobileBriefEvidenceByRef(brief)
	for i, date := range []string{today, yesterday} {
		item := brief.Sections.Noticed[i]
		if ref := refs[item.Refs[0]]; ref.SourceKind != mobileBriefNoticedSource || !strings.HasPrefix(ref.SourceID, date+"/") || item.At == "" || item.Detail == "" || item.Kind != "noticed" {
			t.Fatalf("source %d: %+v, %+v", i, item, ref)
		}
		if item.AttentionReason != "" || item.AttentionRank != 0 {
			t.Fatalf("suggestion needs attention: %+v", item)
		}
	}
	if len(mobileBriefAttentionItems(brief)) != 0 || len(mobileBriefCardRows(brief)) != 0 {
		t.Fatal("noticed suggestions became attention or chat rows")
	}
}

func TestMobileBriefNoticedIsLowestPickTier(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	seedBriefNoticed(t, app, noticedTestDay, 2)
	base := briefWithNoticed(t, app, noticedTestAt(20, 0))
	for _, higher := range []int{0, 4, 5, 6, 7} {
		t.Run(fmt.Sprint(higher), func(t *testing.T) {
			brief := base
			brief.Evidence = append([]MobileBriefRef{}, base.Evidence...)
			brief.Sections.WorkingToward = nil
			for i := 0; i < higher; i++ {
				id := fmt.Sprintf("goal-%d", i)
				brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: id, SourceKind: "purpose", SourceID: id})
				brief.Sections.WorkingToward = append(brief.Sections.WorkingToward, MobileBriefItem{ID: id, Kind: "goal", Title: "Review project " + id, LastTouch: brief.AsOf, Refs: []string{id}})
			}
			picks := mobileBriefPicks(brief)
			if len(picks) != min(6, higher+2) {
				t.Fatalf("picks = %+v", picks)
			}
			for i, p := range picks {
				want := mobileBriefPickGoal
				if i >= higher {
					want = mobileBriefPickNoticed
				}
				if p.Tier != want {
					t.Fatalf("pick %d: %+v", i, p)
				}
			}
			for _, offer := range mobileBriefConversationOffers(brief, "en") {
				if higher >= 2 && offer.Action == mobileBriefCardOpenActivity {
					t.Fatalf("suggestion displaced a higher button: %+v", offer)
				}
			}
		})
	}
}

func TestMobileBriefNoticedRefreshCardsAndPhone(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	items := seedBriefNoticed(t, app, noticedTestDay, 2)
	now := noticedTestAt(20, 0)
	base := briefWithNoticed(t, app, now)
	// Force a ref remap; the unrelated source consumes the first ref.
	base.Evidence = append([]MobileBriefRef{{Ref: "first", SourceKind: "schedule", SourceID: "other"}}, base.Evidence...)
	base.Sections.Schedules = []MobileBriefItem{{ID: "old", Title: "Unrelated routine", Refs: []string{"first"}}}
	fresh := refreshMobileBriefToday(app, base, now, time.Local)
	fresh = app.mobileBriefWithCards(app.mobileBriefForDelivery(fresh))
	if len(fresh.Sections.Noticed) != 2 || len(fresh.Cards) != 0 || len(fresh.Offers) != 2 || len(mobileBriefPicks(fresh)) != 2 {
		t.Fatalf("refresh lost noticed: %+v", fresh)
	}
	assertTodayRefsResolve(t, fresh)
	// Navigation offers remain on the document, but cannot masquerade as
	// prepared closing answers. Activity remains their execution boundary.
	for _, offer := range fresh.Offers {
		ref := mobileBriefEvidenceByRef(fresh)[offer.Refs[0]]
		if offer.Action != mobileBriefCardOpenActivity || offer.Target == nil || ref.SourceID != offer.Target.Date+"/"+offer.Target.SuggestionID {
			t.Fatalf("offer: %+v, ref: %+v", offer, ref)
		}
	}
	// Closing a different Purpose must preserve the optional section too.
	if got := mobileBriefDropClosedPurposes(fresh, map[string]bool{"unrelated": true}); len(got.Sections.Noticed) != 2 {
		t.Fatal("closed Purpose projection erased noticed")
	}
	phone := app.mobileBriefForPhone(fresh)
	if phone.ConversationKey != "" || len(phone.Cards) != 0 || len(phone.Offers) != 0 || !reflect.DeepEqual(phone.Narrative, fresh.Narrative) {
		t.Fatalf("phone projection: %+v", phone)
	}
	for _, route := range []string{"latest", "by-id"} {
		app.mobileBriefSvc = &mobileTestBriefSource{brief: &fresh}
		bridge := &mobileBridge{app: app}
		rec := httptest.NewRecorder()
		if route == "latest" {
			bridge.serveLatestBrief(rec)
		} else {
			bridge.serveBrief(rec, httptest.NewRequest(http.MethodGet, "/briefs/"+fresh.BriefID, nil), fresh.BriefID)
		}
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"open_activity"`) {
			t.Fatalf("phone %s: %d %s", route, rec.Code, rec.Body.String())
		}
	}
	// The source is checked at every model binding, not treated as a durable fact.
	ref := MobileBriefRef{SourceKind: mobileBriefNoticedSource, SourceID: noticedTestDay + "/" + items[0].ID}
	if digest, ok := app.mobileBriefModelSourceDigest(context.Background(), ref, fresh, todayChatSources{}); !ok || digest == "" {
		t.Fatal("readable noticed source cannot bind model text")
	}
}

func TestMobileBriefNoticedWithdrawalEveryReader(t *testing.T) {
	for _, change := range []string{"not-now", "delete", "summary-forget", "generation", "store-error", "closed-eyes"} {
		t.Run(change, func(t *testing.T) {
			app := newMobileBriefPurposeTestApp(t)
			items := seedBriefNoticed(t, app, noticedTestDay, 1)
			now := noticedTestAt(20, 0)
			base := briefWithNoticed(t, app, now)
			base.NarrativeStatus = "ok"
			base.Narrative = []MobileBriefPara{{Text: "Ask Priya about the Halcyon Arc lamps.", Refs: base.Sections.Noticed[0].Refs}}
			base.Audio = &MobileBriefAudio{}
			svc := &mobileBriefService{app: app, store: newMobileBriefStore(t.TempDir()), now: func() time.Time { return now }, location: time.Local}
			if err := svc.store.save(base, now); err != nil {
				t.Fatal(err)
			}
			ref := base.Evidence[0]
			if _, ok := app.mobileBriefNoticedSuggestion(ref.SourceID); !ok {
				t.Fatal("valid control not readable")
			}
			switch change {
			case "not-now":
				seedNoticedDismissal(t, app, items[0])
			case "delete":
				if err := app.activity.store.DeleteActivity(items[0].MomentID); err != nil {
					t.Fatal(err)
				}
			case "summary-forget":
				if _, err := app.activity.store.db.Exec(`DELETE FROM activity_summaries WHERE date = ?`, noticedTestDay); err != nil {
					t.Fatal(err)
				}
			case "generation":
				if _, err := app.activity.store.saveSummaryWithSources(noticedTestDay, "daily", "A replacement.", "test", false, "en", nil); err != nil {
					t.Fatal(err)
				}
			case "store-error":
				if _, err := app.activity.store.db.Exec(`DROP TABLE activity_noticed`); err != nil {
					t.Fatal(err)
				}
			case "closed-eyes":
				if err := app.SetClosedEyesModeEnabled(true); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := app.mobileBriefNoticedSuggestion(ref.SourceID); ok {
				t.Fatal("withdrawn evidence readable")
			}
			if _, ok := app.mobileBriefModelSourceDigest(context.Background(), ref, base, todayChatSources{}); ok {
				t.Fatal("withdrawn input still binds prose")
			}
			p, err := app.mcpReadPrivacy(context.Background())
			if err != nil && !errors.Is(err, errMCPReadPaused) {
				t.Fatal(err)
			}
			if err == nil {
				if _, ok := p.briefReferenceItem(ref); ok {
					t.Fatal("MCP admitted withdrawn suggestion")
				}
			}
			byID, ok := svc.GetMobileBrief(base.BriefID)
			if !ok {
				t.Fatal("missing saved brief")
			}
			latest, ok := svc.LatestMobileBrief()
			if !ok {
				t.Fatal("missing latest brief")
			}
			for _, got := range []MobileBriefV1{byID, latest, app.mobileBriefForPhone(base), refreshMobileBriefToday(app, base, now, time.Local)} {
				if len(got.Sections.Noticed) != 0 || len(got.Cards) != 0 || len(got.Offers) != 0 || len(got.Narrative) != 0 || got.Audio != nil {
					t.Fatalf("withdrawn suggestion survived reader: %+v", got)
				}
			}
		})
	}
}

func TestMobileBriefNoticedCardsArePatternsAndCannotSend(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	seedBriefNoticed(t, app, noticedTestDay, 2)
	base := briefWithNoticed(t, app, noticedTestAt(20, 0))
	for _, lang := range []string{"English", "Chinese"} {
		base.Offers = mobileBriefConversationOffers(base, lang)
		cards := mobileBriefPatternCards(base, lang)
		if len(cards) != 2 {
			t.Fatalf("cards: %+v", cards)
		}
		for _, card := range cards {
			want := "Add follow-up"
			if card.Target.Action == noticedKindRoutine {
				want = "Make a routine"
			}
			if lang == "Chinese" {
				want = "添加跟进"
				if card.Target.Action == noticedKindRoutine {
					want = "设为例行事务"
				}
			}
			if card.Label != want {
				t.Fatalf("label: %+v", card)
			}
			if prompt := mobileBriefCardPrompt(base, []MobileBriefCard{card}, lang); prompt != "" {
				t.Fatalf("model received Activity card: %s", prompt)
			}
			words := []mobileBriefCardWords{{ID: card.ID, Label: "Rewrite this label", Message: "Ask Priya about Halcyon Arc lamps"}}
			if got := mobileBriefApplyCardWords(base, []MobileBriefCard{card}, words, lang); !reflect.DeepEqual(got[0], card) {
				t.Fatal("model rewrote Activity card")
			}
			model := card
			model.Writer = mobileBriefCardWriterModel
			model.Label = "Rewrite this label"
			if got := mobileBriefCardsWithWritten([]MobileBriefCard{card}, []MobileBriefCard{model}); !reflect.DeepEqual(got[0], card) {
				t.Fatal("stored model words replaced pattern")
			}
		}
	}
	base.Offers = mobileBriefConversationOffers(base, "English")
	base.Cards = mobileBriefPatternCards(base, "English")
	dir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := newMobileBriefStore(dir).save(base, noticedTestAt(20, 0)); err != nil {
		t.Fatal(err)
	}
	for _, card := range base.Cards {
		ref := TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: card.ID, BriefID: base.BriefID}
		if _, err := app.resolveTodayChatBriefCard(context.Background(), ref, todayChatSources{}, time.Now()); !errors.Is(err, errTodayChatItemUnavailable) {
			t.Fatalf("Activity card entered chat: %v", err)
		}
	}
}

func TestMobileBriefNoticedOldJSONAndMCP(t *testing.T) {
	var old MobileBriefV1
	if err := json.Unmarshal([]byte(`{"version":1,"sections":{"moved":[]},"evidence":[]}`), &old); err != nil || old.Sections.Noticed != nil {
		t.Fatalf("old document: %+v, %v", old, err)
	}
	app := newMobileBriefPurposeTestApp(t)
	date := time.Now().Format("2006-01-02")
	items := seedBriefNoticed(t, app, date, 1)
	base := briefWithNoticed(t, app, time.Now())
	storeMCPBrief(t, base)
	got := callMCPReadTest(t, app, "brief_today", map[string]any{})
	if len(got.Items) != 1 || got.Items[0].Source != mobileBriefNoticedSource || got.Items[0].Title == nil || got.Items[0].Title.Text != items[0].Text {
		t.Fatalf("MCP readable control: %+v", got)
	}
	seedNoticedDismissal(t, app, items[0])
	got = callMCPReadTest(t, app, "brief_today", map[string]any{})
	if len(got.Items) != 0 || got.Withheld != 1 {
		t.Fatalf("MCP withdrawal: %+v", got)
	}
}

func TestMobileBriefNoticedPhonePushAndQueuedWithdrawal(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	items := seedBriefNoticed(t, app, noticedTestDay, 1)
	base := briefWithNoticed(t, app, noticedTestAt(20, 0))
	bridge := newMobileBriefPrivacyBridge(t, app)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	if err := bridge.publishBrief(base, false); err != nil {
		t.Fatal(err)
	}
	queued := bridge.pendingBriefs[phone.deviceID]
	before := phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, queued.Env)
	if strings.Contains(string(before), `"open_activity"`) || !strings.Contains(string(before), items[0].Text) {
		t.Fatalf("phone push: %s", before)
	}
	bridge.briefDeliveryMu.Lock()
	unchanged, err := bridge.refreshPendingBrief(queued)
	bridge.briefDeliveryMu.Unlock()
	if err != nil || unchanged.Env.Seq != queued.Env.Seq {
		t.Fatalf("unchanged phone revision changed: %d -> %d, %v", queued.Env.Seq, unchanged.Env.Seq, err)
	}
	seedNoticedDismissal(t, app, items[0])
	bridge.briefDeliveryMu.Lock()
	refreshed, err := bridge.refreshPendingBrief(queued)
	bridge.briefDeliveryMu.Unlock()
	if err != nil || refreshed.Env.Seq <= queued.Env.Seq {
		t.Fatalf("queued refresh: %+v, %v", refreshed, err)
	}
	after := phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, refreshed.Env)
	if strings.Contains(string(after), items[0].Text) || strings.Contains(string(after), `"open_activity"`) {
		t.Fatalf("queued withdrawal: %s", after)
	}
}

func TestMobileBriefNoticedPhoneKeepsOrdinaryChat(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	seedBriefNoticed(t, app, noticedTestDay, 1)
	seedMobileBriefReadableHistory(t, app, "ordinary")
	base := briefWithNoticed(t, app, noticedTestAt(20, 0))
	base.Evidence = append(base.Evidence, MobileBriefRef{Ref: "ordinary", SourceKind: "history", SourceID: "ordinary", Label: "Project brief"})
	base.Sections.OpenLoops = []MobileBriefItem{{ID: "ordinary", Kind: "one_time_work", Title: "Project brief", Status: "in_progress", Refs: []string{"ordinary"}}}
	base.NarrativeStatus = "unavailable"
	base.Narrative = nil
	base.Offers = mobileBriefConversationOffers(base, "English")
	// The ordinary work is not a pick; its offer still provides a valid chat
	// opener beside the noticed navigation offer, so phone chat stays usable.
	if picks := mobileBriefPicks(base); len(picks) != 1 || picks[0].Tier != mobileBriefPickNoticed {
		t.Fatalf("picks: %+v", picks)
	}
	desktop := app.mobileBriefWithCards(app.mobileBriefForDelivery(base))
	phone := app.mobileBriefForPhone(base)
	if phone.ConversationKey == "" || phone.ConversationKey != desktop.ConversationKey || len(phone.Offers) != 1 || phone.Offers[0].Action == mobileBriefCardOpenActivity {
		t.Fatalf("ordinary chat lost: %+v", phone)
	}
	if card, ok := mobileBriefFindCard(phone, mobileBriefOpenerCardID); !ok || card.Mode != mobileBriefCardSpeakFirst {
		t.Fatalf("ordinary opener: %+v, %t", card, ok)
	}
}

func TestMobileBriefNoticedLookupReadsEachDayOncePerPass(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	dates := []string{noticedTestDay, noticedTestAt(20, 0).AddDate(0, 0, -1).Format("2006-01-02")}
	items := []NoticedSuggestion{seedBriefNoticed(t, app, dates[0], 1)[0], seedBriefNoticed(t, app, dates[1], 1)[0]}
	calls := 0
	lookup := newMobileBriefNoticedLookup(app)
	lookup.read = func(date string) ([]NoticedSuggestion, error) {
		calls++
		return app.GetDayNoticed(date)
	}
	for i := 0; i < 80; i++ {
		day := i % len(dates)
		id := fmt.Sprintf("missing-%d", i)
		if i < len(dates) {
			id = items[day].ID
		}
		_, ok := lookup.suggestion(dates[day] + "/" + id)
		if ok != (i < len(dates)) {
			t.Fatalf("ref %d: readable = %v", i, ok)
		}
	}
	if calls != 2 {
		t.Fatalf("GetDayNoticed calls = %d, want 2", calls)
	}
	seedNoticedDismissal(t, app, items[0])
	if _, ok := newMobileBriefNoticedLookup(app).suggestion(dates[0] + "/" + items[0].ID); ok {
		t.Fatal("next pass admitted a withdrawn suggestion")
	}
}
