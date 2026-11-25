package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/user"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// Isoform speaks first. When the main chat has no conversation yet, Isoform
// posts its own first message, then the main chat asks for one thing at a
// time: what to call the user (only when no name is stored), an assistant to
// think with (only when none is ready), and each macOS permission. Each
// answered ask becomes a quiet line in the main chat. Nothing here ever shows
// a macOS prompt: the renderer calls
// RequestPermission only on the user's tap, and this file only reads state
// through the side-effect-free probe.
//
// Never re-ask: a step is closed once it has an answer. A step found granted
// (or the assistant found ready) is answered "granted" silently the first time
// it is seen, so a later revoke does not reopen it; a "not now" is kept. The
// onboarding window closes the asks it already made (CloseMainChatSetupSteps),
// so the two never ask for the same thing twice in a row. The answers persist
// in ui_state, so the sequence resumes where it stopped after a restart.

// mainChatSetupStateKey is the ui_state key holding the sequence's progress.
const mainChatSetupStateKey = "main_chat_setup.v1"

// mainChatSetupCorruptKey keeps an unreadable stored value before it is reset.
const mainChatSetupCorruptKey = "main_chat_setup.v1.corrupt"

// mainChatSetupOnboardingKey holds the answers the onboarding window gave
// (CloseMainChatSetupSteps): onboarding may change them while the user moves
// back and forth, and an unreadable progress value is rebuilt with them.
const mainChatSetupOnboardingKey = "main_chat_setup.v1.onboarding"

// onboardingStateKey is the renderer's onboarding state (App.tsx).
const onboardingStateKey = "yansu.onboarding.state"

// mainChatSetupTool marks the record line an answered step leaves behind.
const mainChatSetupTool = "main_chat_setup"

// Setup step kinds.
const (
	mainChatSetupKindName       = "name"
	mainChatSetupKindAssistant  = "assistant"
	mainChatSetupKindPermission = "permission"
)

// Setup step keys outside the permission set.
const (
	mainChatSetupStepName      = "name"
	mainChatSetupStepAssistant = "assistant"
	mainChatSetupStepFirstTask = "first_task"
)

// Answers and statuses.
const (
	mainChatSetupGranted = "granted"
	mainChatSetupNotNow  = "not_now"
	mainChatSetupDone    = "done"
	mainChatSetupPending = "pending"
)

// mainChatSetupPermissionOrder is the order permissions are asked in, on
// macOS. Keys the platform does not list (system audio on older macOS) are
// skipped. Contacts is offered only by the People consent sheet.
var mainChatSetupPermissionOrder = []string{
	"screen_recording",
	"accessibility",
	"microphone",
	"system_audio",
	"calendars",
	"notifications",
}

// mainChatSetupPermissionNames are the names record lines use, per language.
var mainChatSetupPermissionNames = map[string]map[string]string{
	"en": {
		"screen_recording": "Screen Recording",
		"accessibility":    "Accessibility",
		"microphone":       "Microphone",
		"system_audio":     "System Audio Recording",
		"calendars":        "Calendars",
		"notifications":    "Notifications",
		"contacts":         "Contacts",
	},
	"zh-CN": {
		"screen_recording": "屏幕录制",
		"accessibility":    "辅助功能",
		"microphone":       "麦克风",
		"system_audio":     "系统音频录制",
		"calendars":        "日历",
		"notifications":    "通知",
		"contacts":         "通讯录",
	},
}

// Test seams: the live checks the sequence reads. Production reads the
// platform permissions through the probe that starts nothing, only on macOS,
// the assistant readiness, the name stored in the People "self" profile, and
// the Mac account's full name to suggest one.
var (
	mainChatSetupPermissions = func(*App) PlatformPermissions {
		if goruntime.GOOS != "darwin" {
			return PlatformPermissions{}
		}
		return getPlatformPermissions()
	}
	mainChatSetupAssistantReady = func(a *App) bool { return a.AIReady() }
	mainChatSetupSelfName       = storedSelfName
	mainChatSetupAccountName    = accountFullName
)

// storedSelfName is the name the user gave Isoform (the People "self"
// profile), or "".
func storedSelfName(a *App) string {
	if a == nil || a.store == nil {
		return ""
	}
	rows, err := a.store.ListPrincipalsByKind(context.Background(), storage.PrincipalKindSelf)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rows[0].DisplayName)
}

// accountFullName is the full name of this Mac's user account ("Ada
// Lovelace"), or "" when the account has none beyond its short login name.
func accountFullName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(u.Name)
	if name == "" || name == u.Username {
		return ""
	}
	return name
}

// MainChatSetupStep is one ask in the sequence, as the renderer sees it.
type MainChatSetupStep struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // pending | granted | not_now | done
	// Suggestion prefills the name step: the stored name, else the Mac
	// account's full name. Empty for every other step.
	Suggestion string `json:"suggestion,omitempty"`
	// Asked is true for a step the main chat asks or has shown: pending, or
	// offered before. A step closed before the chat reached it (already
	// granted, or answered in the onboarding window) is not, so the
	// progress the chat shows counts only its own asks.
	Asked bool `json:"asked"`
}

// MainChatSetupState is the whole sequence and the step to show now ("" when
// nothing is left).
type MainChatSetupState struct {
	Steps []MainChatSetupStep `json:"steps"`
	Next  string              `json:"next"`
}

// mainChatSetupStored is what persists between launches.
type mainChatSetupStored struct {
	// IntroAt is when the first message was posted, or when the main chat
	// was found to have a conversation already (then no message was posted).
	IntroAt int64 `json:"introAt"`
	// IntroPosted says the first message was posted by this sequence.
	IntroPosted bool              `json:"introPosted"`
	Answers     map[string]string `json:"answers"`
	// Offered is the step shown last; when it turns out granted (the user
	// turned it on in System Settings) it is recorded with a line.
	Offered string `json:"offered"`
	// Shown holds every step the main chat has offered.
	Shown map[string]bool `json:"shown,omitempty"`
}

// offer remembers the step shown now.
func (stored *mainChatSetupStored) offer(step string) {
	stored.Offered = step
	if step == "" {
		return
	}
	if stored.Shown == nil {
		stored.Shown = map[string]bool{}
	}
	stored.Shown[step] = true
}

var mainChatSetupMu sync.Mutex

// mainChatSetupLiveState is what is already in place, read before the lock.
type mainChatSetupLiveState struct {
	granted        map[string]bool
	order          []string
	assistantReady bool
	selfName       string
	accountName    string
}

func (a *App) mainChatSetupLive() mainChatSetupLiveState {
	live := mainChatSetupLiveState{granted: map[string]bool{}, order: []string{}}
	listed := map[string]bool{}
	for _, item := range mainChatSetupPermissions(a).Items {
		listed[item.Key] = true
		live.granted[item.Key] = item.Granted
	}
	for _, key := range mainChatSetupPermissionOrder {
		if listed[key] {
			live.order = append(live.order, key)
		}
	}
	live.assistantReady = mainChatSetupAssistantReady(a)
	live.selfName = mainChatSetupSelfName(a)
	if live.selfName == "" {
		live.accountName = mainChatSetupAccountName()
	}
	return live
}

// isGranted reports whether a step is already in place: a stored name for
// the name step, a ready assistant, a granted permission.
func (live mainChatSetupLiveState) isGranted(step string) bool {
	switch step {
	case mainChatSetupStepName:
		return live.selfName != ""
	case mainChatSetupStepAssistant:
		return live.assistantReady
	default:
		return live.granted[step]
	}
}

// loadMainChatSetup reads the stored progress. A read error is returned so
// the caller does not overwrite answers it could not read; an unreadable
// value is copied aside and the progress starts over.
func (a *App) loadMainChatSetup(ctx context.Context) (mainChatSetupStored, error) {
	stored := mainChatSetupStored{Answers: map[string]string{}}
	raw, err := a.store.GetUIState(ctx, mainChatSetupStateKey)
	if err != nil {
		return stored, fmt.Errorf("read setup progress: %w", err)
	}
	if raw == "" {
		return stored, nil
	}
	if json.Unmarshal([]byte(raw), &stored) != nil {
		log.Printf("[main-chat-setup] stored progress is unreadable; kept under %s and rebuilt from the chat", mainChatSetupCorruptKey)
		_ = a.store.SetUIState(ctx, mainChatSetupCorruptKey, raw)
		return a.rebuildMainChatSetup(ctx), nil
	}
	if stored.Answers == nil {
		stored.Answers = map[string]string{}
	}
	return stored, nil
}

// rebuildMainChatSetup recovers the answers from the record lines the main
// chat already holds, so an unreadable stored value does not reopen them.
func (a *App) rebuildMainChatSetup(ctx context.Context) mainChatSetupStored {
	stored := mainChatSetupStored{Answers: map[string]string{}}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return stored
	}
	items, err := a.store.LoadStreamItems(ctx, main.LocalID)
	if err != nil {
		return stored
	}
	for _, item := range items {
		if item.Kind != "system" || item.ToolName != mainChatSetupTool {
			continue
		}
		var record struct {
			Step   string `json:"step"`
			Answer string `json:"answer"`
		}
		if json.Unmarshal([]byte(item.ToolInput), &record) == nil && record.Step != "" && record.Answer != "" {
			stored.Answers[record.Step] = record.Answer
			stored.offer(record.Step)
		}
	}
	stored.Offered = ""
	// Onboarding's answers leave no line in the chat; they are kept apart.
	for step, answer := range a.loadOnboardingSetupAnswers(ctx) {
		if stored.Answers[step] == "" {
			stored.Answers[step] = answer
		}
	}
	return stored
}

func (a *App) loadOnboardingSetupAnswers(ctx context.Context) map[string]string {
	answers := map[string]string{}
	raw, err := a.store.GetUIState(ctx, mainChatSetupOnboardingKey)
	if err != nil || raw == "" || json.Unmarshal([]byte(raw), &answers) != nil || answers == nil {
		return map[string]string{}
	}
	return answers
}

func (a *App) saveOnboardingSetupAnswers(ctx context.Context, answers map[string]string) {
	raw, err := json.Marshal(answers)
	if err != nil {
		return
	}
	if err := a.store.SetUIState(ctx, mainChatSetupOnboardingKey, string(raw)); err != nil {
		log.Printf("[main-chat-setup] save onboarding answers: %v", err)
	}
}

// onboardingFinished reports whether the onboarding window is done (finished,
// skipped with Skip setup, or turned off for good in Settings).
func (a *App) onboardingFinished(ctx context.Context) bool {
	raw, err := a.store.GetUIState(ctx, onboardingStateKey)
	if err != nil {
		return false
	}
	return onboardingStateDone(raw)
}

func (a *App) saveMainChatSetup(ctx context.Context, stored mainChatSetupStored) {
	raw, err := json.Marshal(stored)
	if err != nil {
		return
	}
	if current, err := a.store.GetUIState(ctx, mainChatSetupStateKey); err == nil && current == string(raw) {
		return
	}
	if err := a.store.SetUIState(ctx, mainChatSetupStateKey, string(raw)); err != nil {
		log.Printf("[main-chat-setup] save: %v", err)
	}
}

// mainChatIntroText is Isoform's first message in the user's language. It is
// worded apart from the onboarding window's copy so the two never repeat.
// moreBelow mentions the asks that follow, only when some do.
func mainChatIntroText(lang string, moreBelow bool) string {
	var greeting, invite, below string
	switch normalizeUILanguage(lang) {
	case "zh-CN":
		greeting = "你好，我是 Isoform。我在这台电脑上安静地工作：了解你的工作方式，记住你手头在忙的事，你交给我的事我来处理。只有值得你花时间的事，我才会主动找你。"
		invite = "想让我做什么，随时在这里告诉我。"
		below = "下面我会一次问一件事，让我能帮上更多忙。每一项都可以先跳过，之后也能在设置里修改。"
	default:
		greeting = "Hi, I'm Isoform. I work quietly on this computer: I learn how you work, keep track of what you're juggling, and take things off your plate when you hand them to me. I'll only reach out when something is worth your time."
		invite = "Whenever you want something done, just tell me here."
		below = "Below I'll ask for a few things, one at a time, so I can help with more. Each one can wait, and you can change any of them later in Settings."
	}
	parts := []string{greeting, invite}
	if moreBelow {
		parts = append(parts, below)
	}
	return strings.Join(parts, "\n\n")
}

// ensureMainChatIntroLocked posts the first message once, and only into a
// main chat with no conversation yet; a main chat that already has one is
// marked as introduced without a message, and its legacy first-task card is
// closed. lang is the language to speak; "" means the language is not known
// yet, and then nothing is posted. The invitation mentions the asks below only
// when there are some. Callers hold mainChatSetupMu.
func (a *App) ensureMainChatIntroLocked(ctx context.Context, stored *mainChatSetupStored, live mainChatSetupLiveState, lang string) {
	if stored.IntroAt != 0 {
		return
	}
	if a.engagement != nil && !a.firstWeekInstall(ctx).IsZero() {
		// The paced welcome owns the first greeting for a new install. It can
		// wait for quiet hours without the setup read posting a second one.
		stored.IntroAt = a.engagement.now().UnixMilli()
		return
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		log.Printf("[main-chat-setup] intro skipped: %v", err)
		return
	}
	history, err := a.store.LoadConversationHistory(ctx, main.LocalID, 1)
	if err != nil {
		log.Printf("[main-chat-setup] intro check: %v", err)
		return
	}
	if len(history) > 0 {
		stored.IntroAt = time.Now().UnixMilli()
		if stored.Answers[mainChatSetupStepFirstTask] == "" {
			stored.Answers[mainChatSetupStepFirstTask] = mainChatSetupDone
		}
		return
	}
	if lang == "" {
		return
	}
	moreBelow := false
	for _, step := range computeMainChatSetup(*stored, live).Steps {
		if step.Status == mainChatSetupPending {
			moreBelow = true
			break
		}
	}
	id := a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{
		ID:      uuid.NewString(),
		Kind:    "assistant",
		Content: mainChatIntroText(lang, moreBelow),
	}, "intro")
	if id == "" {
		// Not stored; the next read tries again.
		return
	}
	stored.IntroAt = time.Now().UnixMilli()
	stored.IntroPosted = true
}

// computeMainChatSetup builds the sequence from live state and answers.
func computeMainChatSetup(stored mainChatSetupStored, live mainChatSetupLiveState) MainChatSetupState {
	state := MainChatSetupState{Steps: []MainChatSetupStep{}}
	add := func(key, kind string) {
		status := mainChatSetupPending
		if answer := stored.Answers[key]; answer != "" {
			status = answer
		} else if live.isGranted(key) {
			status = mainChatSetupGranted
		}
		asked := status == mainChatSetupPending || stored.Shown[key]
		step := MainChatSetupStep{Key: key, Kind: kind, Status: status, Asked: asked}
		if kind == mainChatSetupKindName {
			step.Suggestion = live.selfName
			if step.Suggestion == "" {
				step.Suggestion = live.accountName
			}
		}
		state.Steps = append(state.Steps, step)
		if status == mainChatSetupPending && state.Next == "" {
			state.Next = key
		}
	}
	add(mainChatSetupStepName, mainChatSetupKindName)
	add(mainChatSetupStepAssistant, mainChatSetupKindAssistant)
	for _, key := range live.order {
		add(key, mainChatSetupKindPermission)
	}
	return state
}

// settleMainChatSetupLocked records what the live state already answers: the
// step offered last, if it is now granted, gets a record line; any other step
// found granted is closed silently (no card was shown for it). Either way a
// later revoke does not reopen it.
func (a *App) settleMainChatSetupLocked(ctx context.Context, stored *mainChatSetupStored, live mainChatSetupLiveState) {
	steps := append([]string{mainChatSetupStepName, mainChatSetupStepAssistant}, live.order...)
	for _, step := range steps {
		if stored.Answers[step] != "" || !live.isGranted(step) {
			continue
		}
		stored.Answers[step] = mainChatSetupGranted
		if step == stored.Offered {
			a.postMainChatSetupRecord(ctx, step, mainChatSetupGranted, live.selfName)
		}
	}
}

// mainChatSetupRecordText is the line an answered step leaves in the main
// chat, in the user's language (the phone shows it as text; the desktop
// renders its own words from the payload).
func mainChatSetupRecordText(step, answer, lang string) string {
	zh := normalizeUILanguage(lang) == "zh-CN"
	switch step {
	case mainChatSetupStepName:
		// A given name has its own line (mainChatSetupNameRecordText); this
		// is the "not now" answer.
		if zh {
			return "称呼：暂不。"
		}
		return "Your name: not now."
	case mainChatSetupStepAssistant:
		if answer == mainChatSetupGranted {
			if zh {
				return "助手已就绪。"
			}
			return "Assistant ready."
		}
		if zh {
			return "助手：暂不。"
		}
		return "Assistant: not now."
	case mainChatSetupStepFirstTask:
		if zh {
			return "设置完成。"
		}
		return "Setup finished."
	}
	names := mainChatSetupPermissionNames["en"]
	if zh {
		names = mainChatSetupPermissionNames["zh-CN"]
	}
	name := names[step]
	if name == "" {
		name = step
	}
	if answer == mainChatSetupGranted {
		if zh {
			return name + "已开启。"
		}
		return name + " is on."
	}
	if zh {
		return name + "：暂不。"
	}
	return name + ": not now."
}

// mainChatSetupNameRecordText is the line a given name leaves: Isoform says
// what it will call the user.
func mainChatSetupNameRecordText(name, lang string) string {
	if normalizeUILanguage(lang) == "zh-CN" {
		return "我会称呼你为 " + name + "。"
	}
	return "I'll call you " + name + "."
}

// postMainChatSetupRecord adds the record line for an answer. The line is a
// courtesy: when it cannot be stored the answer still stands and is not
// retried, since the card it describes is already gone. name is the stored
// name, which the line a given name leaves carries.
func (a *App) postMainChatSetupRecord(ctx context.Context, step, answer, name string) {
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return
	}
	record := map[string]string{"step": step, "answer": answer}
	content := mainChatSetupRecordText(step, answer, a.currentUILanguage())
	if step == mainChatSetupStepName && answer == mainChatSetupGranted && name != "" {
		record["name"] = name
		content = mainChatSetupNameRecordText(name, a.currentUILanguage())
	}
	payload, _ := json.Marshal(record)
	a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{
		ID:        uuid.NewString(),
		Kind:      "system",
		Content:   content,
		ToolName:  mainChatSetupTool,
		ToolInput: string(payload),
	}, "setup")
}

func emptyMainChatSetup() MainChatSetupState {
	return MainChatSetupState{Steps: []MainChatSetupStep{}}
}

// GetMainChatSetup returns the sequence and the step to show now. lang is the
// renderer's language: the first message is posted in it when the main chat
// is still empty. It records what the live state already answers.
func (a *App) GetMainChatSetup(lang string) (perfValue MainChatSetupState, perfErr error) {
	if err := chatFeatureErr(); err != nil {
		return perfValue, err
	}
	defer logPageBinding("GetMainChatSetup", time.Now(), &perfValue)
	if a == nil || a.store == nil {
		return emptyMainChatSetup(), fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	live := a.mainChatSetupLive()

	mainChatSetupMu.Lock()
	defer mainChatSetupMu.Unlock()
	stored, err := a.loadMainChatSetup(ctx)
	if err != nil {
		return emptyMainChatSetup(), err
	}
	a.ensureMainChatIntroLocked(ctx, &stored, live, lang)
	a.settleMainChatSetupLocked(ctx, &stored, live)
	state := computeMainChatSetup(stored, live)
	stored.offer(state.Next)
	a.saveMainChatSetup(ctx, stored)
	return state, nil
}

// AnswerMainChatSetup records the user's answer to the step shown now and
// returns the sequence. "granted" is recorded only when the live check
// agrees (the renderer asks after its RequestPermission call, or after it
// saved the name with EnsureSelfProfile). An answer for a step that is not
// shown now changes nothing. Legacy first-task answers stay known to the
// binding, but that step is never shown again.
func (a *App) AnswerMainChatSetup(step, answer string) (MainChatSetupState, error) {
	if err := chatFeatureErr(); err != nil {
		return MainChatSetupState{}, err
	}
	if a == nil || a.store == nil {
		return emptyMainChatSetup(), fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	live := a.mainChatSetupLive()

	mainChatSetupMu.Lock()
	defer mainChatSetupMu.Unlock()
	stored, err := a.loadMainChatSetup(ctx)
	if err != nil {
		return emptyMainChatSetup(), err
	}
	known := step == mainChatSetupStepName || step == mainChatSetupStepAssistant || step == mainChatSetupStepFirstTask
	for _, key := range live.order {
		if key == step {
			known = true
		}
	}
	if !known {
		return computeMainChatSetup(stored, live), fmt.Errorf("unknown setup step %q", step)
	}
	switch answer {
	case mainChatSetupGranted, mainChatSetupNotNow, mainChatSetupDone:
	default:
		return computeMainChatSetup(stored, live), fmt.Errorf("unknown setup answer %q", answer)
	}

	a.settleMainChatSetupLocked(ctx, &stored, live)
	current := computeMainChatSetup(stored, live).Next
	if step == current {
		switch answer {
		case mainChatSetupGranted:
			if step != mainChatSetupStepFirstTask && live.isGranted(step) {
				stored.Answers[step] = mainChatSetupGranted
				a.postMainChatSetupRecord(ctx, step, mainChatSetupGranted, live.selfName)
			}
		case mainChatSetupNotNow:
			if step != mainChatSetupStepFirstTask {
				stored.Answers[step] = mainChatSetupNotNow
				a.postMainChatSetupRecord(ctx, step, mainChatSetupNotNow, "")
			}
		case mainChatSetupDone:
			if step == mainChatSetupStepFirstTask {
				stored.Answers[step] = mainChatSetupDone
				a.postMainChatSetupRecord(ctx, step, mainChatSetupDone, "")
			}
		}
	}
	state := computeMainChatSetup(stored, live)
	stored.offer(state.Next)
	a.saveMainChatSetup(ctx, stored)
	return state, nil
}

// ensureMainChatIntroOnStartup posts the first message at launch when the
// main chat is still empty, the user's language is already known and
// onboarding is over (a later launch). On the very first launch the renderer
// posts it with its language through GetMainChatSetup. It waits for the end
// of onboarding because macOS restarts the app in the middle of it after a
// Screen Recording grant, and what onboarding still asks changes what the
// message says.
func (a *App) ensureMainChatIntroOnStartup() {
	if chatFeatureErr() != nil {
		return
	}
	if a == nil || a.store == nil {
		return
	}
	ctx := context.Background()
	lang, err := a.store.GetUIState(ctx, uiLanguageStateKey)
	if err != nil || lang == "" || !a.onboardingFinished(ctx) {
		return
	}
	live := a.mainChatSetupLive()
	mainChatSetupMu.Lock()
	defer mainChatSetupMu.Unlock()
	stored, err := a.loadMainChatSetup(ctx)
	if err != nil || stored.IntroAt != 0 {
		return
	}
	a.ensureMainChatIntroLocked(ctx, &stored, live, lang)
	a.saveMainChatSetup(ctx, stored)
}

// CloseMainChatSetupSteps closes the asks the onboarding window already made,
// so the main chat does not make them again right after: each permission its
// Permissions step showed (on when granted, otherwise "not now"),
// Notifications when the user chose to hear nothing, and the first task once
// its step was answered or skipped. Closing posts no record line. An answer
// the chat holds of its own is kept; one onboarding gave is given again, so
// a permission turned on after going back is recorded as on. Keys the main
// chat does not ask for are ignored. The assistant is never closed here:
// nothing in the chat works without one, so its card stays the way forward.
func (a *App) CloseMainChatSetupSteps(steps []string) error {
	if err := chatFeatureErr(); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	live := a.mainChatSetupLive()
	mainChatSetupMu.Lock()
	defer mainChatSetupMu.Unlock()
	stored, err := a.loadMainChatSetup(ctx)
	if err != nil {
		return err
	}
	fromOnboarding := a.loadOnboardingSetupAnswers(ctx)
	for _, step := range steps {
		if stored.Answers[step] != "" && fromOnboarding[step] == "" {
			continue
		}
		var answer string
		switch step {
		case mainChatSetupStepFirstTask:
			answer = mainChatSetupDone
		case mainChatSetupStepAssistant:
			// See above: the main chat keeps asking for an assistant.
			continue
		default:
			if !slices.Contains(live.order, step) {
				continue
			}
			answer = mainChatSetupNotNow
			if live.isGranted(step) {
				answer = mainChatSetupGranted
			}
		}
		stored.Answers[step] = answer
		fromOnboarding[step] = answer
	}
	a.saveOnboardingSetupAnswers(ctx, fromOnboarding)
	a.saveMainChatSetup(ctx, stored)
	return nil
}

// ReopenMainChatSetupSteps undoes CloseMainChatSetupSteps for steps the user
// changed their mind about in onboarding (Notifications set to Off, then back
// to another level), so the main chat asks them after all. Only answers
// onboarding gave are reopened.
func (a *App) ReopenMainChatSetupSteps(steps []string) error {
	if err := chatFeatureErr(); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	mainChatSetupMu.Lock()
	defer mainChatSetupMu.Unlock()
	stored, err := a.loadMainChatSetup(ctx)
	if err != nil {
		return err
	}
	fromOnboarding := a.loadOnboardingSetupAnswers(ctx)
	changed := false
	for _, step := range steps {
		if fromOnboarding[step] == "" {
			continue
		}
		delete(stored.Answers, step)
		delete(fromOnboarding, step)
		changed = true
	}
	if !changed {
		return nil
	}
	a.saveOnboardingSetupAnswers(ctx, fromOnboarding)
	a.saveMainChatSetup(ctx, stored)
	return nil
}
