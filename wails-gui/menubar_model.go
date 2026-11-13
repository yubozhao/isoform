package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const menuBarMaxWaiting = 3

// The badge is a local presentation receipt, never a resolution of the work.
// Retain ids, not a count: replacing one waiting item must light the dot again.
type menuBarAttention struct{ seen map[string]bool }

// Match the render-tier account boundary: an enabled identity disappearing
// resets private presentation state; changing only its quota does not.
type menuBarAccountIdentity struct{ provider, kind, name string }
type menuBarAccountPrivacy struct {
	signedIn map[menuBarAccountIdentity]bool
}

func (s *menuBarAccountPrivacy) update(accounts []ModelAccount) bool {
	current := make(map[menuBarAccountIdentity]bool)
	for _, account := range accounts {
		if account.On {
			current[menuBarAccountIdentity{account.Provider, account.Kind, account.Name}] = true
		}
	}
	withdrawn := s.signedIn == nil
	for id := range s.signedIn {
		withdrawn = withdrawn || !current[id]
	}
	s.signedIn = current
	return withdrawn
}

type menuBarIconState struct {
	Opacity   float64 `json:"opacity"`
	Dot       bool    `json:"dot"`
	Highlight bool    `json:"highlight"`
}

func (s *menuBarAttention) icon(paused, open bool, ids []string) menuBarIconState {
	next := make(map[string]bool, len(ids))
	state := menuBarIconState{Opacity: 1, Highlight: open}
	if paused {
		state.Opacity = .45
	}
	for _, id := range ids {
		next[id] = open || s.seen[id]
		state.Dot = state.Dot || !next[id]
	}
	s.seen = next
	return state
}

type menuBarMenuItem struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Key       string            `json:"key,omitempty"`
	Modifiers []string          `json:"modifiers,omitempty"`
	Children  []menuBarMenuItem `json:"children,omitempty"`
	Separator bool              `json:"separator,omitempty"`
}

// English is the source of truth for native chrome.
// The 1.0 popover copy (Isoform design, 2026-10-27,
// docs/handoff-2026-10-26/menu-bar-popover-1.0): existing app strings are
// reused word for word.
var menuBarCopy = map[string]map[string]string{
	"en": {
		"open": "Open Isoform", "make": "Make my brief now", "pause15": "Pause for 15 minutes", "resumeMenu": "Resume observing",
		"settingsMenu": "Settings…", "quit": "Quit Isoform",
		"observing": "Observing", "paused": "Observing paused", "pausedUntil": "Paused until {time}", "off": "Observing is off", "starting": "Starting…",
		"pause": "Pause 15 min", "resume": "Resume", "turnOn": "Turn on",
		"morning": "Hear your morning brief", "evening": "Hear your evening brief", "brief": "Hear your brief", "week": "Hear your week",
		"read":    "Read your brief",
		"dayTime": "{day}, {time}", "today": "Today", "yesterday": "Yesterday",
		"tried": "Tried {n} times without finishing. Isoform tries again at {time}.", "triedOnce": "Tried once without finishing. Isoform tries again at {time}.",
		"triedSignedOut": "Tried {n} times. {ai} is signed out. Isoform tries again at {time}.", "triedSignedOutOnce": "Tried 1 time. {ai} is signed out. Isoform tries again at {time}.",
		"signIn": "Sign in", "signedOut": "Tried {n} times. {ai} is signed out.", "signedOutOnce": "Tried 1 time. {ai} is signed out.", "yourAI": "Your AI",
		"retry": "Isoform tries again at {time}.", "none": "No brief yet today", "madeLastNight": "Made last night at {time}", "madeYesterday": "Made yesterday at {time}", "madeAt": "Made at {time}", "madeOnDate": "Made {date} at {time}", "next": "Next brief tomorrow at {time}", "nextToday": "Next brief today at {time}",
		"making": "Making your brief now.", "makingMorning": "Making your morning brief now.", "makingEvening": "Making your evening brief now.", "triedTimeout": "Tried {n} times. It timed out. Isoform tries again at {time}.", "triedTimeoutOnce": "Tried once. It timed out. Isoform tries again at {time}.", "playing": "{elapsed} of {length}",
		"waiting": "Waiting on you", "seeAll": "See all {n} in Chat", "empty": "Nothing is waiting on you.",
		"failed": "Could not load what is waiting on you. Your chat still has everything.",
		"update": "{version} installs when you quit", "updateRestart": "{version} is ready", "restart": "Restart now",
		"models":   "Downloading models · {percent}%",
		"settings": "Settings", "footerQuit": "Quit", "onThisMac": "What Isoform sees", "openShortcut": "⌘⇧I",
		"label": "Isoform, {state}, {n} waiting on you", "labelNone": "Isoform, {state}",
		"actionFailed":  "Could not complete that action. Open Isoform to try again.",
		"signedOutLine": "{assistant} is signed out, so briefs are shorter and Chat can't answer.",
	},
	"zh-CN": {
		"signedOutLine": "{assistant} 已退出登录，所以简报会更简短，聊天也无法回答。",
		"open":          "打开 Isoform", "make": "立即生成简报", "pause15": "暂停 15 分钟", "resumeMenu": "恢复观察",
		"settingsMenu": "设置…", "quit": "退出 Isoform",
		"observing": "观察中", "paused": "观察已暂停", "pausedUntil": "已暂停至 {time}", "off": "观察已关闭", "starting": "正在启动…",
		"pause": "暂停 15 分钟", "resume": "恢复", "turnOn": "打开",
		"morning": "收听早间简报", "evening": "收听晚间简报", "brief": "收听简报", "week": "收听你的一周",
		"read":    "阅读简报",
		"dayTime": "{day} {time}", "today": "今天", "yesterday": "昨天",
		"tried": "试了 {n} 次都没完成。Isoform 会在{time}再试。", "triedOnce": "试了 1 次都没完成。Isoform 会在{time}再试。",
		"triedSignedOut": "已尝试 {n} 次。{ai} 已退出登录。Isoform 会在 {time} 再试一次。", "triedSignedOutOnce": "已尝试 1 次。{ai} 已退出登录。Isoform 会在 {time} 再试一次。",
		"signIn": "登录", "signedOut": "已尝试 {n} 次。{ai} 已退出登录。", "signedOutOnce": "已尝试 1 次。{ai} 已退出登录。", "yourAI": "你的 AI",
		"retry": "Isoform 会在{time}再试。", "none": "今天还没有简报", "madeLastNight": "昨晚 {time} 生成", "madeYesterday": "昨天 {time} 生成", "madeAt": "{time} 生成", "madeOnDate": "{date} {time} 生成", "next": "下一份在明天 {time}", "nextToday": "下一份在今天 {time}",
		"making": "正在生成简报。", "makingMorning": "正在生成你的早间简报。", "makingEvening": "正在生成你的晚间简报。", "triedTimeout": "试了 {n} 次。超时了。Isoform 会在{time}再试。", "triedTimeoutOnce": "试了 1 次。超时了。Isoform 会在{time}再试。", "playing": "{elapsed} / {length}",
		"waiting": "等你处理", "seeAll": "在聊天中查看全部 {n} 项", "empty": "没有等你处理的事。",
		"failed": "没能读取等你处理的事。聊天里的内容都还在。",
		"update": "退出时安装 Isoform {version}", "updateRestart": "{version} 已准备好", "restart": "立即重启",
		"models":   "正在下载模型 · {percent}%",
		"settings": "设置", "footerQuit": "退出", "onThisMac": "Isoform 能看到什么", "openShortcut": "⌘⇧I",
		"label": "Isoform，{state}，{n} 项等你处理", "labelNone": "Isoform，{state}",
		"actionFailed": "无法完成操作。请打开 Isoform 重试。",
	},
}

func menuBarWords(lang string) map[string]string { return menuBarCopy[normalizeUILanguage(lang)] }

// menuBarAssistantName is the chosen assistant's name for the signed-out lines;
// "Your AI" only when the run does not say which assistant it was.
func menuBarAssistantName(w map[string]string, assistant string) string {
	if assistant == "" {
		return w["yourAI"]
	}
	return assistantSignOutDisplayName(assistant)
}

func menuBarFill(template string, values ...string) string {
	for i := 0; i+1 < len(values); i += 2 {
		template = strings.ReplaceAll(template, "{"+values[i]+"}", values[i+1])
	}
	return template
}

// menuBarMenu is the short native menu for right-click and Control-click.
// The observation-state line, the Library submenu and Pause for 1 hour left
// it in 1.0; the global privacy shortcut (Control-Option-Command-E) is its
// own event tap and keeps working.
func menuBarMenu(lang string, paused bool) []menuBarMenuItem {
	w := menuBarWords(lang)
	pause := menuBarMenuItem{ID: "pause15", Title: w["pause15"]}
	if paused {
		pause = menuBarMenuItem{ID: "resume", Title: w["resumeMenu"]}
	}
	sep := menuBarMenuItem{Separator: true}
	return []menuBarMenuItem{
		{ID: "open", Title: w["open"], Key: "i", Modifiers: []string{"command", "shift"}},
		{ID: "make", Title: w["make"]}, sep,
		pause, sep,
		{ID: "settings", Title: w["settingsMenu"], Key: ",", Modifiers: []string{"command"}},
		{ID: "quit", Title: w["quit"], Key: "q", Modifiers: []string{"command"}},
	}
}

type menuBarWaitingItem struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
}

// menuBarBrief is the popover's brief row: ready (with a recording), read
// (no recording; opens Today), trying (today's is still being tried), none
// (nothing yet today) or making.
type menuBarBrief struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id,omitempty"`
	Title   string  `json:"title"`
	Meta    string  `json:"meta"`
	Seconds float64 `json:"seconds,omitempty"`
}

type menuBarView struct {
	Language string            `json:"language"`
	Theme    string            `json:"theme"`
	Words    map[string]string `json:"words"`
	// Status is observing, paused, off or starting; State is its words.
	Status     string               `json:"status"`
	State      string               `json:"state"`
	Paused     bool                 `json:"paused"`
	Waiting    int                  `json:"waiting"`
	Items      []menuBarWaitingItem `json:"items"`
	WaitingIDs []string             `json:"-"`
	Brief      menuBarBrief         `json:"brief"`
	// Update is the downloaded update's version, shown only then.
	Update string `json:"update,omitempty"`
	// SignOut is R75's first row: the chosen assistant is confirmed signed out.
	SignOut *menuBarSignOut `json:"signOut,omitempty"`
	// UpdateRestart is set when the staged update needs Restart now to install.
	UpdateRestart bool `json:"updateRestart,omitempty"`
	// Models is "Downloading models · 64%" while the local models setup
	// confirmed are still downloading (setup v2.2.0 §2); empty otherwise.
	Models string            `json:"models,omitempty"`
	Menu   []menuBarMenuItem `json:"menu"`
	Icon   menuBarIconState  `json:"icon"`
	// Label is the icon's VoiceOver label: the state and the count.
	Label string `json:"label"`
	Error string `json:"error,omitempty"`
	// Loading marks the first second after launch, before the first read:
	// "Starting…" with no brief row and no Waiting on you section, never
	// "Loading…" or a claim such as "Nothing is waiting on you".
	Loading bool `json:"loading,omitempty"`
	// WaitingPending leaves Waiting on you out when a read failed after
	// the last list was withdrawn (a privacy change): no error, since an
	// earlier read worked, no stale rows and no placeholders (shell B5).
	WaitingPending bool `json:"waitingPending,omitempty"`
}

// menuBarObservationStarted is set once launch has started observing (the
// activity_start task), whether or not observing is on.
var menuBarObservationStarted atomic.Bool

// menuBarWaitingMemory is the last waiting list a read produced, in memory
// only (titles are personal content and are never written to disk). A
// refresh that is slow or fails keeps showing it; the error is only for a
// launch where no read has worked yet.
//
// It also decides when the list is read at all. The five-second status tick
// refreshes the header and the brief row; the waiting list is read only
// after something that can change it (a main-chat write, the popover
// opening, a privacy withdraw, another language), after a failed read once
// menuBarWaitingRetry has passed, and otherwise once menuBarWaitingMaxAge
// old, since cards can expire on their own.
type menuBarWaitingMemory struct {
	succeeded bool
	held      bool
	dirty     bool
	language  string
	waiting   int
	ids       []string
	items     []menuBarWaitingItem
	readAt    time.Time
	retryAt   time.Time
}

const (
	// The menu bar summary is periodic (at most hourly); a real change in
	// the waiting set or opening the popover still reads it sooner.
	menuBarWaitingMaxAge = time.Hour
	menuBarWaitingRetry  = 30 * time.Second
)

// begin says whether this refresh reads the waiting list, and takes the
// change it reads: one that lands during the read marks it again.
func (m *menuBarWaitingMemory) begin(now time.Time, language string) bool {
	if now.Before(m.retryAt) {
		return false
	}
	if m.held && !m.dirty && m.language == language && now.Sub(m.readAt) < menuBarWaitingMaxAge {
		return false
	}
	m.dirty = false
	return true
}

// changed marks the held list as out of date; the next refresh reads it.
func (m *menuBarWaitingMemory) changed() { m.dirty = true }

// settle records a good read, or puts the held list into a view that did
// not read one (read false) or whose read failed.
func (m *menuBarWaitingMemory) settle(view *menuBarView, read bool, err error, now time.Time) {
	if read && err == nil {
		m.succeeded, m.held, m.language, m.waiting = true, true, view.Language, view.Waiting
		m.ids = append([]string(nil), view.WaitingIDs...)
		m.items = append([]menuBarWaitingItem(nil), view.Items...)
		m.readAt, m.retryAt = now, time.Time{}
		return
	}
	if read {
		m.dirty, m.retryAt = true, now.Add(menuBarWaitingRetry)
	}
	view.Error, view.WaitingPending = "", false
	view.Waiting, view.WaitingIDs, view.Items = 0, nil, []menuBarWaitingItem{}
	switch {
	case m.held && m.language == view.Language:
		view.Waiting = m.waiting
		view.WaitingIDs = append([]string(nil), m.ids...)
		view.Items = append([]menuBarWaitingItem(nil), m.items...)
	case m.succeeded:
		view.WaitingPending = true
	default:
		view.Error = view.Words["failed"]
	}
	view.Label = menuBarLabel(view.Words, view.State, view.Waiting)
}

// withdraw drops the held list after a privacy or source change; the next
// good read fills it again.
func (m *menuBarWaitingMemory) withdraw() {
	m.held, m.waiting, m.ids, m.items = false, 0, nil, nil
}

func menuBarDuration(seconds float64) string {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return ""
	}
	total := int(math.Round(seconds))
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
func menuBarLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// menuBarNeedNoChat prefixes a Needs you item that has no chat card, so
// clicking it opens Tasks › Needs you instead of Chat.
const menuBarNeedNoChat = "need:"

// menuBarNeedItems is the first rows of Needs you in Tasks' order, and the id
// of every row so a new one lights the dot.
func menuBarNeedItems(needs []TodayNeed, lang string) ([]menuBarWaitingItem, []string) {
	items := []menuBarWaitingItem{}
	ids := make([]string, 0, len(needs))
	for _, need := range needs {
		id, session := menuBarNeedNoChat+need.ID, ""
		if need.TargetKind == "chat_item" && need.TargetID != "" {
			id, session = need.TargetID, need.ChatID
		}
		ids = append(ids, id)
		if len(items) == menuBarMaxWaiting {
			continue
		}
		title, detail := menuBarNeedLines(need, lang)
		items = append(items, menuBarWaitingItem{ID: id, SessionID: session, Title: menuBarLine(title), Detail: menuBarLine(detail)})
	}
	return items, ids
}

// menuBarNeedLines is one item format (shell review v1.0.2 B7): the sentence,
// then what it's about. A chat card gives its headline and body; any other
// Needs you item gives its sentence alone.
func menuBarNeedLines(need TodayNeed, lang string) (string, string) {
	language := normalizeUILanguage(lang)
	if need.Item != nil {
		var card mainChatProactive
		if json.Unmarshal([]byte(need.Item.ToolInput), &card) == nil {
			if card.Kind == "digest" {
				card = menuBarDigestCard(card, need.Sentence)
			}
			title, detail := card.Headline, card.Body
			if copy, ok := card.Localized[language]; ok {
				title, detail = copy.Headline, copy.Body
			}
			if strings.TrimSpace(title) != "" {
				return title, detail
			}
		}
	}
	if local := need.SentenceLocalized[language]; strings.TrimSpace(local) != "" {
		return local, ""
	}
	return need.Sentence, ""
}

// menuBarDigestCard is the card in a digest that the need was projected from.
func menuBarDigestCard(digest mainChatProactive, sentence string) mainChatProactive {
	for _, card := range digest.Items {
		line := strings.TrimSpace(card.Body)
		if line == "" {
			line = strings.TrimSpace(card.Headline)
		}
		if line == strings.TrimSpace(sentence) {
			return card
		}
	}
	return mainChatProactive{}
}

func menuBarLabel(w map[string]string, state string, waiting int) string {
	if waiting == 0 {
		return menuBarFill(w["labelNone"], "state", state)
	}
	return menuBarFill(w["label"], "state", state, "n", fmt.Sprint(waiting))
}

// menuBarDay is the brief's day: Today, Yesterday, a weekday within the
// week, else the date (shell-review v1.0.0 B4: never a time with no day).
func menuBarDay(w map[string]string, lang string, when, now time.Time) string {
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	days := int(math.Round(day(now).Sub(day(when)).Hours() / 24))
	zh := normalizeUILanguage(lang) == "zh-CN"
	switch {
	case days == 0:
		return w["today"]
	case days == 1:
		return w["yesterday"]
	case days > 1 && days < 7:
		if zh {
			return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[when.Weekday()]
		}
		return when.Format("Monday")
	case zh:
		return fmt.Sprintf("%d月%d日", when.Month(), when.Day())
	default:
		return when.Format("Jan 2")
	}
}

// menuBarBriefRow picks the brief row's state from the newest brief, whether
// one is being made, and the schedule: while today's is still being tried,
// the row is Today's tries line; otherwise it is the latest brief with its day.
func menuBarBriefRow(w map[string]string, lang string, latest MobileBriefV1, hasLatest, building bool, health MobileBriefHealth, now time.Time) menuBarBrief {
	if building {
		key := "making"
		if health.PendingSlot == mobileBriefSlotMorning {
			key = "makingMorning"
		} else if health.PendingSlot == mobileBriefSlotEvening {
			key = "makingEvening"
		}
		return menuBarBrief{Kind: "making", Title: w[key]}
	}
	if health.PendingSlot != "" && health.PendingCause == "signed_out" && health.PendingAssistant != "" && health.PendingAttempts > 0 {
		key := "signedOut"
		if health.PendingAttempts == 1 {
			key = "signedOutOnce"
		}
		return menuBarBrief{Kind: "sign_in", ID: health.PendingAssistant, Title: menuBarFill(w[key], "n", fmt.Sprint(health.PendingAttempts), "ai", menuBarAssistantName(w, health.PendingAssistant))}
	}
	today := now.Local().Format("2006-01-02")
	if retry, err := time.Parse(time.RFC3339, health.RetryAt); health.PendingSlot != "" && err == nil && retry.After(now) {
		at := observationMenuClock(retry.Local(), lang)
		attempts := health.PendingAttempts
		if attempts == 0 && health.FailingSlot == health.PendingSlot {
			attempts = health.FailedAttempts
		}
		title := menuBarFill(w["retry"], "time", at)
		if attempts == 1 {
			title = menuBarFill(w["triedOnce"], "time", at)
		} else if attempts > 1 {
			title = menuBarFill(w["tried"], "n", fmt.Sprint(attempts), "time", at)
		}
		if attempts > 0 {
			key := ""
			switch health.PendingCause {
			case "timeout":
				key = "triedTimeout"
			case "signed_out":
				key = "triedSignedOut"
			}
			if key != "" {
				if attempts == 1 {
					key += "Once"
				}
				title = menuBarFill(w[key], "n", fmt.Sprint(attempts), "time", at, "ai", menuBarAssistantName(w, health.PendingAssistant))
			}
		}
		return menuBarBrief{Kind: "trying", Title: title}
	}
	// The current brief, whatever its date: it stays until the next arrives.
	if hasLatest {
		// The kind the title names (design section 1): a brief someone asked
		// for is "Brief", whatever its hour.
		kind := mobileBriefPackageKind(latest)
		asOf := mobileBriefAsOfTime(latest).Local()
		at := menuBarFill(w["dayTime"], "day", menuBarDay(w, lang, asOf, now.Local()), "time", observationMenuClock(asOf, lang))
		if mobileBriefIsAhead(latest) {
			// An early brief names the time it was made, never its slot (R64); the same line as the Today title.
			at = menuBarMadeAheadMeta(w, lang, latest)
		}
		// Play only where the words are too: a recording never plays alone.
		if latest.Audio != nil && latest.Audio.Bytes > 0 && latest.Audio.Status == mobileBriefAudioStatusOK && len(latest.Narrative) > 0 {
			title := w["brief"]
			if kind == mobileBriefKindWeek || kind == mobileBriefSlotMorning || kind == mobileBriefSlotEvening {
				title = w[kind]
			}
			meta := at
			if length := menuBarDuration(latest.Audio.DurationSeconds); length != "" {
				meta += " · " + length
			}
			return menuBarBrief{Kind: "ready", ID: latest.BriefID, Title: title, Meta: meta, Seconds: latest.Audio.DurationSeconds}
		}
		// No recording: the row reads the brief, and the player isn't there.
		return menuBarBrief{Kind: "read", ID: latest.BriefID, Title: w["read"], Meta: at}
	}
	row := menuBarBrief{Kind: "none", Title: w["none"]}
	if next, err := time.Parse(time.RFC3339, health.NextRunAt); err == nil {
		next = next.Local()
		key := "next"
		if next.Format("2006-01-02") == today {
			key = "nextToday"
		}
		row.Meta = menuBarFill(w[key], "time", observationMenuClock(next, lang))
	}
	return row
}

// Reuse the existing read projections, including current source/privacy gates.
// No new queue, stored copy of personal content, or inferred authority. This
// runs in the background refresh only; a click never waits on it.
func (a *App) menuBarView(lang string, readWaiting bool) (perfValue menuBarView, perfErr error) {
	if readWaiting {
		// Only refreshes that read the waiting list are logged; the
		// five-second status tick is not.
		defer logPageBinding("menuBarView", time.Now(), &perfValue)
	}
	w := menuBarWords(lang)
	doing := a.GetDoingNow()
	paused := doing.Kind == doingNowPaused
	observing := a.isActivityMonitorRunning() || a.backgroundListeningRunningNow()
	view := menuBarView{Language: lang, Theme: currentThemePreference(), Words: w, Paused: paused, Items: []menuBarWaitingItem{}}
	switch {
	case paused:
		view.Status, view.State = "paused", menuBarPausedState(w, lang, doing)
	case observing:
		view.Status, view.State = "observing", w["observing"]
	case !menuBarObservationStarted.Load():
		// Launch starts observing a little after the window: until then
		// the header says so instead of "Observing is off".
		view.Status, view.State = "starting", w["starting"]
	default:
		view.Status, view.State = "off", w["off"]
	}
	view.Menu = menuBarMenu(lang, paused)
	view.Label = menuBarLabel(w, view.State, 0)
	if status := a.GetUpdateStatus(); status.Status == "update-downloaded" && status.Version != "" {
		view.Update = status.Version
		view.UpdateRestart = status.RestartNeeded
	}
	view.Models = a.menuBarModels(w)
	view.SignOut = a.menuBarSignOutRow(routeOrder(loadAIProviderConfig()), w)
	// The launch preload can run before startup starts the brief service.
	if svc := a.ensureMobileBriefService(); svc != nil {
		latest, ok := a.menuBarLatestBrief(svc)
		building := false
		if b, can := svc.(mobileBriefBuilding); can {
			building = b.Building()
		}
		view.Brief = menuBarBriefRow(w, lang, latest, ok, building, a.menuBarBriefHealth(svc, latest, ok), time.Now())
	} else {
		view.Brief = menuBarBrief{Kind: "none", Title: w["none"]}
	}
	if view.Brief.Kind == "sign_in" {
		view.Menu[1] = menuBarMenuItem{ID: "signIn:" + view.Brief.ID, Title: w["signIn"]}
	}
	if !readWaiting {
		return view, nil
	}
	// Waiting on you is what Tasks › Needs you lists (evening walk B7):
	// never a brief or another chat card that Tasks doesn't list.
	needs, err := a.GetNeedsYou(0)
	if err != nil {
		return view, err
	}
	view.Items, view.WaitingIDs = menuBarNeedItems(needs.Items, lang)
	view.Waiting = len(needs.Items)
	view.Label = menuBarLabel(w, view.State, view.Waiting)
	return view, nil
}

// menuBarModels is the popover's model download line, or "" when nothing
// setup confirmed is downloading. Deferred jobs (Later) are not shown.
func (a *App) menuBarModels(w map[string]string) string {
	manager, err := a.setupModelManager()
	if err != nil {
		return ""
	}
	percent, active := manager.Progress()
	if !active {
		return ""
	}
	return menuBarFill(w["models"], "percent", fmt.Sprint(percent))
}

// menuBarPausedState is the header while paused: until when, if a timer ends it.
func menuBarPausedState(w map[string]string, lang string, doing DoingNow) string {
	if doing.PausedUntil <= 0 {
		return w["paused"]
	}
	return menuBarFill(w["pausedUntil"], "time", observationMenuClock(time.UnixMilli(doing.PausedUntil).Local(), lang))
}

// menuBarLatestBrief is the saved finished brief Today opens (#506), not a
// rebuild through every delivery gate on each status tick.
func (a *App) menuBarLatestBrief(svc mobileBriefSource) (MobileBriefV1, bool) {
	if service, isService := svc.(*mobileBriefService); isService && service != nil {
		key := a.menuBarBriefKey(service)
		if brief, ok, hit := menuBarBriefMemo.get(key, time.Now()); hit {
			return brief, ok
		}
		if latest, ok, err := service.finishedLatest(); err == nil {
			menuBarBriefMemo.put(key, latest, ok, time.Now())
			return latest, ok
		}
	}
	return svc.LatestMobileBrief()
}

// menuBarBriefHealth uses the already-read brief and the cached run record
// for the retry row. Time and the building state remain live on every tick.
func (a *App) menuBarBriefHealth(source mobileBriefSource, latest MobileBriefV1, hasLatest bool) MobileBriefHealth {
	service, ok := source.(*mobileBriefService)
	if !ok || service == nil {
		return source.Health()
	}
	settings := service.config.load()
	now := service.now()
	view := MobileBriefHealth{}
	pending, found := mobileBriefPendingOccurrence(now, service.location, settings, latest, hasLatest, func(at time.Time) bool {
		return service.catchUpAllowed(at, now, settings)
	})
	if found {
		view.PendingSlot = pending.Slot
		if run, ok := menuBarRunMemo.failedRun(service.runs, pending.BriefID); ok {
			view.PendingAttempts, view.PendingCause, view.PendingAssistant = run.Attempts, run.FailureCause, run.FailureAssistant
		}
		service.mu.Lock()
		retryAfter, retryFor := service.retryAfter, service.retryFor
		service.mu.Unlock()
		if retryAfter.After(now) && retryFor == pending.BriefID && retryAfter.Before(mobileBriefSlotCloses(pending, service.location, settings)) {
			view.RetryAt = formatMobileBriefTime(retryAfter)
		}
	}
	if !hasLatest && view.RetryAt == "" && settings.Enabled {
		// Only the empty row displays NextRunAt. Adaptive predictions can
		// change with activity, so ask the scheduler when this row is visible.
		if settings.Adaptive {
			if next, found := service.nextAdaptivePrediction(now, settings); found {
				view.NextRunAt = formatMobileBriefTime(mobileBriefReadyAt(next, now, service.location, settings))
			}
		} else if next, found := mobileBriefScheduledNextRun(now, service.location, settings, service.scheduleFacts(now, settings)); found {
			view.NextRunAt = formatMobileBriefTime(next.ScheduledAt)
		}
	}
	return view
}

// menuBarRunMemo parses and indexes the run record only when its contents
// change. Reading the small record does not read or stamp the saved brief.
var menuBarRunMemo menuBarRunCache

type menuBarRunCache struct {
	mu     sync.Mutex
	store  *mobileBriefRunStore
	record string
	failed map[string]mobileBriefRun
}

func (c *menuBarRunCache) failedRun(store *mobileBriefRunStore, briefID string) (mobileBriefRun, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	store.mu.Lock()
	data, _ := os.ReadFile(store.path)
	store.mu.Unlock()
	if c.store != store || c.record != string(data) {
		c.store, c.record = store, string(data)
		c.failed = make(map[string]mobileBriefRun)
		var file mobileBriefRunsFile
		if json.Unmarshal(data, &file) == nil && file.Version == mobileBriefRunsVersion {
			for _, run := range file.Runs {
				if run.Status == mobileBriefRunFailed {
					c.failed[run.BriefID] = run
				}
			}
		}
	}
	run, ok := c.failed[briefID]
	return run, ok
}

// menuBarBriefMemo keeps the status tick's brief row input while nothing the
// saved brief's read depends on has changed, so the five-second tick does not
// stamp every source file (finishedReadKey) each time. It is read again at
// least once a minute.
var menuBarBriefMemo menuBarBriefCache

const menuBarBriefMemoMaxAge = time.Minute

type menuBarBriefCache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	brief MobileBriefV1
	ok    bool
}

func (c *menuBarBriefCache) get(key string, now time.Time) (MobileBriefV1, bool, bool) {
	if key == "" {
		return MobileBriefV1{}, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != key || now.Sub(c.at) >= menuBarBriefMemoMaxAge {
		return MobileBriefV1{}, false, false
	}
	return c.brief, c.ok, true
}

func (c *menuBarBriefCache) put(key string, brief MobileBriefV1, ok bool, now time.Time) {
	if key == "" {
		return
	}
	c.mu.Lock()
	c.key, c.at, c.brief, c.ok = key, now, brief, ok
	c.mu.Unlock()
}

// menuBarBriefKey stamps, without reading any source, what changes the
// saved brief's read: the brief folder (a stored or patched brief), the
// privacy epoch, memory writes and the memory snapshot, Closed Eyes and the
// day.
func (a *App) menuBarBriefKey(service *mobileBriefService) string {
	if service.store == nil || service.store.dir == "" {
		return ""
	}
	info, err := os.Stat(service.store.dir)
	if err != nil {
		return ""
	}
	snapshot, err := a.memorySnapshot()
	if err != nil {
		return ""
	}
	loc := service.location
	if loc == nil {
		loc = time.Local
	}
	return fmt.Sprintf("%p|%d|%d|%d|%p|%t|%s", service, info.ModTime().UnixNano(), mobileBriefReadEpoch.Load(),
		a.memoryExternalReads.revision.Load(), snapshot, a.IsClosedEyesModeEnabled(), service.now().In(loc).Format("2006-01-02"))
}

// menuBarMadeAheadMeta is the made-at phrase of an early brief in the menu bar's
// words, where it starts the line (so it is capitalized in English; v1.0.2):
// "Made at 1:20 AM" on the brief's own day, "Made last night at 11:20 PM" for the day
// before at 6 PM or later, "Made yesterday at 3:10 PM" for the day before earlier,
// and "Made Nov 6 at 3:10 PM" (zh-CN "11月6日 15:10 生成") for an older day. A brief with
// no time gives no phrase, so the row shows its title alone. zh-CN uses the same
// 24-hour clock as every zh-CN time.
func menuBarMadeAheadMeta(w map[string]string, lang string, brief MobileBriefV1) string {
	made := mobileBriefAsOfTime(brief)
	if made.IsZero() {
		return ""
	}
	loc := time.Local
	if zone, err := time.LoadLocation(brief.Timezone); err == nil && brief.Timezone != "" {
		loc = zone
	}
	made = made.In(loc)
	clock := observationMenuClock(made, lang)
	day, err := time.ParseInLocation("2006-01-02", brief.LocalDate, loc)
	if err != nil {
		return menuBarFill(w["madeAt"], "time", clock)
	}
	switch made.Format("2006-01-02") {
	case brief.LocalDate:
		return menuBarFill(w["madeAt"], "time", clock)
	case day.AddDate(0, 0, -1).Format("2006-01-02"):
		if made.Hour() >= 18 {
			return menuBarFill(w["madeLastNight"], "time", clock)
		}
		return menuBarFill(w["madeYesterday"], "time", clock)
	}
	date := made.Format("Jan 2")
	if normalizeUILanguage(lang) == "zh-CN" {
		date = fmt.Sprintf("%d月%d日", int(made.Month()), made.Day())
	}
	return menuBarFill(w["madeOnDate"], "date", date, "time", clock)
}

// menuBarSignOut is the popover's first row when the chosen assistant is
// confirmed signed out (R75). SignIn opens that assistant's own sign-in.
type menuBarSignOut struct {
	Assistant string `json:"assistant"`
	Line      string `json:"line"`
	SignIn    string `json:"signIn"`
}

// menuBarSignOutRow names the first confirmed signed-out assistant in order,
// or returns nil. The line is the same as Today's.
func (a *App) menuBarSignOutRow(order []string, w map[string]string) *menuBarSignOut {
	assistant, ok := a.signOut.firstConfirmed(order)
	if !ok {
		return nil
	}
	return &menuBarSignOut{
		Assistant: assistant,
		Line:      menuBarFill(w["signedOutLine"], "assistant", assistantSignOutDisplayName(assistant)),
		SignIn:    w["signIn"],
	}
}
