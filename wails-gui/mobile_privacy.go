package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// What Isoform sees, by job (Phone Threads §5): one row per kind of source,
// on or off, from the Mac's own permission switches and macOS grants. The phone
// words every row from its catalog; the Mac sends only codes.

const (
	mobileSeesMail     = "mail"
	mobileSeesCalendar = "calendar"
	mobileSeesCalls    = "calls"
	mobileSeesScreen   = "screen"
	mobileSeesAI       = "assistant"
	mobileSeesContacts = "contacts"
)

type MobileSeesSource struct {
	Job string `json:"job"`
	On  bool   `json:"on"`
}

type MobileSeesView struct {
	Sources []MobileSeesSource `json:"sources"`
	// Assistant is the person's own AI the text a task needs goes to:
	// claude_code, codex, or empty for Isoform's built-in model.
	Assistant string `json:"assistant,omitempty"`
	// PhonePaired is true while a phone is paired; the computer's sheet
	// names the phone line only then (shell-review v1.0.0 B2).
	PhonePaired bool `json:"phonePaired,omitempty"`
}

type mobileSeesSource = MobileSeesSource
type mobileSeesView = MobileSeesView

// mobileSeesFrom maps the Mac's permission rows to jobs. A job is on only when
// its switch is on and macOS granted what it needs.
func mobileSeesFrom(perms PlatformPermissions, provider string) mobileSeesView {
	on := map[string]bool{}
	for _, item := range perms.Items {
		on[item.Key] = item.Granted && (!item.Toggleable || item.Enabled)
	}
	view := mobileSeesView{Assistant: mobileTaskRunner(provider)}
	view.Sources = []mobileSeesSource{
		{Job: mobileSeesMail, On: on[mailPermissionKey]},
		{Job: mobileSeesCalendar, On: on[calendarPermissionKey] || on[remindersPermissionKey]},
		{Job: mobileSeesCalls, On: on["microphone"]},
		{Job: mobileSeesScreen, On: on["screen_recording"]},
	}
	if view.Assistant != "" {
		view.Sources = append(view.Sources, mobileSeesSource{Job: mobileSeesAI, On: true})
	}
	view.Sources = append(view.Sources, mobileSeesSource{Job: mobileSeesContacts, On: on["contacts"]})
	return view
}

func (bridge *mobileBridge) serveSees(w http.ResponseWriter, r *http.Request) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	view := struct {
		MobileSeesView
		AgentOfYouReviewOff bool `json:"agentOfYouReviewOff,omitempty"`
	}{MobileSeesView: bridge.app.GetWhatIsoformSees()}
	if env, err := bridge.app.agentOwnerEnv(); err == nil {
		view.AgentOfYouReviewOff = mobileOwnerReviewOff(r.Context(), env)
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func mobileOwnerReviewOff(ctx context.Context, env agentOwnerEnv) bool {
	// A privacy read must not create an owner identity.
	data, err := os.ReadFile(env.reservation.stateFile)
	var state yourAgentStateFile
	if err != nil || json.Unmarshal(data, &state) != nil || state.Hub == "" || state.Token == "" {
		return false
	}
	status, err := env.client.do(ctx, http.MethodGet, "/v1/owner", nil, state.Hub, state.Token)
	return err == nil && mobileOwnerStatusReviewOff(status)
}

func mobileOwnerStatusReviewOff(status json.RawMessage) bool {
	var owner struct {
		State    string `json:"state"`
		Settings struct {
			ReviewResults *bool `json:"reviewResults"`
		} `json:"settings"`
	}
	if json.Unmarshal(status, &owner) != nil {
		return false
	}
	return (owner.State == "on" || owner.State == "paused") && owner.Settings.ReviewResults != nil && !*owner.Settings.ReviewResults
}

// GetWhatIsoformSees uses the same job projection on the desktop and phone.
func (a *App) GetWhatIsoformSees() MobileSeesView {
	view := mobileSeesFrom(a.GetPlatformPermissions(), loadAIProviderConfig().Provider)
	if bridge := a.mobileBridgeForUse(); bridge != nil {
		view.PhonePaired = bridge.phonePaired(time.Now())
	}
	return view
}

// phonePaired reports whether a phone's pairing is still current.
func (bridge *mobileBridge) phonePaired(now time.Time) bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	for _, record := range bridge.devices {
		if expires, ok := mobileParseTimestamp(record.ExpiresAt); ok && now.Before(expires) {
			return true
		}
	}
	return false
}

// mobileSeesNotice is what the Mac shows when the phone asks to turn a source
// on: the computer is where the switch and the macOS prompt live.
func mobileSeesNotice(job, lang string) (string, string, bool) {
	zh := lang == "zh-CN"
	names := map[string][2]string{
		mobileSeesMail:     {"Mail", "邮件"},
		mobileSeesCalendar: {"Calendar and Reminders", "日历和提醒事项"},
		mobileSeesCalls:    {"calls and meetings", "通话和会议"},
		mobileSeesScreen:   {"your screen", "你的屏幕"},
		mobileSeesContacts: {"Contacts", "通讯录"},
	}
	name, ok := names[job]
	if !ok {
		return "", "", false
	}
	if zh {
		return "为 Isoform 打开" + name[1], "你在手机上点了“打开”。在 设置 › 观察 里打开。", true
	}
	return "Turn on " + name[0] + " for Isoform", "You asked from your phone. Turn it on in Settings › What Isoform sees.", true
}

func (bridge *mobileBridge) serveSeesShow(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Job            string `json:"job"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	title, body, ok := mobileSeesNotice(strings.TrimSpace(input.Job), bridge.app.currentUILanguage())
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "unknown source")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if mobileSeesShowHook != nil {
			mobileSeesShowHook(title, body)
		} else {
			bridge.app.NotifyUser(title, body, "settings:activity")
		}
		mobileJSON(w, http.StatusOK, map[string]bool{"shown": true}, "")
	})
}

// mobileSeesShowHook replaces the desktop notice in tests.
var mobileSeesShowHook func(title, body string)
