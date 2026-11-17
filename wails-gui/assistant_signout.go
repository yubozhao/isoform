package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"yansu-agent/agent"
)

// R75: when the chosen assistant is signed out, Isoform says so. A first
// "signed out" reading is never enough: it waits for one re-check, and only
// a second reading still signed out (at least assistantSignOutRecheckAfter
// later) confirms it. While confirmed, a follow-up check every
// assistantSignOutPollEvery notices the sign-in, and the state clears at once.

const (
	assistantSignOutRecheckAfter = 20 * time.Second
	assistantSignOutPollEvery    = 30 * time.Second
	// assistantSignOutChangedEvent tells Today, Chat and Settings to read the
	// state again.
	assistantSignOutChangedEvent = "assistant-signout:changed"
	// assistantSignOutClickData opens Today when the notification is tapped.
	assistantSignOutClickData = "today:assistant-signed-out"
)

// signInVerdict is what one probe says about an assistant's sign-in.
type signInVerdict int

const (
	// signInUnknown: the probe failed or the CLI is missing. It says nothing
	// about sign-in, so it never starts or clears a sign-out.
	signInUnknown signInVerdict = iota
	signInOK
	signInOut
)

// signOutStep is what one reading changed.
type signOutStep int

const (
	signOutNone signOutStep = iota
	// signOutRecheck: first sighting; schedule the one re-check.
	signOutRecheck
	// signOutConfirmed: the re-check still says signed out. Show it, notify once.
	signOutConfirmed
	// signOutCleared: a reading says signed in again after a confirmed sign-out.
	signOutCleared
)

// signOutTracker holds the debounce state per assistant. Its zero value is ready.
type signOutTracker struct {
	mu        sync.Mutex
	pending   map[string]time.Time
	confirmed map[string]time.Time
}

// observe applies one reading and returns what it changed. For
// signOutConfirmed and signOutCleared the time is when the sign-out began.
func (t *signOutTracker) observe(assistant string, verdict signInVerdict, now time.Time) (signOutStep, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = make(map[string]time.Time)
		t.confirmed = make(map[string]time.Time)
	}
	switch verdict {
	case signInOK:
		delete(t.pending, assistant)
		if since, ok := t.confirmed[assistant]; ok {
			delete(t.confirmed, assistant)
			return signOutCleared, since
		}
		return signOutNone, time.Time{}
	case signInOut:
		if since, ok := t.confirmed[assistant]; ok {
			return signOutNone, since
		}
		first, ok := t.pending[assistant]
		if !ok {
			t.pending[assistant] = now
			return signOutRecheck, time.Time{}
		}
		if now.Sub(first) < assistantSignOutRecheckAfter {
			return signOutNone, time.Time{}
		}
		delete(t.pending, assistant)
		t.confirmed[assistant] = now
		return signOutConfirmed, now
	default:
		return signOutNone, time.Time{}
	}
}

// isConfirmed reports whether the assistant is confirmed signed out.
func (t *signOutTracker) isConfirmed(assistant string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.confirmed[assistant]
	return ok
}

// confirmedSince is when the assistant's confirmed sign-out began, or zero.
func (t *signOutTracker) confirmedSince(assistant string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.confirmed[assistant]
}

// firstConfirmed returns the first confirmed assistant in order.
func (t *signOutTracker) firstConfirmed(order []string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, assistant := range order {
		if _, ok := t.confirmed[assistant]; ok {
			return assistant, true
		}
	}
	return "", false
}

// assistantSignInVerdict maps one probe state to a verdict. Only the probe's
// own "signed out" reason counts as signed out; a failed probe is unknown.
func assistantSignInVerdict(state agent.AssistantState) signInVerdict {
	switch {
	case state.SignedIn:
		return signInOK
	case state.Available && state.Reason == "signed out":
		return signInOut
	default:
		return signInUnknown
	}
}

// assistantSignOutDisplayName is the assistant's product name, used in copy.
func assistantSignOutDisplayName(assistant string) string {
	if assistant == string(agent.BackendCodex) {
		return "Codex"
	}
	return "Claude Code"
}

// assistantSignOutCopy is the native notification copy. English is the source;
// zh-CN follows it.
var assistantSignOutCopy = map[string]map[string]string{
	"en":    {"title": "Isoform", "body": "%s is signed out. Briefs are shorter until you sign in."},
	"zh-CN": {"title": "Isoform", "body": "%s 已退出登录。登录之前，简报会更简短。"},
}

// observeAssistantSignIn applies the probe's readings for the chosen
// assistants, and schedules the follow-up probe the tracker asks for.
func (a *App) observeAssistantSignIn(states map[string]agent.AssistantState) {
	now := time.Now()
	for _, assistant := range []string{string(agent.BackendClaudeCode), string(agent.BackendCodex)} {
		state, ok := states[assistant]
		if !ok {
			continue
		}
		step, since := a.signOut.observe(assistant, assistantSignInVerdict(state), now)
		switch step {
		case signOutRecheck:
			a.scheduleAssistantSignOutCheck(assistant, assistantSignOutRecheckAfter)
		case signOutConfirmed:
			a.scheduleAssistantSignOutCheck(assistant, assistantSignOutPollEvery)
			a.emitBrowserEvent(assistantSignOutChangedEvent)
			a.recordAssistantSignOut("assistant.signed_out", assistant, 0)
			a.notifyAssistantSignedOut(assistant)
		case signOutCleared:
			a.emitBrowserEvent(assistantSignOutChangedEvent)
			a.recordAssistantSignOut("assistant.signed_out_fixed", assistant, now.Sub(since).Seconds())
		case signOutNone:
			if a.signOut.isConfirmed(assistant) {
				a.scheduleAssistantSignOutCheck(assistant, assistantSignOutPollEvery)
			}
		}
	}
}

// scheduleAssistantSignOutCheck runs one follow-up probe for the assistant,
// replacing any check already waiting for it.
func (a *App) scheduleAssistantSignOutCheck(assistant string, after time.Duration) {
	a.signOutTimersMu.Lock()
	defer a.signOutTimersMu.Unlock()
	if a.signOutTimers == nil {
		a.signOutTimers = make(map[string]*time.Timer)
	}
	if timer := a.signOutTimers[assistant]; timer != nil {
		timer.Stop()
	}
	a.signOutTimers[assistant] = time.AfterFunc(after, func() { a.recheckAssistantSignIn(assistant) })
}

// recheckAssistantSignIn reads sign-in again through the shared probe cache.
// The probe reports back through observeAssistantSignIn.
func (a *App) recheckAssistantSignIn(assistant string) {
	a.mu.Lock()
	cache := a.assistantProbes
	a.mu.Unlock()
	if cache == nil {
		return
	}
	cache.invalidate()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cache.snapshot(ctx)
}

// notifyAssistantSignedOut sends the one notification for this sign-out.
func (a *App) notifyAssistantSignedOut(assistant string) {
	copy := assistantSignOutCopy[normalizeUILanguage(a.currentUILanguage())]
	if copy == nil {
		copy = assistantSignOutCopy["en"]
	}
	body := fmt.Sprintf(copy["body"], assistantSignOutDisplayName(assistant))
	a.NotifyUser(copy["title"], body, assistantSignOutClickData)
}

// recordAssistantSignOut sends the two R75 events. The properties are the
// assistant's id and, when fixed, the seconds it took; no personal data.
func (a *App) recordAssistantSignOut(event, assistant string, seconds float64) {
	if a.telemetry == nil {
		return
	}
	props := map[string]any{"assistant": assistant}
	if event == "assistant.signed_out_fixed" {
		props["seconds"] = int(seconds)
	}
	a.telemetry.Capture(event, props)
}

// GetAssistantSignOut returns the first confirmed signed-out assistant in the
// user's order, or nothing. Today and Chat read it.
func (a *App) GetAssistantSignOut() AssistantSignOutView {
	cfg := loadAIProviderConfig()
	assistant, ok := a.signOut.firstConfirmed(routeOrder(cfg))
	if !ok {
		return AssistantSignOutView{}
	}
	return AssistantSignOutView{Assistant: assistant, SignedOut: true}
}

// AssistantSignOutView names the assistant whose sign-in is confirmed gone.
type AssistantSignOutView struct {
	Assistant string `json:"assistant"`
	SignedOut bool   `json:"signedOut"`
}

// mobileTodayAssistantSignOut is the phone's optional Today field (R75): the
// id of the chosen assistant whose sign-in is confirmed gone on the computer.
// The phone words the line itself and has no button.
type mobileTodayAssistantSignOut struct {
	Assistant string `json:"assistant"`
}

// mobileTodaySignOut returns the first confirmed signed-out assistant in the
// given order, or nil. A nil app reports nothing.
func (a *App) mobileTodaySignOut(order []string) *mobileTodayAssistantSignOut {
	if a == nil {
		return nil
	}
	assistant, ok := a.signOut.firstConfirmed(order)
	if !ok {
		return nil
	}
	return &mobileTodayAssistantSignOut{Assistant: assistant}
}
