package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/remoteaccess"
)

// The Settings landing page ("How Isoform works for you", RC 1 A7) reads one
// aggregate view: a structured section per area, then up to three "Worth a
// look" issues. Every value comes from the feature that owns it — engagement
// settings, the phone connection, magpie quotas, the sensitive-apps list,
// storage, the brief schedule and the Self Dream queue — and the page writes
// the sentences (en and zh-CN) around these fields; nothing here is copy.

const (
	// settingsOverviewMaxIssues caps "Worth a look" so the page never nags.
	settingsOverviewMaxIssues = 3
	// Two missed poll intervals make the cached quota unknown.
	overviewQuotaMaxAge = 2 * magpieQuotaPollInterval
	// overviewNoisyKindMinSent is how often a kind must speak up in the
	// 30-day stats window with zero opens or actions before it qualifies.
	overviewNoisyKindMinSent = 20
	// settingsOverviewDismissalsKey is the ui_state key for "Leave it"
	// choices: issue id -> the signature that was dismissed.
	settingsOverviewDismissalsKey = "settings_overview_dismissals"

	// overviewIssueNoisyKind marks a kind that spoke up at least
	// overviewNoisyKindMinSent times in 30 days without one open or action.
	// Its fix quiets the kind on Today: SetEngagementKindQuiet, added by a
	// sibling PR in flight — this read only reports the issue.
	overviewIssueNoisyKind = "noisy_kind"
	// overviewIssuePhoneSameNetwork marks the relay_loopback warning: a
	// paired physical phone can reach the computer only on the same network.
	overviewIssuePhoneSameNetwork       = "phone_same_network_only"
	overviewIssuePhoneRemoteUnavailable = "phone_remote_access_unavailable"
	// overviewIssueAssistantOut marks an assistant in the routing order whose
	// current quota window has 0 percent left.
	overviewIssueAssistantOut = "assistant_out_of_usage"
)

// SettingsOverviewView is the aggregate read for the Settings landing page.
type SettingsOverviewView struct {
	// Unavailable names only the failed sections (speaksUp, remembers, keeps,
	// briefs, thinksWith). Their zero values must not be presented as successful reads.
	Unavailable []string                   `json:"unavailable"`
	Sees        SettingsOverviewSees       `json:"sees"`
	SpeaksUp    SettingsOverviewSpeaksUp   `json:"speaksUp"`
	ThinksWith  SettingsOverviewThinksWith `json:"thinksWith"`
	Remembers   SettingsOverviewRemembers  `json:"remembers"`
	Keeps       SettingsOverviewKeeps      `json:"keeps"`
	Briefs      SettingsOverviewBriefs     `json:"briefs"`
	Ignores     SettingsOverviewIgnores    `json:"ignores"`
	WorthALook  []SettingsOverviewIssue    `json:"worthALook"`
}

// SettingsOverviewSees is the observation summary. PausedUntil is the timed
// pause's auto-resume deadline (RFC 3339, UTC) and stays nil while watching or
// while paused by hand. PasswordManagersHidden is always true: password
// managers are protected by default and never named on the page.
type SettingsOverviewSees struct {
	Watching               bool    `json:"watching"`
	PausedUntil            *string `json:"pausedUntil"`
	IgnoredAppsCount       int     `json:"ignoredAppsCount"`
	PasswordManagersHidden bool    `json:"passwordManagersHidden"`
}

// SettingsOverviewSpeaksUp is the notification summary: the level dial, the
// daily cap, the quiet hours (HH:MM) and the channels a delivery can reach
// right now (desktop, push, text, email), in display order. Zero values mean
// the engagement service is not up yet.
type SettingsOverviewSpeaksUp struct {
	Level      string   `json:"level"`
	DailyCap   int      `json:"dailyCap"`
	QuietStart string   `json:"quietStart"`
	QuietEnd   string   `json:"quietEnd"`
	Channels   []string `json:"channels"`
}

// SettingsOverviewThinksWith lists the assistants Isoform thinks with, in the
// routing order the model groups admit (primary first).
type SettingsOverviewThinksWith struct {
	Order []SettingsOverviewAssistant `json:"order"`
}

// SettingsOverviewAssistant is one assistant in the routing order. PercentLeft
// is the percent of the window's allowance left (0-100, magpie's remaining;
// see QuotaWindow) for the window that binds first — the lowest remaining,
// ties broken by the earlier reset — and nil when magpie reports no numbers
// for the account. WindowLabel and ResetsAt name that window.
type SettingsOverviewAssistant struct {
	Assistant   string   `json:"assistant"`
	PercentLeft *float64 `json:"percentLeft"`
	WindowLabel string   `json:"windowLabel"`
	ResetsAt    string   `json:"resetsAt"`
}

// SettingsOverviewRemembers counts the learned things waiting for the user's
// confirmation (the Self Dream review queue).
type SettingsOverviewRemembers struct {
	AwaitingConfirmCount int `json:"awaitingConfirmCount"`
}

// SettingsOverviewKeeps is the storage summary: the retention in days (0 is
// unlimited), the cap and the current use, both in bytes.
type SettingsOverviewKeeps struct {
	RetentionDays int   `json:"retention"`
	LimitBytes    int64 `json:"limitBytes"`
	UsedBytes     int64 `json:"usedBytes"`
}

// SettingsOverviewBriefs is the brief schedule (HH:MM) with adaptive timing,
// and the paired phone the briefs reach — nil when no phone is paired. The
// times are empty while the brief service is not up yet.
type SettingsOverviewBriefs struct {
	MorningAt  string                      `json:"morningAt"`
	EveningAt  string                      `json:"eveningAt"`
	Adaptive   bool                        `json:"adaptive"`
	Phone      *SettingsOverviewPhone      `json:"phone"`
	Connection *SettingsOverviewConnection `json:"connection"`
}

// SettingsOverviewConnection contains only the phone status fields needed by Overview.
type SettingsOverviewConnection struct {
	Enabled       bool                    `json:"enabled"`
	RelayURL      string                  `json:"relayUrl"`
	RelayState    string                  `json:"relayState"`
	RelayLastSeen string                  `json:"relayLastSeenAt"`
	WarningCode   string                  `json:"warningCode,omitempty"`
	RemoteAccess  *MobileRemoteAccessView `json:"remoteAccess,omitempty"`
}

// SettingsOverviewPhone is the paired phone seen most recently.
type SettingsOverviewPhone struct {
	Name       string `json:"name"`
	LastSeenAt string `json:"lastSeenAt"`
}

// SettingsOverviewIgnores lists the apps Isoform never opens, except password
// managers (Sees.PasswordManagersHidden covers those).
type SettingsOverviewIgnores struct {
	Apps []SettingsOverviewIgnoredApp `json:"apps"`
}

// SettingsOverviewIgnoredApp is one ignored app. Name is the display label the
// user picked; bundleId is the matched bundle prefix.
type SettingsOverviewIgnoredApp struct {
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
}

// SettingsOverviewIssue is one "Worth a look" row: a kind the UI translates,
// the values its sentence interpolates, and the signature a "Leave it"
// remembers — the row returns only when the signature changes (the sent count
// doubles, a different assistant runs out, the relay address changes).
type SettingsOverviewIssue struct {
	ID        string                      `json:"id"`
	Kind      string                      `json:"kind"`
	Params    SettingsOverviewIssueParams `json:"params"`
	Signature string                      `json:"signature"`
}

// SettingsOverviewIssueParams carries only the fields for the issue's kind.
type SettingsOverviewIssueParams struct {
	Kind            string `json:"kind,omitempty"`
	Sent            int    `json:"sent,omitempty"`
	Assistant       string `json:"assistant,omitempty"`
	ResetsAt        string `json:"resetsAt,omitempty"`
	Host            string `json:"host,omitempty"`
	RemoteAccessOff string `json:"remoteAccessOff,omitempty"`
}

// settingsOverviewDismissal is the persisted "Leave it" of one issue.
type settingsOverviewDismissal struct {
	Signature   string `json:"signature"`
	DismissedAt int64  `json:"dismissedAt"` // Unix milliseconds
}

// settingsOverviewDismissalsMu serializes the read-modify-write a "Leave it"
// does on the ui_state map, so two clicks never clobber each other.
var settingsOverviewDismissalsMu sync.Mutex

// overviewSources are the shared reads the overview and its issues derive
// from, gathered once per call. engagement is nil when the service or its
// stats are unavailable.
type overviewSources struct {
	engagement  *EngagementSettingsView
	channels    []string
	models      ModelAccountsStatus
	mobile      MobileConnectionStatus
	unavailable []string
	now         time.Time
}

// GetSettingsOverview reads the Settings landing page. It never fails the page
// on one section's error: a source that cannot be read leaves its zero value.
func (a *App) GetSettingsOverview() (perfValue SettingsOverviewView, perfErr error) {
	defer logPageBinding("GetSettingsOverview", time.Now(), &perfValue)
	out := SettingsOverviewView{
		Unavailable: []string{},
		Sees:        SettingsOverviewSees{PasswordManagersHidden: true},
		SpeaksUp:    SettingsOverviewSpeaksUp{Channels: []string{}},
		ThinksWith:  SettingsOverviewThinksWith{Order: []SettingsOverviewAssistant{}},
		Ignores:     SettingsOverviewIgnores{Apps: []SettingsOverviewIgnoredApp{}},
		WorthALook:  []SettingsOverviewIssue{},
	}
	if a == nil {
		return out, nil
	}
	src := a.readOverviewSources()
	out.Ignores = overviewIgnoresFrom(a.GetSensitiveApps())
	out.Sees = a.settingsOverviewSees(len(out.Ignores.Apps))
	out.SpeaksUp = overviewSpeaksUpFrom(src)
	var quotaUnavailable bool
	out.ThinksWith, quotaUnavailable = overviewThinksWithAt(src.models, src.now)
	if quotaUnavailable {
		src.unavailable = append(src.unavailable, "thinksWith")
	}
	if count, err := a.settingsOverviewAwaitingConfirmCount(); err == nil {
		out.Remembers.AwaitingConfirmCount = count
	} else {
		src.unavailable = append(src.unavailable, "remembers")
	}
	var err error
	out.Keeps, err = a.settingsOverviewKeeps()
	if err != nil {
		src.unavailable = append(src.unavailable, "keeps")
	}
	out.Briefs, err = a.settingsOverviewBriefs(src.mobile)
	if err != nil {
		src.unavailable = append(src.unavailable, "briefs")
	}
	out.WorthALook = a.visibleOverviewIssues(computeOverviewIssues(src))
	out.Unavailable = append(out.Unavailable, src.unavailable...)
	return out, nil
}

// readOverviewSources gathers the shared reads once. The engagement stats and
// the phone status are cached or local reads; nothing here blocks on the
// network or the model gateway.
func (a *App) readOverviewSources() overviewSources {
	src := overviewSources{channels: []string{}, now: time.Now().UTC()}
	if svc := a.engagementService(); svc != nil {
		if view, err := engagementSettingsView(context.Background(), svc); err != nil {
			log.Printf("[settings-overview] engagement settings: %v", err)
			src.unavailable = append(src.unavailable, "speaksUp")
		} else {
			src.engagement = &view
		}
		if svc.presence != nil {
			// The channels the router can reach now, computed the way a
			// moment that allows every channel would compute them.
			moment := engagement.Moment{Channels: engagement.ChoosableChannels()}
			for _, channel := range engagement.RequestedChannels(moment, svc.presence(svc.now())) {
				src.channels = append(src.channels, string(channel))
			}
		}
	} else {
		src.unavailable = append(src.unavailable, "speaksUp")
	}
	snapshot := a.magpieModels.snapshot()
	src.models = a.modelAccountsStatusFromSnapshot(snapshot)
	if !snapshot.PollAt.IsZero() && src.now.Sub(snapshot.PollAt) > overviewQuotaMaxAge {
		src.models.Error = "overview_quota_stale"
	}
	if a.magpie != nil && !a.magpie.Healthy() {
		src.models.Error = "overview_quota_unavailable"
	}
	if status, err := a.GetMobileConnectionStatus(); err == nil {
		src.mobile = status
	} else {
		src.unavailable = append(src.unavailable, "briefs")
	}
	return src
}

// settingsOverviewSees reads the observation state and the ignored-apps count.
func (a *App) settingsOverviewSees(ignoredAppsCount int) SettingsOverviewSees {
	pause := a.observationPauseSnapshot()
	sees := SettingsOverviewSees{
		Watching:               !pause.enabled,
		IgnoredAppsCount:       ignoredAppsCount,
		PasswordManagersHidden: true,
	}
	if !pause.resumeAt.IsZero() {
		formatted := pause.resumeAt.UTC().Format(time.RFC3339Nano)
		sees.PausedUntil = &formatted
	}
	return sees
}

// overviewSpeaksUpFrom projects the engagement settings into the summary. The
// channels stay empty while the engagement service is not up.
func overviewSpeaksUpFrom(src overviewSources) SettingsOverviewSpeaksUp {
	out := SettingsOverviewSpeaksUp{Channels: src.channels}
	if out.Channels == nil {
		out.Channels = []string{}
	}
	if src.engagement != nil {
		out.Level = src.engagement.Level
		out.DailyCap = src.engagement.DailyInterruptBudget
		out.QuietStart = src.engagement.QuietStart
		out.QuietEnd = src.engagement.QuietEnd
	}
	return out
}

// overviewThinksWithFrom maps the managed account order onto the cached quota
// windows. An assistant without an account row, or one whose account reports
// no windows, gets a nil PercentLeft: unknown, not zero.
func overviewThinksWithFrom(models ModelAccountsStatus) SettingsOverviewThinksWith {
	out, _ := overviewThinksWithAt(models, time.Now().UTC())
	return out
}

func overviewThinksWithAt(models ModelAccountsStatus, now time.Time) (SettingsOverviewThinksWith, bool) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	unavailable := models.Error != ""
	out := SettingsOverviewThinksWith{Order: []SettingsOverviewAssistant{}}
	for _, assistant := range models.AccountOrder {
		entry := SettingsOverviewAssistant{Assistant: assistant}
		if account, ok := overviewAccountFor(models.Accounts, assistant); ok && models.Error == "" {
			if account.Error != "" {
				unavailable = true
				out.Order = append(out.Order, entry)
				continue
			}
			window, known := overviewBindingWindowAt(account.Windows, now)
			if !known {
				for _, window := range account.Windows {
					unavailable = unavailable || !window.Unlimited
				}
				out.Order = append(out.Order, entry)
				continue
			}
			left := window.Remaining
			entry.PercentLeft = &left
			entry.WindowLabel = window.Name
			entry.ResetsAt = window.ResetsAt
		}
		out.Order = append(out.Order, entry)
	}
	return out, unavailable
}

// overviewAccountFor finds the first account row for a provider id.
func overviewAccountFor(accounts []ModelAccount, provider string) (ModelAccount, bool) {
	for _, account := range accounts {
		if account.Provider == provider {
			return account, true
		}
	}
	return ModelAccount{}, false
}

func overviewBindingWindowAt(windows []QuotaWindow, now time.Time) (QuotaWindow, bool) {
	var best QuotaWindow
	known := false
	for _, window := range windows {
		if window.Unlimited || math.IsNaN(window.Remaining) || math.IsInf(window.Remaining, 0) || window.Remaining < 0 || window.Remaining > 100 {
			continue
		}
		reset, resetErr := time.Parse(time.RFC3339Nano, window.ResetsAt)
		if window.ResetsAt != "" && (resetErr != nil || !reset.After(now)) {
			continue
		}
		bestReset, bestResetErr := time.Parse(time.RFC3339Nano, best.ResetsAt)
		earlier := resetErr == nil && (bestResetErr != nil || reset.Before(bestReset))
		if !known || window.Remaining < best.Remaining || (window.Remaining == best.Remaining && earlier) {
			best, known = window, true
		}
	}
	return best, known
}

// settingsOverviewAwaitingConfirmCount uses the product review queue's exact
// deduplication and decision rules without ListSelfDreamUpdates' 50-row UI cap.
func (a *App) settingsOverviewAwaitingConfirmCount() (int, error) {
	if a.activity == nil || a.activity.store == nil {
		return 0, errors.New("overview_activity_store_unavailable")
	}
	queue, err := a.activity.store.loadSelfDreamReviewQueue()
	if err != nil {
		return 0, err
	}
	keys := map[string]bool{}
	for _, op := range queue.waiting {
		if _, open := queue.open(op); open {
			keys[selfDreamProposalKey(op)] = true
		}
	}
	return len(keys), nil
}

// settingsOverviewKeeps reads the retention days, the storage cap and the
// current use from the activity settings.
func (a *App) settingsOverviewKeeps() (SettingsOverviewKeeps, error) {
	days := a.GetActivityRetentionDays()
	gb := a.GetActivityMaxStorageGB()
	stats, err := a.GetActivityStorageStats()
	if err != nil {
		log.Printf("[settings-overview] storage stats: %v", err)
	}
	return SettingsOverviewKeeps{
		RetentionDays: days,
		LimitBytes:    int64(gb) * 1024 * 1024 * 1024,
		UsedBytes:     stats.RetainedBytes,
	}, err
}

// settingsOverviewBriefs reads the brief schedule and the paired phone seen
// most recently (the phone field of the connection status is shared with the
// phone_same_network_only issue through src.mobile).
func (a *App) settingsOverviewBriefs(status MobileConnectionStatus) (SettingsOverviewBriefs, error) {
	out := SettingsOverviewBriefs{Connection: &SettingsOverviewConnection{
		Enabled: status.Enabled, RelayURL: status.RelayURL, RelayState: status.RelayState,
		RelayLastSeen: status.RelayLastSeen, WarningCode: status.WarningCode, RemoteAccess: status.RemoteAccess,
	}}
	schedule, scheduleErr := a.GetMobileBriefSchedule()
	if scheduleErr == nil {
		out.MorningAt = schedule.Morning
		out.EveningAt = schedule.Evening
		if schedule.AdaptiveTiming != nil {
			out.Adaptive = *schedule.AdaptiveTiming
		}
	}
	best := -1
	for i, device := range status.Devices {
		if best == -1 || device.LastSeenAt > status.Devices[best].LastSeenAt {
			best = i
		}
	}
	if best >= 0 {
		device := status.Devices[best]
		out.Phone = &SettingsOverviewPhone{Name: device.DeviceName, LastSeenAt: device.LastSeenAt}
	}
	return out, scheduleErr
}

// overviewIgnoresFrom lists the ignored apps the page may name. Password
// managers are the whole default list and stay hidden; user entries carry
// their display name as the category label (see pii.userSensitiveApps).
func overviewIgnoresFrom(entries []pii.SensitiveAppEntry) SettingsOverviewIgnores {
	out := SettingsOverviewIgnores{Apps: []SettingsOverviewIgnoredApp{}}
	for _, entry := range entries {
		if entry.Category == "password_manager" {
			continue
		}
		name := entry.Category
		if entry.IsDefault {
			// No default outside the password managers today; a future one
			// would fall back to its bundle prefix rather than a slug.
			name = entry.BundlePrefix
		}
		out.Apps = append(out.Apps, SettingsOverviewIgnoredApp{Name: name, BundleID: entry.BundlePrefix})
	}
	return out
}

// computeOverviewIssues derives the "Worth a look" set, before dismissal
// filtering: assistants out of usage first, then the phone warning, then the
// noisy kinds, so the cap keeps the most actionable rows.
func computeOverviewIssues(src overviewSources) []SettingsOverviewIssue {
	issues := []SettingsOverviewIssue{}
	thinksWith, _ := overviewThinksWithAt(src.models, src.now)
	for _, entry := range thinksWith.Order {
		if entry.PercentLeft == nil || *entry.PercentLeft > 0 {
			continue
		}
		issues = append(issues, SettingsOverviewIssue{
			ID:     overviewIssueAssistantOut + ":" + entry.Assistant,
			Kind:   overviewIssueAssistantOut,
			Params: SettingsOverviewIssueParams{Assistant: entry.Assistant, ResetsAt: entry.ResetsAt},
			// The same assistant earns a new look when the window renews and
			// runs out again (a new resetsAt); a different assistant is a new id.
			Signature: entry.Assistant + "|" + entry.ResetsAt,
		})
	}
	if src.mobile.WarningCode == "relay_loopback" {
		issues = append(issues, SettingsOverviewIssue{
			ID:   overviewIssuePhoneSameNetwork,
			Kind: overviewIssuePhoneSameNetwork,
			Params: SettingsOverviewIssueParams{
				Host: src.mobile.WarningHost, RemoteAccessOff: src.mobile.RemoteAccessOffReason,
			},
			// A dismissed warning returns when the relay address changes.
			Signature: src.mobile.WarningHost,
		})
	}
	if src.mobile.Enabled && src.mobile.RemoteAccess != nil &&
		src.mobile.RemoteAccess.State == remoteaccess.StateFailed && len(src.mobile.Devices) > 0 {
		issues = append(issues, SettingsOverviewIssue{
			ID: overviewIssuePhoneRemoteUnavailable, Kind: overviewIssuePhoneRemoteUnavailable,
			Signature: "failed",
		})
	}
	return append(issues, noisyKindIssues(src.engagement)...)
}

// noisyKindIssues reports the kinds that spoke up at least
// overviewNoisyKindMinSent times through interruption channels in the stats
// window without a single open or action. Quiet history never drives a hint.
// Most-sent first, kind id as the tie-break, so the cap is deterministic.
func noisyKindIssues(view *EngagementSettingsView) []SettingsOverviewIssue {
	if view == nil {
		return nil
	}
	issues := []SettingsOverviewIssue{}
	for _, kind := range view.Kinds {
		// Suggest quiet only where policy allows it and it has an effect.
		// The user can still choose quiet directly for non-suggestible kinds.
		if engagementKindSilent(engagement.Kind(kind.Kind)) || !kind.QuietSuggestionAllowed || kind.Quiet {
			continue
		}
		if kind.NotificationSent < overviewNoisyKindMinSent || kind.NotificationOpenedOrActed > 0 {
			continue
		}
		issues = append(issues, SettingsOverviewIssue{
			ID:     overviewIssueNoisyKind + ":" + kind.Kind,
			Kind:   overviewIssueNoisyKind,
			Params: SettingsOverviewIssueParams{Kind: kind.Kind, Sent: kind.NotificationSent},
			// A dismissal holds while the noise stays in the same doubling
			// bucket: 20-39 sent signs as "20", 40-79 as "40", and so on.
			Signature: fmt.Sprintf("%s|%d", kind.Kind, overviewNoisyBucket(kind.NotificationSent)),
		})
	}
	sort.Slice(issues, func(i, j int) bool {
		sentI, sentJ := issues[i].Params.Sent, issues[j].Params.Sent
		if sentI != sentJ {
			return sentI > sentJ
		}
		return issues[i].ID < issues[j].ID
	})
	return issues
}

// overviewNoisyBucket quantizes a sent count into its doubling bucket.
func overviewNoisyBucket(sent int) int {
	bucket := overviewNoisyKindMinSent
	for bucket <= sent/2 {
		bucket *= 2
	}
	return bucket
}

// visibleOverviewIssues drops the dismissed rows whose signature still holds
// and caps the rest.
func (a *App) visibleOverviewIssues(issues []SettingsOverviewIssue) []SettingsOverviewIssue {
	dismissed, err := a.loadOverviewDismissals()
	if err != nil {
		log.Printf("[settings-overview] read dismissals: %v", err)
	}
	out := make([]SettingsOverviewIssue, 0, len(issues))
	for _, issue := range issues {
		if remembered, ok := dismissed[issue.ID]; ok && remembered.Signature == issue.Signature {
			continue
		}
		out = append(out, issue)
		if len(out) == settingsOverviewMaxIssues {
			break
		}
	}
	return out
}

// loadOverviewDismissals reads the "Leave it" choices. An unreadable or
// corrupt value means nothing was dismissed, never a hidden issue.
func (a *App) loadOverviewDismissals() (map[string]settingsOverviewDismissal, error) {
	out := map[string]settingsOverviewDismissal{}
	if a == nil || a.store == nil {
		return out, errors.New("overview_store_unavailable")
	}
	raw, err := a.store.GetUIState(context.Background(), settingsOverviewDismissalsKey)
	if err != nil || raw == "" {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]settingsOverviewDismissal{}, err
	}
	if out == nil {
		out = map[string]settingsOverviewDismissal{}
	}
	return out, nil
}

// DismissOverviewIssue records "Leave it" for a current "Worth a look" row:
// the row's signature is stored and the row stays hidden until the signature
// changes. An id that names no current issue is an error, so a stale page
// cannot hide a row that no longer qualifies.
func (a *App) DismissOverviewIssue(id string) error {
	if a == nil || a.store == nil {
		return errors.New("overview_store_unavailable")
	}
	id = strings.TrimSpace(id)
	for _, issue := range computeOverviewIssues(a.readOverviewSources()) {
		if issue.ID != id {
			continue
		}
		settingsOverviewDismissalsMu.Lock()
		dismissed, err := a.loadOverviewDismissals()
		if err != nil {
			settingsOverviewDismissalsMu.Unlock()
			return err
		}
		dismissed[id] = settingsOverviewDismissal{Signature: issue.Signature, DismissedAt: time.Now().UnixMilli()}
		raw, err := json.Marshal(dismissed)
		if err != nil {
			settingsOverviewDismissalsMu.Unlock()
			return err
		}
		err = a.store.SetUIState(context.Background(), settingsOverviewDismissalsKey, string(raw))
		settingsOverviewDismissalsMu.Unlock()
		return err
	}
	return errors.New("overview_issue_not_current")
}
