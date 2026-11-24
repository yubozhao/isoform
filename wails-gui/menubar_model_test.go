package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMenuBarIconLifecycle(t *testing.T) {
	var attention menuBarAttention
	steps := []struct {
		paused, open bool
		ids          []string
		want         menuBarIconState
	}{
		{false, false, nil, menuBarIconState{1, false, false, false}},
		{true, false, nil, menuBarIconState{.45, false, false, false}},
		{false, false, []string{"a"}, menuBarIconState{1, true, false, false}},
		{false, true, []string{"a"}, menuBarIconState{1, false, true, false}},
		{false, false, []string{"a"}, menuBarIconState{1, false, false, false}},
		{true, false, []string{"b"}, menuBarIconState{.45, true, false, false}},
		{true, true, []string{"b"}, menuBarIconState{.45, false, true, false}},
		{false, false, nil, menuBarIconState{1, false, false, false}},
		{false, false, []string{"b"}, menuBarIconState{1, true, false, false}},
	}
	for i, step := range steps {
		if got := attention.icon(step.paused, step.open, step.ids); got != step.want {
			t.Errorf("step %d: %+v want %+v", i, got, step.want)
		}
	}
}

// Right-click and Control-click: the short native menu. Closed Eyes is the
// one way to stop seeing (design S2, 2026-11-21): the phone's three lengths
// while eyes are open, Open eyes while they're closed. "Pause" goes.
func TestMenuBarMenuModel(t *testing.T) {
	want := map[string]map[bool][]string{
		"en": {
			false: {"Open Isoform", "Make my brief now", "Closed Eyes for 15 minutes", "Closed Eyes for an hour", "Closed Eyes until I end it", "Settings…", "Quit Isoform"},
			true:  {"Open Isoform", "Make my brief now", "Open eyes", "Settings…", "Quit Isoform"},
		},
		"zh-CN": {
			false: {"打开 Isoform", "立即生成简报", "Closed Eyes 15 分钟", "Closed Eyes 1 小时", "Closed Eyes，直到我结束", "设置…", "退出 Isoform"},
			true:  {"打开 Isoform", "立即生成简报", "睁眼", "设置…", "退出 Isoform"},
		},
	}
	wantIDs := map[bool][]string{
		false: {"open", "make", "closedEyes15", "closedEyes60", "closedEyes", "settings", "quit"},
		true:  {"open", "make", "resume", "settings", "quit"},
	}
	for _, lang := range []string{"en", "zh-CN"} {
		for _, paused := range []bool{false, true} {
			ids, titles := []string{}, []string{}
			byID := map[string]menuBarMenuItem{}
			for _, row := range menuBarMenu(lang, paused) {
				if !row.Separator {
					ids = append(ids, row.ID)
					titles = append(titles, row.Title)
					byID[row.ID] = row
				}
			}
			if !reflect.DeepEqual(ids, wantIDs[paused]) {
				t.Fatalf("%s paused=%v ids %v", lang, paused, ids)
			}
			if !reflect.DeepEqual(titles, want[lang][paused]) {
				t.Fatalf("%s paused=%v titles %v", lang, paused, titles)
			}
			for _, title := range titles {
				if lower := strings.ToLower(title); strings.Contains(lower, "pause") || strings.Contains(lower, "observing") || strings.Contains(title, "暂停") || strings.Contains(title, "观察") {
					t.Errorf("%s menu item %q still says Pause or observing", lang, title)
				}
			}
			for id, key := range map[string]string{"open": "i", "settings": ",", "quit": "q"} {
				if byID[id].Key != key {
					t.Errorf("%s key %+v", id, byID[id])
				}
			}
			if !reflect.DeepEqual(byID["open"].Modifiers, []string{"command", "shift"}) {
				t.Fatal("open shortcut changed")
			}
		}
	}
	if len(menuBarCopy["en"]) != len(menuBarCopy["zh-CN"]) {
		t.Fatal("copy catalogs differ")
	}
	for k, v := range menuBarCopy["en"] {
		if v == "" || menuBarCopy["zh-CN"][k] == "" {
			t.Errorf("missing copy %q", k)
		}
	}
	for k := range menuBarCopy["zh-CN"] {
		if _, ok := menuBarCopy["en"][k]; !ok {
			t.Errorf("zh-CN key %q has no English", k)
		}
	}
	// Retired in 1.0, and the waiting list and Pause words retired with Tasks
	// and design S2.
	for _, retired := range []string{"Pause 1 hour", "Your spoken brief is not ready yet", "Loading...", "Show menu", "Could not read the menu bar. Open Isoform to try again.", "Settings...",
		"Pause for 15 minutes", "Pause 15 min", "Resume observing", "Waiting on you", "See all {n} in Chat", "Nothing is waiting on you."} {
		for _, v := range menuBarCopy["en"] {
			if v == retired {
				t.Errorf("retired string %q is back", retired)
			}
		}
	}
	for _, key := range []string{"pause15", "resumeMenu", "pause", "resume", "waiting", "seeAll", "empty"} {
		if _, ok := menuBarCopy["en"][key]; ok {
			t.Errorf("retired key %q is back", key)
		}
	}
}

// The popover's restart row ends at Restart now, so its copy has no trailing dot.
func TestMenuBarUpdateRestartCopy(t *testing.T) {
	if got := menuBarWords("en")["updateRestart"]; got != "{version} is ready" {
		t.Fatalf("en restart row %q", got)
	}
	if got := menuBarWords("zh-CN")["updateRestart"]; got != "{version} 已准备好" {
		t.Fatalf("zh-CN restart row %q", got)
	}
}

// The brief row's states, from the newest brief and the schedule.
func TestMenuBarBriefRow(t *testing.T) {
	now := time.Date(2026, 10, 27, 8, 40, 0, 0, time.Local)
	w := menuBarWords("en")
	// Play needs the words as well as the recording.
	morning := MobileBriefV1{BriefID: "2026-10-27-morning", Slot: mobileBriefSlotMorning, LocalDate: "2026-10-27", AsOf: time.Date(2026, 10, 27, 7, 0, 0, 0, time.Local).Format(time.RFC3339),
		Narrative: []MobileBriefPara{{Text: "Good morning. Three meetings today."}},
		Audio:     &MobileBriefAudio{Bytes: 10, Status: mobileBriefAudioStatusOK, DurationSeconds: 48}}
	if got := menuBarBriefRow(w, "en", morning, true, false, MobileBriefHealth{}, now); got.Kind != "ready" || got.Title != "Hear your morning brief" || got.Meta != "Today, 7:00 AM · 0:48" || got.ID != morning.BriefID {
		t.Fatalf("ready %+v", got)
	}
	evening := MobileBriefV1{BriefID: "2026-10-27-evening", Slot: mobileBriefSlotEvening, LocalDate: "2026-10-27", AsOf: time.Date(2026, 10, 27, 19, 30, 0, 0, time.Local).Format(time.RFC3339)}
	if got := menuBarBriefRow(w, "en", evening, true, false, MobileBriefHealth{}, now); got.Kind != "read" || got.Title != "Read your brief" || got.Meta != "Today, 7:30 PM" {
		t.Fatalf("read %+v", got)
	}
	next := time.Date(2026, 10, 28, 7, 0, 0, 0, time.Local).UTC().Format(time.RFC3339)
	// The current brief stays on the menu bar on an earlier day (the same one
	// Today and Chat show).
	yesterday := morning
	yesterday.LocalDate = "2026-10-26"
	if got := menuBarBriefRow(w, "en", yesterday, true, false, MobileBriefHealth{NextRunAt: next}, now); got.Kind != "ready" || got.ID != morning.BriefID {
		t.Fatalf("an earlier day's current brief %+v", got)
	}
	if got := menuBarBriefRow(w, "en", MobileBriefV1{}, false, false, MobileBriefHealth{NextRunAt: next}, now); got.Kind != "none" || got.Title != "No brief yet today" || got.Meta != "Next brief tomorrow at 7:00 AM" {
		t.Fatalf("none %+v", got)
	}
	if got := menuBarBriefRow(w, "en", morning, true, true, MobileBriefHealth{}, now); got.Kind != "making" || got.Title != "Making your brief now." || got.Meta != "" {
		t.Fatalf("making %+v", got)
	}
	if got := menuBarBriefRow(menuBarWords("zh-CN"), "zh-CN", evening, true, false, MobileBriefHealth{}, now); got.Title != "阅读简报" || got.Meta != "今天 19:30" {
		t.Fatalf("zh-CN read %+v", got)
	}
	if got := menuBarLabel(w, "Observing", 3); got != "Isoform, observing, 3 waiting on you" && got != "Isoform, Observing, 3 waiting on you" {
		t.Fatalf("label %q", got)
	}
}

// Tasks › Needs you is gone (computer-map 3.4): nothing in the menu bar reads
// it. Next, the label's count and the icon's dot all come from Your work's
// open items.
func TestMenuBarReadsNoNeedsYou(t *testing.T) {
	files, err := filepath.Glob("menubar_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("menubar files %v %v", files, err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"GetNeedsYou", "menuBarNeedItems", "menuBarNeedLines", "menuBarNeedNoChat", "menuBarWaitingItem"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s still uses %s", file, banned)
			}
		}
	}
}

func TestMenuBarDotAndCountComeFromTheWorkList(t *testing.T) {
	w := menuBarWords("en")
	list := WorkList{Items: []WorkItem{
		{ID: "done", State: "answered"},
		{ID: "a", State: workStateOpen, Source: workSourceDayOne, Work: WorkBody{Title: "Prepare the 2:00 with Maya"}},
		{ID: "b", State: workStateOpen, Source: workSourceDayOne},
	}}
	open := menuBarOpenWork(list)
	ids := []string{}
	for _, item := range open {
		ids = append(ids, item.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("open ids %v", ids)
	}
	// The first day's next item: "Next · 1 of 2 · Day one" (20d).
	if got := menuBarNextItem(w, list); got == nil || got.Line != "Next · 1 of 2 · Day one" || got.ID != "a" {
		t.Fatalf("day one next %+v", got)
	}
	if got := menuBarNextItem(menuBarWords("zh-CN"), list); got == nil || !strings.HasSuffix(got.Line, "· Day one") {
		t.Fatalf("zh-CN day one next %+v", got)
	}
	// A new open work item lights the dot; seeing it clears it.
	var attention menuBarAttention
	if attention.icon(false, false, ids[:1]).Dot != true {
		t.Fatal("the first open item did not light the dot")
	}
	attention.icon(false, true, ids[:1])
	if attention.icon(false, false, ids[:1]).Dot {
		t.Fatal("a seen item kept the dot")
	}
	if !attention.icon(false, false, ids).Dot {
		t.Fatal("a new open work item did not light the dot")
	}
	view := menuBarView{Words: w, State: "seeing", Waiting: len(open)}
	if got := menuBarViewLabel(&view); got != "Isoform, seeing, 2 waiting on you" {
		t.Fatalf("label %q", got)
	}
}

// pass-access v1.1.0 rule 3: while a trouble line is up, the menu bar item
// reads "Isoform, {the line}", so the amber dot has words.
func TestMenuBarLabelSaysTheTroubleLine(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		w := menuBarWords(lang)
		line := menuBarFill(w["troubleScreen"], "time", "3:10 PM") + " " + w["troubleScreenWhy"]
		view := menuBarView{Words: w, State: "seeing", Waiting: 3, SignOut: &menuBarSignOut{Kind: troubleScreen, Line: line}}
		want := map[string]string{"en": "Isoform, " + line, "zh-CN": "Isoform，" + line}[lang]
		if got := menuBarViewLabel(&view); got != want {
			t.Fatalf("%s label %q", lang, got)
		}
		// The held list's label keeps the trouble words too.
		var memory menuBarWaitingMemory
		memory.settle(&view, true, context.DeadlineExceeded, time.Now())
		if view.Label != want {
			t.Fatalf("%s settled label %q", lang, view.Label)
		}
		view.SignOut = nil
		if got := menuBarViewLabel(&view); got != menuBarLabel(w, "seeing", view.Waiting) {
			t.Fatalf("%s label without trouble %q", lang, got)
		}
	}
}

// Design S8: the state line is the mark's words, then the watch's part.
// No permission is "not seeing" (with the trouble row); the person's own
// screen switch is "screen off"; an unknown screen state is the watch's part
// alone, never "Starting…" or "off".
func TestMenuBarStatusUsesTheMarksWords(t *testing.T) {
	previous, _ := menuBarWatch.Load().(string)
	t.Cleanup(func() { menuBarWatch.Store(previous) })
	menuBarWatch.Store("watchListening")
	for _, lang := range []string{"en", "zh-CN"} {
		w := menuBarWords(lang)
		watch := w["watchListening"]
		for _, tc := range []struct {
			name                                  string
			paused, trouble, screenOff, observing bool
			status, state                         string
		}{
			{"seeing", false, false, false, true, "observing", w["observing"] + " · " + watch},
			{"no permission", false, true, false, false, "not_seeing", w["notSeeing"] + " · " + watch},
			{"no permission while capture runs", false, true, false, true, "not_seeing", w["notSeeing"] + " · " + watch},
			{"screen switched off", false, false, true, true, "screen_off", w["screenOff"] + " · " + watch},
			{"not known yet", false, false, false, false, "starting", watch},
			{"closed eyes", true, true, true, true, "paused", w["paused"] + " · " + watch},
		} {
			status, state := menuBarStatus(w, lang, DoingNow{}, tc.paused, tc.trouble, tc.screenOff, true, tc.observing)
			if status != tc.status || state != tc.state {
				t.Errorf("%s %s: %q %q, want %q %q", lang, tc.name, status, state, tc.status, tc.state)
			}
		}
	}
	if got := menuBarWords("en")["screenOff"] + " · " + menuBarWords("en")["watchListening"]; got != "screen off · watch listening" {
		t.Fatalf("screen off line %q", got)
	}
	// Nothing known at all: an empty line, and the label is just the name.
	menuBarWatch.Store("")
	if _, state := menuBarStatus(menuBarWords("en"), "en", DoingNow{}, false, false, false, true, false); state != "" {
		t.Fatalf("unknown state %q", state)
	}
	if got := menuBarLabel(menuBarWords("en"), "", 0); got != "Isoform" {
		t.Fatalf("label with no state %q", got)
	}
	if got := menuBarLabel(menuBarWords("en"), "", 2); got != "Isoform, 2 waiting on you" {
		t.Fatalf("label with no state and a count %q", got)
	}
}

// Design S8: "Observing", "Starting" and "Turn on" go from the popover.
func TestMenuBarWordsSayNoObservingStartingOrTurnOn(t *testing.T) {
	for lang, words := range menuBarCopy {
		for key, v := range words {
			lower := strings.ToLower(v)
			for _, banned := range []string{"observing", "starting", "turn on", "pause"} {
				if strings.Contains(lower, banned) {
					t.Errorf("%s %s = %q says %q", lang, key, v, banned)
				}
			}
		}
		for _, key := range []string{"off", "starting", "turnOn"} {
			if _, ok := words[key]; ok {
				t.Errorf("%s retired key %q is back", lang, key)
			}
		}
	}
	for _, banned := range []string{"turnOn", "w.turnOn", "'off'"} {
		if strings.Contains(menuBarHTML, banned) {
			t.Errorf("popover page still has %s", banned)
		}
	}
}

// computer-settings v1.2.0 20d: before the first brief, the brief row is a
// clock, "Your first brief" and "at {time}".
func TestMenuBarFirstBriefRow(t *testing.T) {
	at := time.Date(2026, 11, 21, 19, 30, 0, 0, time.Local)
	first := FirstBriefTime{At: at.Format(time.RFC3339), Slot: mobileBriefSlotEvening}
	for lang, meta := range map[string]string{"en": "at 7:30 PM", "zh-CN": "19:30"} {
		row, ok := menuBarFirstBriefRow(menuBarWords(lang), lang, first)
		if !ok || row.Kind != "first" || row.Title != menuBarWords(lang)["firstBrief"] || row.Meta != meta {
			t.Fatalf("%s first brief row %+v", lang, row)
		}
	}
	if row := menuBarWords("en")["firstBrief"]; row != "Your first brief" {
		t.Fatalf("en word %q", row)
	}
	if _, ok := menuBarFirstBriefRow(menuBarWords("en"), "en", FirstBriefTime{}); ok {
		t.Fatal("no time, no first brief row")
	}
	// In the view: the saved first brief time fills the row until a brief is made.
	app := mainChatTestApp(t)
	raw, _ := json.Marshal(first)
	if err := app.store.SetUIState(t.Context(), firstBriefTimeKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	view, err := app.menuBarView("en", false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Brief.Kind != "first" || view.Brief.Title != "Your first brief" || view.Brief.Meta != "at 7:30 PM" {
		t.Fatalf("view brief %+v", view.Brief)
	}
}

func TestMenuBarRenderEscapesSourcesAndSharesTokens(t *testing.T) {
	view := menuBarView{Words: menuBarWords("en"), Next: &menuBarNext{Title: `</script><script>bad()</script>`}}
	html := renderMenuBarHTML(view)
	if strings.Contains(html, `<script>bad()`) || strings.Contains(html, "/*__MODEL__*/") || strings.Contains(html, "/*__TOKENS__*/") {
		t.Fatal("unsafe or incomplete render")
	}
	css := menuBarTokenCSS(menuBarAppCSS)
	for _, s := range []string{":root{", `:root[data-theme="onedark"]`, "--color-panel:", "--color-ink:", "--color-copper:"} {
		if !strings.Contains(css, s) {
			t.Errorf("missing shared token %s", s)
		}
	}
	for _, s := range []string{"@import", "@theme", "data:image/svg", "background-image"} {
		if strings.Contains(css, s) {
			t.Errorf("unrelated CSS included %s", s)
		}
	}
	for _, s := range []string{"innerHTML", "fetch(", "http://", "https://", "#[0-9]"} {
		if strings.Contains(menuBarHTML, s) {
			t.Errorf("unexpected HTML %s", s)
		}
	}
	for seconds, want := range map[float64]string{48: "0:48", 95: "1:35", 0: "", -1: "", math.Inf(1): ""} {
		if got := menuBarDuration(seconds); got != want {
			t.Errorf("%f => %s", seconds, got)
		}
	}
}

// Bo's popover said "Could not load what is waiting on you" whenever a
// refresh ran past ListMainChatWaitingItems' deadline under load. A slow or
// failed refresh now keeps the last good list; the error is only for a
// launch where no read has worked yet.
func TestMenuBarTimeoutKeepsTheLastList(t *testing.T) {
	timeout := context.DeadlineExceeded
	read := func(lang string, ids ...string) menuBarView {
		view := menuBarView{Language: lang, Words: menuBarWords(lang), State: "Observing", Waiting: len(ids), WaitingIDs: ids}
		if len(ids) > 0 {
			view.Next = &menuBarNext{ID: ids[0], Title: "Reply to Halcyon"}
		}
		return view
	}
	failed := func(lang string) menuBarView {
		// A failed read carries whatever it had read so far.
		return menuBarView{Language: lang, Words: menuBarWords(lang), State: "Observing", Waiting: 9, WaitingIDs: []string{"partial"}, Next: &menuBarNext{ID: "partial"}}
	}
	var memory menuBarWaitingMemory

	first := failed("en")
	memory.settle(&first, true, timeout, time.Now())
	if first.Error != menuBarWords("en")["failed"] || first.Next != nil || first.Waiting != 0 || first.WaitingPending {
		t.Fatalf("no read has worked yet: say so plainly %+v", first)
	}

	good := read("en", "a", "b")
	memory.settle(&good, true, nil, time.Now())

	slow := failed("en")
	memory.settle(&slow, true, timeout, time.Now())
	if slow.Error != "" || slow.WaitingPending || slow.Waiting != 2 || slow.Next == nil || slow.Next.Title != "Reply to Halcyon" {
		t.Fatalf("a timeout must keep the last list silently %+v", slow)
	}
	if !reflect.DeepEqual(slow.WaitingIDs, []string{"a", "b"}) || slow.Label != menuBarLabel(slow.Words, slow.State, 2) {
		t.Fatalf("ids or label not from the last list %+v", slow)
	}
	data, _ := json.Marshal(slow)
	if strings.Contains(string(data), `"error"`) {
		t.Fatalf("error reached the page: %s", data)
	}

	// The kept list is a copy: a later view cannot change it.
	slow.Next.Title = "changed"
	again := failed("en")
	memory.settle(&again, true, timeout, time.Now())
	if again.Next == nil || again.Next.Title != "Reply to Halcyon" {
		t.Fatal("the held list was shared with a view")
	}

	// Another language: the held rows are in English, so placeholders.
	zh := failed("zh-CN")
	memory.settle(&zh, true, timeout, time.Now())
	if zh.Error != "" || !zh.WaitingPending || zh.Next != nil || zh.Waiting != 0 {
		t.Fatalf("a held list in another language %+v", zh)
	}

	// A privacy change withdraws the list: placeholders, never old rows and
	// never the error, since a read has worked.
	memory.withdraw()
	withdrawn := failed("en")
	memory.settle(&withdrawn, true, timeout, time.Now())
	if withdrawn.Error != "" || !withdrawn.WaitingPending || withdrawn.Next != nil || withdrawn.Waiting != 0 || len(withdrawn.WaitingIDs) != 0 {
		t.Fatalf("after a withdraw %+v", withdrawn)
	}

	// The next good read holds again, an empty list included.
	empty := read("en")
	memory.settle(&empty, true, nil, time.Now())
	after := failed("en")
	memory.settle(&after, true, timeout, time.Now())
	if after.WaitingPending || after.Error != "" || after.Waiting != 0 || after.Next != nil {
		t.Fatalf("an empty good list is a list %+v", after)
	}
	if !strings.Contains(renderMenuBarHTML(after), "!!model.waitingPending") {
		t.Fatal("the page ignores waitingPending")
	}
}

// Bo's log on 2026-10-27: with Chat idle, focused or not, the five-second
// status tick read GetMainChatOverview and ListMainChatWaitingItems about 40
// times in five minutes each. Idle ticks now reuse the held list; the list
// is read after a change, the popover opening, another language, a failed
// read once 30 s have passed, and otherwise every 15 minutes.
func TestMenuBarIdleTicksDoNotReadWaiting(t *testing.T) {
	start := time.Date(2026, 10, 27, 17, 42, 0, 0, time.Local)
	var memory menuBarWaitingMemory
	reads := 0
	tick := func(at time.Time, lang string, err error) menuBarView {
		view := menuBarView{Language: lang, Words: menuBarWords(lang), State: "Observing"}
		read := memory.begin(at, lang)
		if read {
			reads++
			if err == nil {
				view.Waiting, view.WaitingIDs, view.Next = 1, []string{"a"}, &menuBarNext{ID: "a", Title: "Reply to Halcyon"}
			}
		}
		memory.settle(&view, read, err, at)
		return view
	}
	at := start
	step := func(d time.Duration) time.Time { at = at.Add(d); return at }

	tick(at, "en", nil)
	if reads != 1 {
		t.Fatalf("the first tick reads the list, got %d reads", reads)
	}
	// Five idle minutes of five-second ticks: no read, the held list shows.
	for i := 0; i < 60; i++ {
		view := tick(step(5*time.Second), "en", nil)
		if view.Next == nil || view.Error != "" || view.WaitingPending {
			t.Fatalf("tick %d lost the held list %+v", i, view)
		}
	}
	if reads != 1 {
		t.Fatalf("idle ticks read the waiting list %d times in five minutes", reads-1)
	}

	// A main-chat write, or the popover opening: one read on the next tick.
	memory.changed()
	tick(step(5*time.Second), "en", nil)
	tick(step(5*time.Second), "en", nil)
	if reads != 2 {
		t.Fatalf("a change should read once, got %d reads", reads)
	}

	// Another language reads again at once.
	tick(step(5*time.Second), "zh-CN", nil)
	if reads != 3 {
		t.Fatalf("another language should read, got %d reads", reads)
	}

	// A failed read keeps the list and waits 30 s before the next try.
	memory.changed()
	failedAt := step(5 * time.Second)
	view := tick(failedAt, "zh-CN", context.DeadlineExceeded)
	if reads != 4 || view.Next == nil || view.Error != "" {
		t.Fatalf("failed read %d %+v", reads, view)
	}
	for at.Before(failedAt.Add(menuBarWaitingRetry - 5*time.Second)) {
		tick(step(5*time.Second), "zh-CN", nil)
	}
	if reads != 4 {
		t.Fatalf("retried before 30 s, got %d reads", reads)
	}
	tick(step(5*time.Second), "zh-CN", nil)
	if reads != 5 {
		t.Fatalf("no retry after 30 s, got %d reads", reads)
	}

	// Cards can expire on their own: one read at 15 minutes, none before.
	lastRead := at
	for at.Before(lastRead.Add(menuBarWaitingMaxAge - 5*time.Second)) {
		tick(step(5*time.Second), "zh-CN", nil)
	}
	if reads != 5 {
		t.Fatalf("read before the 15-minute age, got %d reads", reads)
	}
	tick(step(5*time.Second), "zh-CN", nil)
	if reads != 6 {
		t.Fatalf("no read at the 15-minute age, got %d reads", reads)
	}

	// A privacy withdraw drops the list: the next tick reads.
	memory.withdraw()
	tick(step(5*time.Second), "zh-CN", nil)
	if reads != 7 {
		t.Fatalf("a withdraw should read again, got %d reads", reads)
	}
}

// The in-memory chat count also counts briefs, so the menu bar never takes
// it; with nothing open in Your work, nothing is counted.
func TestMenuBarViewIgnoresChatWaitingCount(t *testing.T) {
	app := mainChatTestApp(t)
	card := mainChatProactive{MomentID: "held", Kind: "brief_ready", State: "pending", Headline: "Your first week with Isoform."}
	app.mainChatWaiting.mu.Lock()
	app.mainChatWaiting.state = MainChatWaitingState{SessionID: "main", Count: 1, Ready: true, Items: []StreamItemData{{ID: "held", Kind: "system", ToolName: mainChatProactiveTool, ToolInput: mainChatViewPayload(t, card)}}}
	app.mainChatWaiting.mu.Unlock()
	view, err := app.menuBarView("en", true)
	if err != nil {
		t.Fatal(err)
	}
	// The label says the trouble line while one is up (pass-access v1.1.0
	// rule 3); a machine whose assistant is signed out has one, so the
	// count-free label is checked against whichever form applies.
	want := menuBarLabel(view.Words, view.State, 0)
	if view.SignOut != nil && view.SignOut.Line != "" {
		want = menuBarFill(view.Words["menuTrouble"], "line", view.SignOut.Line)
	}
	if view.Waiting != 0 || view.Next != nil || len(view.WaitingIDs) != 0 || view.Label != want {
		t.Fatalf("view counted what Your work doesn't: %+v", view)
	}
}

// shell-review v1.0.0 B4: the row is the latest brief with its day, never a
// bare time or "no recording"; while today's is still being tried, it is
// Today's tries line.
func TestMenuBarBriefRowNamesTheDayAndTheTries(t *testing.T) {
	now := time.Date(2026, 11, 3, 16, 17, 0, 0, time.Local)
	brief := func(day int, hour, minute int) MobileBriefV1 {
		at := time.Date(2026, 11, day, hour, minute, 0, 0, time.Local)
		return MobileBriefV1{BriefID: at.Format("2006-01-02") + "-evening", Slot: mobileBriefSlotEvening, LocalDate: at.Format("2006-01-02"), AsOf: at.Format(time.RFC3339)}
	}
	for _, c := range []struct {
		lang, want string
		brief      MobileBriefV1
	}{
		{"en", "Yesterday, 5:18 PM", brief(2, 17, 18)},
		{"en", "Today, 7:00 AM", brief(3, 7, 0)},
		{"en", "Friday, 6:00 PM", brief(-1, 18, 0)},
		{"zh-CN", "昨天 17:18", brief(2, 17, 18)},
	} {
		got := menuBarBriefRow(menuBarWords(c.lang), c.lang, c.brief, true, false, MobileBriefHealth{}, now)
		if got.Kind != "read" || got.Meta != c.want || strings.Contains(got.Meta, "recording") || strings.Contains(got.Meta, "录音") {
			t.Errorf("%s %s: %+v, want %q", c.lang, c.brief.LocalDate, got, c.want)
		}
	}
	if got := menuBarBriefRow(menuBarWords("en"), "en", brief(2, 17, 18), true, false, MobileBriefHealth{}, now); got.Title != "Read your brief" {
		t.Errorf("title %q", got.Title)
	}
	old := menuBarBriefRow(menuBarWords("en"), "en", brief(20, 18, 0), true, false, MobileBriefHealth{}, time.Date(2026, 11, 30, 9, 0, 0, 0, time.Local))
	if old.Meta != "Nov 20, 6:00 PM" {
		t.Errorf("a brief over a week old names its date: %+v", old)
	}

	retry := time.Date(2026, 11, 3, 16, 35, 0, 0, time.Local).UTC().Format(time.RFC3339)
	trying := MobileBriefHealth{PendingSlot: mobileBriefSlotMorning, PendingAttempts: 13, RetryAt: retry}
	if got := menuBarBriefRow(menuBarWords("en"), "en", brief(2, 17, 18), true, false, trying, now); got.Kind != "trying" || got.Title != "Tried 13 times without finishing. Isoform tries again at 4:35 PM." || got.Meta != "" {
		t.Errorf("tries line %+v", got)
	}
	if got := menuBarBriefRow(menuBarWords("zh-CN"), "zh-CN", brief(2, 17, 18), true, false, trying, now); got.Title != "试了 13 次都没完成。Isoform 会在16:35再试。" {
		t.Errorf("zh tries line %q", got.Title)
	}
	once := MobileBriefHealth{PendingSlot: mobileBriefSlotMorning, FailingSlot: mobileBriefSlotMorning, FailedAttempts: 1, RetryAt: retry}
	if got := menuBarBriefRow(menuBarWords("en"), "en", MobileBriefV1{}, false, false, once, now); got.Title != "Tried once without finishing. Isoform tries again at 4:35 PM." {
		t.Errorf("once %q", got.Title)
	}
	for _, tc := range []struct{ lang, cause, want string }{
		{"en", "timeout", "Tried 13 times. It timed out. Isoform tries again at 4:35 PM."},
		{"en", "signed_out", "Tried 13 times. Your AI is signed out. Isoform tries again at 4:35 PM."},
		{"zh-CN", "timeout", "试了 13 次。超时了。Isoform 会在16:35再试。"},
		{"zh-CN", "signed_out", "已尝试 13 次。你的 AI 已退出登录。Isoform 会在 16:35 再试一次。"},
	} {
		trying.PendingCause = tc.cause
		if got := menuBarBriefRow(menuBarWords(tc.lang), tc.lang, MobileBriefV1{}, false, false, trying, now); got.Title != tc.want {
			t.Errorf("%s %s: %q", tc.lang, tc.cause, got.Title)
		}
	}
	// A retry time already past is not a promise: the latest brief shows.
	past := MobileBriefHealth{PendingSlot: mobileBriefSlotMorning, PendingAttempts: 13, RetryAt: time.Date(2026, 11, 3, 16, 0, 0, 0, time.Local).Format(time.RFC3339)}
	if got := menuBarBriefRow(menuBarWords("en"), "en", brief(2, 17, 18), true, false, past, now); got.Kind != "read" {
		t.Errorf("past retry %+v", got)
	}
	// Making wins over the tries line.
	if got := menuBarBriefRow(menuBarWords("en"), "en", brief(2, 17, 18), true, true, trying, now); got.Kind != "making" {
		t.Errorf("making %+v", got)
	}
}

// shell-review v1.0.0 B6: the footer names the sheet it opens.
func TestMenuBarFooterNamesWhatIsoformSees(t *testing.T) {
	if got := menuBarWords("en")["onThisMac"]; got != "What Isoform sees" {
		t.Fatalf("en footer %q", got)
	}
	if got := menuBarWords("zh-CN")["onThisMac"]; got != "Isoform 能看到什么" {
		t.Fatalf("zh-CN footer %q", got)
	}
	for _, lang := range []string{"en", "zh-CN"} {
		for _, v := range menuBarCopy[lang] {
			if strings.Contains(v, "no recording") || strings.Contains(v, "没有录音") || v == "On this computer" || v == "在这台电脑上" {
				t.Errorf("%s: retired %q is back", lang, v)
			}
		}
	}
}

func TestMenuBarPausedStateSaysUntilWhen(t *testing.T) {
	until := time.Date(2026, 11, 5, 19, 15, 0, 0, time.Local).UnixMilli()
	for _, tc := range []struct{ lang, want string }{{"en", "Closed Eyes · until 7:15 PM · every device"}, {"zh-CN", "Closed Eyes · 至 19:15 · 所有设备"}} {
		if got := menuBarPausedState(menuBarWords(tc.lang), tc.lang, DoingNow{Kind: doingNowPaused, PausedUntil: until}); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.lang, got, tc.want)
		}
	}
	if got := menuBarPausedState(menuBarWords("en"), "en", DoingNow{Kind: doingNowPaused}); got != "Closed Eyes · every device" {
		t.Fatalf("no timer: got %q", got)
	}
}

func TestMenuBarRetryIncludesCauseAndNoEstimate(t *testing.T) {
	now := time.Date(2026, 11, 4, 8, 30, 0, 0, time.Local)
	health := MobileBriefHealth{PendingSlot: "morning", PendingAttempts: 6, PendingCause: "timeout", RetryAt: now.Add(15 * time.Minute).Format(time.RFC3339)}
	for _, tc := range []struct{ lang, want, making string }{
		{"en", "Tried 6 times. It timed out. Isoform tries again at 8:45 AM.", "Making your morning brief now."},
		{"zh-CN", "试了 6 次。超时了。Isoform 会在8:45再试。", "正在生成你的早间简报。"},
	} {
		w := menuBarWords(tc.lang)
		if got := menuBarBriefRow(w, tc.lang, MobileBriefV1{}, false, false, health, now); got.Title != tc.want {
			t.Errorf("%s retry: %+v", tc.lang, got)
		}
		if got := menuBarBriefRow(w, tc.lang, MobileBriefV1{}, false, true, health, now); got.Title != tc.making || got.Meta != "" {
			t.Errorf("%s making: %+v", tc.lang, got)
		}
	}
}

func TestMenuBarBriefRunChangesUpdateRetryWithoutStampingBrief(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(8, 0)}
	service, _ := newTestMobileBriefService(t, clock)
	settings := defaultMobileBriefSettings()
	settings.Adaptive = false
	if err := service.config.save(settings); err != nil {
		t.Fatal(err)
	}
	latest := MobileBriefV1{BriefID: "2026-10-08-evening", AsOf: mobileBriefTestDay(18, 0).AddDate(0, 0, -1).Format(time.RFC3339)}
	occurrence := mobileBriefTodaySlots(clock.Now(), time.UTC, settings)[0]
	service.retryFor, service.retryAfter = occurrence.BriefID, clock.Now().Add(10*time.Minute)
	writeFailure := func(cause string) {
		t.Helper()
		_, err := service.runs.record(mobileBriefRun{BriefID: occurrence.BriefID, Slot: occurrence.Slot, ScheduledAt: formatMobileBriefTime(occurrence.ScheduledAt), Status: mobileBriefRunFailed, At: formatMobileBriefTime(clock.Now()), FailureCause: cause})
		if err != nil {
			t.Fatal(err)
		}
	}
	writeFailure("timeout")
	before := mobileBriefFinishedReadKeys.Load()
	for i := 0; i < 3; i++ {
		health := service.app.menuBarBriefHealth(service, latest, true)
		if health.PendingAttempts != 1 || health.PendingCause != "timeout" || health.RetryAt == "" {
			t.Fatalf("tick %d: %+v", i, health)
		}
	}
	writeFailure("signed_out")
	health := service.app.menuBarBriefHealth(service, latest, true)
	if health.PendingAttempts != 2 || health.PendingCause != "signed_out" {
		t.Fatalf("updated run: %+v", health)
	}
	clock.Advance(10 * time.Minute)
	if got := service.app.menuBarBriefHealth(service, latest, true); got.RetryAt != "" {
		t.Fatalf("expired retry: %+v", got)
	}
	if stamps := mobileBriefFinishedReadKeys.Load() - before; stamps != 0 {
		t.Fatalf("run changes and ticks stamped the brief %d times", stamps)
	}
}

// R75 review: the signed-out lines name the chosen assistant; "Your AI" only when none is known.
func TestMenuBarSignedOutLinesNameTheChosenAssistant(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
	retry := time.Date(2026, 11, 3, 16, 35, 0, 0, time.Local).UTC().Format(time.RFC3339)
	for _, tc := range []struct{ lang, assistant, want string }{
		{"en", "codex", "Tried 13 times. Codex is signed out."},
		{"en", "claude-code", "Tried 13 times. Claude Code is signed out."},
		{"en", "", "Tried 13 times. Your AI is signed out. Isoform tries again at 4:35 PM."},
		{"zh-CN", "claude-code", "已尝试 13 次。Claude Code 已退出登录。"},
		{"zh-CN", "", "已尝试 13 次。你的 AI 已退出登录。Isoform 会在 16:35 再试一次。"},
	} {
		health := MobileBriefHealth{PendingSlot: mobileBriefSlotMorning, PendingAttempts: 13, RetryAt: retry, PendingCause: "signed_out", PendingAssistant: tc.assistant}
		if got := menuBarBriefRow(menuBarWords(tc.lang), tc.lang, MobileBriefV1{}, false, false, health, now); got.Title != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.lang, tc.assistant, got.Title, tc.want)
		}
	}
}

// The popover's header and Next row (computer-settings v1.0.1 rule 8):
// "seeing", with "watch listening" once a watch sent audio within the hour;
// the first item to answer with its place in the list and its source.
func TestMenuBarNextItemAndWatchListening(t *testing.T) {
	w := menuBarWords("en")
	if menuBarNextItem(w, WorkList{}) != nil {
		t.Fatal("no items should give no Next row")
	}
	list := WorkList{Items: []WorkItem{
		{ID: "done", State: workStateAnswered, Work: WorkBody{Title: "Answered already"}},
		{ID: "a", State: workStateOpen, Source: workSourceBrief, Work: WorkBody{Title: "Reply to Lena about the import", Text: "She asked twice"}, Yes: WorkYes{Does: "mail_draft"}},
		{ID: "b", State: workStateOpen, Source: workSourceAgent, Yes: WorkYes{Does: "agent"}},
		{ID: "c", State: workStateOpen}, {ID: "d", State: workStateOpen},
	}}
	next := menuBarNextItem(w, list)
	if next == nil || next.ID != "a" || next.Line != "Next · 1 of 4 · Your brief" || next.Title != "Reply to Lena about the import" || next.Detail != "She asked twice" || next.ToAgent {
		t.Fatalf("next = %+v", next)
	}
	if got := menuBarNextItem(w, WorkList{Items: list.Items[2:]}); !got.ToAgent || got.Line != "Next · 1 of 3 · Your agent" {
		t.Fatalf("agent next = %+v", got)
	}
	if got := menuBarNextItem(w, WorkList{Items: list.Items[3:4]}); got.Line != "Next · 1 of 1" {
		t.Fatalf("no source line = %q", got.Line)
	}
	if got := menuBarNextItem(menuBarWords("zh-CN"), list); !strings.Contains(got.Line, "4") || !strings.Contains(got.Line, "你的简报") {
		t.Fatalf("zh-CN line = %q", got.Line)
	}
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	if watchListeningAt(nil, now) {
		t.Fatal("no recordings is not listening")
	}
	if !watchListeningAt([]WatchRecording{{StartedAt: now.Add(-40 * time.Minute).Format(time.RFC3339)}}, now) {
		t.Fatal("a recording 40 minutes ago is listening")
	}
	if watchListeningAt([]WatchRecording{{StartedAt: now.Add(-3 * time.Hour).Format(time.RFC3339)}, {StartedAt: "bad"}}, now) {
		t.Fatal("a recording three hours ago is not listening")
	}
}

func TestQuickChatHotkeyDisplay(t *testing.T) {
	for _, tc := range []struct {
		in   QuickChatHotkeyConfig
		want string
	}{
		{QuickChatHotkeyConfig{Key: quickChatHotkeyKeyOptOpt}, "⌥⌥"},
		{QuickChatHotkeyConfig{Key: quickChatHotkeyKeyOff}, ""},
		{QuickChatHotkeyConfig{Alt: true, Key: " "}, "⌥Space"},
		{QuickChatHotkeyConfig{Meta: true, Shift: true, Key: "k"}, "⇧⌘K"},
	} {
		if got := quickChatHotkeyDisplay(tc.in); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Next stays on the five-second ticks that don't read the list, and leaves
// with a privacy withdraw.
func TestMenuBarWaitingMemoryHoldsNext(t *testing.T) {
	var m menuBarWaitingMemory
	now := time.Now()
	read := menuBarView{Language: "en", Words: menuBarWords("en"), Next: &menuBarNext{ID: "a", Line: "Next · 1 of 1"}}
	m.settle(&read, true, nil, now)
	tick := menuBarView{Language: "en", Words: menuBarWords("en")}
	m.settle(&tick, false, nil, now.Add(5*time.Second))
	if tick.Next == nil || tick.Next.ID != "a" {
		t.Fatalf("tick next = %+v", tick.Next)
	}
	m.withdraw()
	after := menuBarView{Language: "en", Words: menuBarWords("en")}
	m.settle(&after, false, nil, now.Add(10*time.Second))
	if after.Next != nil {
		t.Fatalf("next after withdraw = %+v", after.Next)
	}
}

func TestMenuBarAnswersOnlyOpenItemsAndNeverAnAgentsYes(t *testing.T) {
	list := WorkList{Items: []WorkItem{
		{ID: "draft", State: workStateOpen, Yes: WorkYes{Does: "mail_draft"}},
		{ID: "code", State: workStateOpen, Yes: WorkYes{Does: "agent"}},
		{ID: "done", State: workStateAnswered},
	}}
	for _, tc := range []struct {
		id, action string
		want       bool
	}{
		{"draft", workActionYes, true},
		{"draft", workActionNo, true},
		{"code", workActionYes, false},
		{"code", workActionNo, true},
		{"done", workActionYes, false},
		{"gone", workActionYes, false},
		{"gone", workActionNo, false},
	} {
		if got := menuBarAnswersHere(list, tc.id, tc.action); got != tc.want {
			t.Errorf("%s %s: answers here = %v, want %v", tc.id, tc.action, got, tc.want)
		}
	}
	if menuBarAnswersHere(WorkList{}, "draft", workActionYes) {
		t.Fatal("an empty list (or one that could not be read) never answers here")
	}
}

// The state line never hides the watch's state (computer-settings v1.2.0
// rule 5), and Closed Eyes shows its one shortcut.
func TestMenuBarStateLineSaysTheWatch(t *testing.T) {
	w := menuBarWords("en")
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		recordings []WatchRecording
		want       string
	}{
		{nil, "seeing · no watch paired"},
		{[]WatchRecording{{StartedAt: now.Add(-20 * time.Minute).Format(time.RFC3339)}}, "seeing · watch listening"},
		{[]WatchRecording{{StartedAt: now.Add(-5 * time.Hour).Format(time.RFC3339)}}, "seeing · watch not listening"},
	} {
		menuBarWatch.Store(menuBarWatchWord(tc.recordings, now))
		if got := menuBarStateLine(w, w["observing"]); got != tc.want {
			t.Errorf("state line = %q, want %q", got, tc.want)
		}
	}
	menuBarWatch.Store("watchNot")
	if got := menuBarStateLine(w, w["paused"]); got != "Closed Eyes · every device · watch not listening" {
		t.Errorf("closed state line = %q", got)
	}
	menuBarWatch.Store("")
	if closedEyesHotkeyDisplay("darwin") != "⌃⌥⌘E" || closedEyesHotkeyDisplay("linux") != "" {
		t.Fatal("Closed Eyes shortcut display")
	}
}

func TestMenuBarMarkLabelSaysTheState(t *testing.T) {
	w := menuBarWords("en")
	menuBarWatch.Store("watchListening")
	if got := menuBarMarkLabel(w, false); got != "Listening" {
		t.Fatalf("listening label = %q", got)
	}
	if got := menuBarMarkLabel(w, true); got != "Closed Eyes" {
		t.Fatalf("closed label = %q", got)
	}
	menuBarWatch.Store("watchNot")
	if got := menuBarMarkLabel(w, false); got != "Not listening" {
		t.Fatalf("not listening label = %q", got)
	}
	menuBarWatch.Store("")
}
