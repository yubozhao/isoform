package main

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/engagement"
	"wails-gui/internal/proactivevoice"
)

const proactiveVoiceReceiptPrefix = "proactive_voice_provenance:"

// Receipts stay in local storage. They contain source identities and revision
// hashes, never a second copy of source text, the provider job or a draft address.
// The opaque reference in a stored card is removed at every delivery boundary.
// An old generated card without a receipt fails closed to catalog copy.
type proactiveVoiceReceipt struct {
	Version          int                        `json:"version"`
	Moment           engagement.Moment          `json:"moment"`
	AssembledAt      time.Time                  `json:"assembledAt"`
	Locale           string                     `json:"locale"`
	Dependencies     []proactiveVoiceDependency `json:"dependencies"`
	InputHash        string                     `json:"inputHash"`
	PresentationHash string                     `json:"presentationHash"`
	StatusHash       string                     `json:"statusHash"`
}

func proactiveVoiceHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return agencyPayloadHash(raw)
}

func proactiveVoiceInputHash(job proactivevoice.Job, addresses map[string]string) string {
	job.MomentID = "" // Each assembly mints an opaque provider-only identity.
	return proactiveVoiceHash(struct {
		Job       proactivevoice.Job
		Addresses map[string]string
	}{job, addresses})
}

func proactiveVoicePresentationHash(item mainChatProactive) string {
	// Not now is a local, catalog-owned presentation action. Hydrating it on
	// an RC 1 reply card must not invalidate that card's source-bound draft.
	// Its labels are overwritten from the catalog at every delivery; content,
	// draft, evidence and all other actions remain covered by the receipt.
	if item.Kind == string(engagement.KindWaitingOnReply) {
		ids := make([]string, 0, len(item.ActionIDs))
		for _, id := range item.ActionIDs {
			if id != "not_now" {
				ids = append(ids, id)
			}
		}
		item.ActionIDs = ids
		item.ActionLabels = maps.Clone(item.ActionLabels)
		delete(item.ActionLabels, "not_now")
		item.Localized = maps.Clone(item.Localized)
		for lang, copy := range item.Localized {
			copy.ActionLabels = maps.Clone(copy.ActionLabels)
			delete(copy.ActionLabels, "not_now")
			item.Localized[lang] = copy
		}
	}
	item.State, item.VoiceReceipt = "", "" // Resolution changes no source copy.
	item.SentAt = 0                        // Nor does the time a draft was sent.
	item.KindMuted = false                 // Current preferences are projected independently.
	item.ReadReceipt, item.sourceHash = "", ""
	return proactiveVoiceHash(item)
}

func proactiveVoiceStatusEntry(item mainChatProactive, key string) mainChatStatusEntry {
	localized := make(map[string]string, len(item.Localized))
	for lang, copy := range item.Localized {
		localized[lang] = copy.Body
		if item.VoiceVersion != "" && copy.Headline == item.Headline && copy.Body != copy.Headline {
			localized[lang] = copy.Headline + "\n" + copy.Body
		}
	}
	return mainChatStatusEntry{ID: key, MomentID: item.MomentID, Kind: item.Kind, Text: proactiveFallback(item), Localized: localized,
		Target: item.Target, VoiceVersion: item.VoiceVersion, VoiceReceipt: item.VoiceReceipt}
}

func proactiveVoiceStatusHash(entry mainChatStatusEntry) string {
	entry.VoiceReceipt = ""
	entry.ReadReceipt = ""
	entry.SentAt = 0 // Delivery bookkeeping does not change source copy.
	return proactiveVoiceHash(entry)
}

func (a *App) saveProactiveVoiceReceipt(ctx context.Context, m engagement.Moment, now time.Time, job proactivevoice.Job, addresses map[string]string, dependencies []proactiveVoiceDependency, item *mainChatProactive) bool {
	if a.store == nil || len(dependencies) == 0 {
		return false
	}
	// Only code-owned locators needed to reconstruct the source read survive.
	moment := engagement.Moment{Kind: m.Kind, SubjectID: m.SubjectID, DedupeKey: m.DedupeKey, CreatedAt: m.CreatedAt}
	receipt := proactiveVoiceReceipt{Version: 1, Moment: moment, AssembledAt: now, Locale: job.Locale,
		Dependencies: dependencies, InputHash: proactiveVoiceInputHash(job, addresses),
		PresentationHash: proactiveVoicePresentationHash(*item),
		StatusHash:       proactiveVoiceStatusHash(proactiveVoiceStatusEntry(*item, engagementReportKey(m, now)))}
	raw, err := json.Marshal(receipt)
	id := uuid.NewString()
	if err != nil || a.store.SetUIState(ctx, proactiveVoiceReceiptPrefix+id, string(raw)) != nil {
		return false
	}
	item.VoiceReceipt = id
	return true
}

func (a *App) proactiveVoiceReceiptAllowsRead(ctx context.Context, id, kind, presentationHash string, status bool) bool {
	if a == nil || a.store == nil || ctx.Err() != nil || !proactivevoice.Supported(kind) {
		return false
	}
	key := proactiveReadVerdictKey{id: id, kind: kind, presentation: presentationHash, status: status}
	return a.proactiveReadVerdict(ctx, key, func() bool {
		return a.proactiveVoiceReceiptAllowsReadLive(ctx, id, kind, presentationHash, status)
	})
}

// proactiveVoiceReceiptAllowsReadLive is the gate itself: it reloads the
// receipt's sources and checks them again.
func (a *App) proactiveVoiceReceiptAllowsReadLive(ctx context.Context, id, kind, presentationHash string, status bool) bool {
	if _, err := uuid.Parse(id); err != nil {
		return false
	}
	raw, err := a.store.GetUIState(ctx, proactiveVoiceReceiptPrefix+id)
	var receipt proactiveVoiceReceipt
	if err != nil || json.Unmarshal([]byte(raw), &receipt) != nil || receipt.Version != 1 || len(receipt.Dependencies) == 0 || string(receipt.Moment.Kind) != kind {
		return false
	}
	want := receipt.PresentationHash
	if status {
		want = receipt.StatusHash
	}
	if want == "" || want != presentationHash {
		return false
	}
	// Preserve the original evidence clock only for displayed dates/durations.
	// The adapters still recheck today's permissions, exclusions, source rows,
	// origin sessions, tombstones and revisions; no cached gate is consulted.
	source, reason := a.loadProactiveVoiceSource(ctx, receipt.Moment, receipt.AssembledAt, receipt.Locale)
	if reason != "" || !reflect.DeepEqual(receipt.Dependencies, source.Provenance) {
		return false
	}
	job, addresses, reason := assembleProactiveVoiceJob(kind, receipt.Locale, true, receipt.AssembledAt, source)
	return reason == "" && receipt.InputHash == proactiveVoiceInputHash(job, addresses)
}

func withdrawProactiveVoice(item *mainChatProactive, language string) {
	item.Draft, item.DraftContent = false, nil
	item.VoiceVersion, item.VoiceReceipt = "", ""
	item.Work, item.Permission = nil, nil
	item.ActionIDs = []string{}
	item.Localized = map[string]mainChatProactiveCopy{}
	for _, lang := range []string{"en", "zh-CN"} {
		catalog, _ := engagement.Copy(engagement.Kind(item.Kind), lang)
		labels := map[string]string{}
		for _, action := range engagement.ChatActions(engagement.Kind(item.Kind)) {
			if action.ID == "copy_draft" {
				continue
			}
			labels[action.ID] = catalog.Actions[action.ID]
			if lang == "en" {
				item.ActionIDs = append(item.ActionIDs, action.ID)
			}
		}
		item.Localized[lang] = mainChatProactiveCopy{ActionLabels: labels}
	}
	resetProactiveVoiceToGeneric(item, language)
	item.ActionLabels = item.Localized[language].ActionLabels
}

func sourceFreeProactiveTarget(kind string) mainChatTarget {
	if kind == string(engagement.KindCapturePaused) {
		target, _ := engagementChatTarget(engagement.Moment{Kind: engagement.KindCapturePaused})
		return target
	}
	return mainChatTarget{Kind: mainChatTargetToday}
}

func (a *App) projectProactiveVoiceItem(ctx context.Context, item *mainChatProactive, lang string) bool {
	// Older person sightings have no supported open question. Hide them on
	// replay rather than presenting a generic first-week or sighting card.
	if item.Kind == string(engagement.KindPersonReappeared) {
		*item = mainChatProactive{MomentID: item.MomentID, Kind: "thread_update_unavailable", Target: mainChatTarget{Kind: mainChatTargetNone}}
		return true
	}
	// First-week cards are Isoform's own letters, never about a person (chat v1.0.12).
	// Until a person card can name the person and what is open, the card is withheld: not shown, and not a removed source.
	if item.Kind == string(engagement.KindFirstWeek) && item.Target.Kind == "person" {
		*item = mainChatProactive{MomentID: item.MomentID, Kind: mainChatWithheldKind, State: "expired", Target: mainChatTarget{Kind: mainChatTargetNone}}
		return true
	}
	if item.Kind == "first_read" {
		a.projectFirstRead(ctx, item, lang)
		return true
	}
	if item.Kind == string(engagement.KindWeeklyRecap) && strings.TrimSpace(item.BriefID) == "" && !(item.Target.Kind == mainChatTargetToday && mobileValidBriefID(item.Target.ID)) {
		*item = mainChatProactive{MomentID: item.MomentID, Kind: "thread_update_unavailable", ActionIDs: []string{}, ActionLabels: map[string]string{}, Target: mainChatTarget{Kind: "none"}}
		return true
	}
	outcome := item.ActivityOutcomeSource != ""
	generated := item.ActivityOutcomeSource == "" && (item.VoiceVersion != "" || item.VoiceReceipt != "" || item.DraftContent != nil)
	journey := item.Kind == string(engagement.KindFirstWeek) || item.Kind == string(engagement.KindWeeklyRecap)
	changed := a.projectThreadOutcomeItem(ctx, item, lang) || generated || journey
	if journey {
		a.projectEngagementJourneyItem(ctx, item, lang)
	} else if generated {
		if !a.proactiveVoiceReceiptAllowsRead(ctx, item.VoiceReceipt, item.Kind, proactiveVoicePresentationHash(*item), false) || !a.projectProactiveCommitmentSentence(ctx, item, lang) {
			withdrawProactiveVoice(item, lang)
		}
		item.VoiceReceipt = ""
	} else if _, known := engagement.Spec(engagement.Kind(item.Kind)); known && !outcome {
		changed = true
		if !a.engagementCardAllowsRead(ctx, item.ReadReceipt, proactiveVoicePresentationHash(*item)) || !a.projectProactiveCommitmentSentence(ctx, item, lang) {
			withdrawProactiveVoice(item, lang)
			item.Target = sourceFreeProactiveTarget(item.Kind)
			item.Evidence = nil
		}
		item.ReadReceipt = ""
	}
	for i := range item.Items {
		changed = a.projectProactiveVoiceItem(ctx, &item.Items[i], lang) || changed
	}
	if changed && len(item.Items) > 0 {
		// The digest envelope must not retain a second copy of a child title.
		item.Headline = engagement.DigestHeadline(lang, len(item.Items))
		item.Body = engagement.DigestStatus(lang)
		item.Localized = nil
		item.Draft, item.DraftContent = false, nil
		populateMainChatProactiveCopy(item, nil)
	}
	return changed
}

// Project both representations together, before desktop localization or phone
// pagination. Stripping only structured metadata would expose Content to older
// clients. Invalid payloads are also replaced rather than falling back to text
// whose source permissions can no longer be established.
func (a *App) projectProactiveVoiceDelivery(ctx context.Context, tool, content, input string) (string, string) {
	if tool != mainChatProactiveTool && tool != mainChatStatusTool {
		return content, input
	}
	lang := a.currentUILanguage()
	if len(input) > mobileChatPageBudget {
		return engagement.DigestStatus(lang), ""
	}
	switch tool {
	case mainChatProactiveTool:
		var item mainChatProactive
		if json.Unmarshal([]byte(input), &item) != nil || item.MomentID == "" || item.Kind == "" {
			return engagement.DigestStatus(lang), ""
		}
		changed := a.projectProactiveVoiceItem(ctx, &item, lang)
		changed = a.applySavedWorkNames(ctx, input, &item) || changed
		if !changed {
			return content, input
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return engagement.DigestStatus(lang), ""
		}
		return proactiveFallback(item), string(raw)
	case mainChatStatusTool:
		var status mainChatStatus
		if json.Unmarshal([]byte(input), &status) != nil || len(status.Entries) == 0 {
			return engagement.DigestStatus(lang), ""
		}
		changed := false
		for i := range status.Entries {
			entry := &status.Entries[i]
			// Older voice capture status rows had no version marker. They
			// cannot prove current provenance and use source-free settings copy.
			changed = true
			allowed := a.engagementCardAllowsRead(ctx, entry.ReadReceipt, proactiveVoiceStatusHash(*entry))
			if entry.VoiceVersion != "" || entry.VoiceReceipt != "" {
				allowed = a.proactiveVoiceReceiptAllowsRead(ctx, entry.VoiceReceipt, entry.Kind, proactiveVoiceStatusHash(*entry), true)
			}
			if !allowed {
				item := mainChatProactive{MomentID: entry.MomentID, Kind: entry.Kind, Target: entry.Target}
				sentAt := entry.SentAt
				withdrawProactiveVoice(&item, lang)
				item.Target = sourceFreeProactiveTarget(item.Kind)
				item.Evidence = nil
				*entry = proactiveVoiceStatusEntry(item, entry.ID)
				entry.SentAt = sentAt
				if entry.Text == "" {
					entry.Localized = map[string]string{}
					for _, language := range engagement.Languages {
						text := engagement.StatusReport(language, entry.Kind, "")
						if text == "" {
							text = engagement.DigestStatus(language)
						}
						entry.Localized[language] = text
					}
					entry.Text = entry.Localized[lang]
				}
			}
			entry.VoiceReceipt = ""
			entry.ReadReceipt = ""
		}
		if changed {
			lines := make([]string, 0, len(status.Entries))
			for _, entry := range status.Entries {
				lines = append(lines, entry.Text)
			}
			raw, err := json.Marshal(status)
			if err != nil {
				return engagement.DigestStatus(lang), ""
			}
			return strings.Join(lines, "\n"), string(raw)
		}
	}
	return content, input
}
