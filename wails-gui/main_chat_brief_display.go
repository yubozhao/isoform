package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/brief"
)

// Title metadata and the card's first words are projected. The words come from
// the same privacy-processed brief read Today uses (finishedBrief); the rest of
// the narrative and the actions never enter a stream item.
type mainChatBriefDisplay struct {
	Slot     string `json:"slot"`
	AsOf     string `json:"asOf"`
	Timezone string `json:"timezone"`
}

func mainChatBriefDisplayFor(brief MobileBriefV1, state upcomingBriefState, loc *time.Location) mainChatBriefDisplay {
	slot := brief.Slot
	if slot != "morning" && slot != "evening" {
		slot = brief.DisplaySlot
	}
	if slot != "morning" && slot != "evening" {
		slot = "on_request"
	}
	result := mainChatBriefDisplay{Slot: slot, AsOf: brief.AsOf, Timezone: brief.Timezone}
	if slot == "on_request" {
		return result
	}
	// Use the same historical correction and late rule as Tasks. Limit the
	// input to this brief so its run is not lost behind the three-run window.
	runs := state.Runs
	state.Runs = nil
	for _, run := range runs {
		if run.BriefID == brief.BriefID && run.Status == mobileBriefRunOK {
			state.Runs = append(state.Runs, run)
		}
	}
	state.MadeOnRequest = nil
	state.Settings.Enabled = false
	for _, run := range briefScheduleDetailFrom(time.Now(), loc, state, slot).Runs {
		if run.BriefID != brief.BriefID || run.Status != mobileBriefRunOK {
			continue
		}
		// The made-at time, as Today shows it (brief-rebuild rule 1), whether the
		// brief was on time or late; the slot time is never the card's time (R64).
		if run.At != "" {
			result.AsOf = run.At
		} else if run.ScheduledAt != "" {
			result.AsOf = run.ScheduledAt
		}
		break
	}
	return result
}

func (a *App) mainChatBriefDisplayProjector() func(string, string) string {
	var service *mobileBriefService
	serviceChecked := false
	var state upcomingBriefState
	loaded := false
	cache := map[string]*mainChatBriefDisplay{}
	leads := map[string]string{}
	var reader *brief.Reader
	return func(tool, input string) string {
		if tool != mainChatProactiveTool {
			return input
		}
		var card map[string]json.RawMessage
		if json.Unmarshal([]byte(input), &card) != nil {
			return input
		}
		var enrich func(map[string]json.RawMessage)
		enrich = func(item map[string]json.RawMessage) {
			var briefID, kind string
			var evidence []mainChatEvidenceRef
			var target mainChatTarget
			_ = json.Unmarshal(item["briefId"], &briefID)
			_ = json.Unmarshal(item["kind"], &kind)
			_ = json.Unmarshal(item["evidence"], &evidence)
			_ = json.Unmarshal(item["target"], &target)
			// Older first-week brief cards stored their brief locator in evidence,
			// before briefId was added. Resolve only an existing saved brief.
			if kind == "first_week" && briefID == "" {
				for _, ref := range evidence {
					if mobileValidBriefID(ref.ID) {
						if !serviceChecked {
							service, _ = a.ensureMobileBriefService().(*mobileBriefService)
							serviceChecked = true
						}
						if service != nil && service.store != nil {
							if _, found := service.store.load(ref.ID); found {
								briefID = ref.ID
								item["briefId"], _ = json.Marshal(briefID)
								break
							}
						}
					}
				}
			}
			// A withdrawn ordinary card loses its evidence. A weekly recap's
			// sanitized Today target may retain only its own brief locator.
			allowed := len(evidence) > 0 || (kind == "weekly_recap" && target.Kind == "today" && target.ID == briefID)
			if briefID != "" && allowed && (kind == "brief_ready" || kind == "weekly_recap" || kind == "first_week") {
				if !serviceChecked {
					service, _ = a.ensureMobileBriefService().(*mobileBriefService)
					serviceChecked = true
				}
				if service == nil || service.store == nil {
					return
				}
				display, seen := cache[briefID]
				if !seen {
					if brief, found := service.store.load(briefID); found {
						if !loaded && service.config != nil && service.runs != nil {
							state = upcomingBriefState{Settings: service.config.load(), Runs: service.runs.list()}
							loaded = true
						}
						briefState := state
						briefState.Runs = append([]mobileBriefRun(nil), state.Runs...)
						if brief.Generation != nil && brief.Generation.ScheduledAt != "" {
							if _, err := time.Parse(time.RFC3339Nano, brief.Generation.ScheduledAt); err == nil {
								for i := range briefState.Runs {
									if briefState.Runs[i].BriefID == brief.BriefID {
										briefState.Runs[i].ScheduledAt = brief.Generation.ScheduledAt
									}
								}
							}
						}
						value := mainChatBriefDisplayFor(brief, briefState, service.location)
						display = &value
					}
					cache[briefID] = display
				}
				if display != nil {
					item["briefDisplay"], _ = json.Marshal(display)
					// The card's first words come with the card, from the same
					// privacy-processed read Today uses, so the card draws whole.
					lead, seenLead := leads[briefID]
					if !seenLead {
						if reader == nil {
							reader = a.mainChatSealedBriefReader(service)
						}
						lead = a.mainChatSealedBriefLead(service, reader, briefID)
						leads[briefID] = lead
					}
					if lead != "" {
						item["briefLead"], _ = json.Marshal(lead)
					}
				}
			}
			var children []json.RawMessage
			if json.Unmarshal(item["items"], &children) == nil {
				for i, child := range children {
					var object map[string]json.RawMessage
					if json.Unmarshal(child, &object) == nil && object != nil {
						enrich(object)
						children[i], _ = json.Marshal(object)
					}
				}
				item["items"], _ = json.Marshal(children)
			}
		}
		enrich(card)
		encoded, err := json.Marshal(card)
		if err != nil {
			return input
		}
		return string(encoded)
	}
}

// mainChatBriefLead is the first paragraph of a brief's words, trimmed, or ""
// when the brief has none to show.
func mainChatBriefLead(brief MobileBriefV1) string {
	for _, paragraph := range brief.Narrative {
		if text := strings.TrimSpace(paragraph.Text); text != "" {
			return text
		}
	}
	return ""
}

// The card's first words come from the brief's sealed copy, read through the
// brief package's Reader: one file read, no gate and no seal. A copy sealed
// under another privacy print or day is not shown here; the card draws without
// its lead and the finished read Today uses amends the copy in the background
// (mainChatAmendBriefLater). The background stream refresh then saves the page
// with the lead and tells the chat to read it again. Running that amend on the
// projection path held a page of brief cards for 6 to 14 s.
func (a *App) mainChatSealedBriefReader(service *mobileBriefService) *brief.Reader {
	return brief.NewReader(brief.ReaderOptions{Dir: service.store.dir, Print: a.briefPrivacyPrint, Day: service.readDay})
}

func (a *App) mainChatSealedBriefLead(service *mobileBriefService, reader *brief.Reader, briefID string) string {
	if a.IsClosedEyesModeEnabled() {
		// Closed Eyes: nothing the brief says stays readable.
		return ""
	}
	sealed, ok := reader.Get(briefID)
	if !ok {
		a.mainChatAmendBriefLater(service, briefID)
		return ""
	}
	var copy MobileBriefV1
	if unmarshalStoredMobileBrief(sealed.Raw, &copy) != nil || copy.BriefID != briefID {
		return ""
	}
	if a.store == nil || a.store.Closing() {
		// As the finished read does: with the policy store closed, a web source's
		// hold cannot be confirmed, so its items are left out.
		copy = mobileBriefWithoutWebSources(copy)
	}
	return mainChatBriefLead(copy)
}

// mainChatBriefAmends are the briefs whose background finished read is running.
var mainChatBriefAmends sync.Map

// mainChatAmendBriefLater runs the finished read of one brief off the
// projection path, once at a time per brief, so its copy is sealed or amended.
func (a *App) mainChatAmendBriefLater(service *mobileBriefService, briefID string) {
	if _, running := mainChatBriefAmends.LoadOrStore(briefID, true); running {
		return
	}
	go func() {
		defer mainChatBriefAmends.Delete(briefID)
		if _, found, err := service.finishedBrief(briefID); err == nil && found {
			a.scheduleStreamPageRefresh(time.Now())
		}
	}()
}
