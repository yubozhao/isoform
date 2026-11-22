package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/engagement"
)

const engagementJourneyReceiptPrefix = "engagement_journey_provenance:"
const engagementJourneyVoiceVersion = "engagement-journey-v1"

// Like model voice receipts, these stay local. The saved card carries only an
// opaque reference, stripped by the shared computer/phone replay projection.
type engagementJourneyReceipt struct {
	Version          int
	Kind             engagement.Kind
	Stage, SubjectID string
	PresentationHash string
}

func (a *App) engagementJourneyMomentForDelivery(ctx context.Context, m engagement.Moment, now time.Time) engagement.Moment {
	if m.Kind == engagement.KindFirstWeek && m.Params["stage"] == "welcome" {
		// Quiet hours or a locked computer can hold the welcome beyond the
		// original estimate. Do not promise a first brief at a past time.
		planned, err := time.ParseInLocation("2006-01-02 15:04", m.Params["first_brief"], now.Location())
		if err == nil && !planned.After(now) {
			m.Params = map[string]string{"stage": "welcome", "first_brief": ""}
		}
		// The day-0 welcome names only sources with a current saved choice.
		// Pass 1 is local and quick; if it is not ready in time the welcome
		// keeps its usual text.
		if key := assistantHistoryHistoryKey(a.assistantHistoryAssistantsWithin(assistantHistoryWelcomeWait)); key != "" {
			params := make(map[string]string, len(m.Params)+2)
			for k, v := range m.Params {
				params[k] = v
			}
			params["history"] = key
			params["name"] = a.getUserDisplayName()
			m.Params = params
		}
	}
	return m
}

func (a *App) saveEngagementJourneyReceipt(ctx context.Context, m engagement.Moment, item *mainChatProactive) bool {
	if a == nil || a.store == nil {
		return false
	}
	receipt := engagementJourneyReceipt{Version: 1, Kind: m.Kind, Stage: m.Params["stage"], SubjectID: m.SubjectID}
	item.VoiceVersion = engagementJourneyVoiceVersion
	receipt.PresentationHash = proactiveVoicePresentationHash(*item)
	raw, err := json.Marshal(receipt)
	id := uuid.NewString()
	if err != nil || a.store.SetUIState(ctx, engagementJourneyReceiptPrefix+id, string(raw)) != nil {
		return false
	}
	item.VoiceReceipt = id
	return true
}

func (a *App) projectEngagementJourneyItem(ctx context.Context, item *mainChatProactive, lang string) {
	var receipt engagementJourneyReceipt
	valid := false
	if a != nil && a.store != nil && ctx.Err() == nil {
		if _, err := uuid.Parse(item.VoiceReceipt); err == nil {
			raw, err := a.store.GetUIState(ctx, engagementJourneyReceiptPrefix+item.VoiceReceipt)
			valid = err == nil && json.Unmarshal([]byte(raw), &receipt) == nil && receipt.Version == 1 &&
				string(receipt.Kind) == item.Kind && item.VoiceVersion == engagementJourneyVoiceVersion &&
				receipt.PresentationHash == proactiveVoicePresentationHash(*item)
		}
	}
	if valid {
		switch receipt.Kind {
		case engagement.KindFirstWeek:
			switch receipt.Stage {
			case "welcome", "brief":
			case "commitment", "person", "meeting":
				now := time.Now()
				if a.engagement != nil {
					now = a.engagement.now()
				}
				valid = a.firstWeekSourceReadable(ctx, engagement.Moment{SubjectID: receipt.SubjectID, Params: map[string]string{"stage": receipt.Stage}}, now)
			default:
				valid = false
			}
		default:
			// "Your week" lines, and the count recaps they replace, read
			// as the catalog doorbell that opens the brief.
			valid = false
		}
	}
	if !valid {
		withdrawProactiveVoice(item, lang)
		// Generic copy cannot retain a protected locator in an action,
		// evidence link, a secondary locale or the legacy text fallback.
		// A weekly recap keeps the id of the brief that holds its week: an
		// Isoform brief id is not a source locator, and without it "Read your
		// week" lands on today's brief, which has no week.
		target := mainChatTarget{Kind: "today"}
		if item.Kind == string(engagement.KindWeeklyRecap) && item.Target.Kind == mainChatTargetToday && mobileValidBriefID(item.Target.ID) {
			target.ID = item.Target.ID
		}
		item.Target = target
		item.Evidence = []mainChatEvidenceRef{}
		item.Items = nil
	}
	item.VoiceReceipt = ""
}
