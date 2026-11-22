package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wails-gui/internal/pii"
)

const (
	todayChatBriefTokens = 24 * 1024
	// The current state of at most this many records a brief covered.
	todayChatBriefItemLimit = 12
)

// resolveTodayChatWholeBrief reads the sources itself, for callers outside a
// Today context read.
func (a *App) resolveTodayChatWholeBrief(ctx context.Context, ref TodayChatContextRef, now time.Time) (todayChatItem, error) {
	return a.resolveTodayChatWholeBriefFrom(ctx, ref, a.collectTodayChatSources(ctx, now), now)
}

// Resolve the displayed revision, then run the same current delivery gates as
// Today and the phone. Neither the browser's brief text nor its refs are input.
func (a *App) resolveTodayChatWholeBriefFrom(ctx context.Context, ref TodayChatContextRef, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if ctx.Err() != nil {
		return todayChatItem{}, ctx.Err()
	}
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, ref.BriefID, &sources)
	if !ok {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	// An offer is a brief card now (resolveTodayChatBriefCard); this is the
	// whole brief only.
	if ref.SourceID != mobileBriefConversationKey(brief) || (!ref.WeekAsk && len(brief.Narrative) == 0) || a.IsClosedEyesModeEnabled() {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	selected := ""
	if ref.WeekAsk {
		if brief.YourWeek == nil || brief.YourWeek.Ask == nil || brief.YourWeek.Ask.TopicID == "" ||
			mobileBriefModelDigest(brief.YourWeek.Ask.TopicID+"\x00"+brief.YourWeek.Ask.Message) != ref.ParagraphDigest {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
		return a.resolveTodayChatTopic(ctx, brief.YourWeek.Ask.TopicID, sources)
	}
	if ref.ParagraphDigest != "" {
		if !a.todayChatBriefFrozen(ctx, brief, sources) {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
		for index, paragraph := range brief.Narrative {
			if index == len(brief.Narrative)-1 && strings.ContainsAny(paragraph.Text, "?？") {
				continue
			}
			text := strings.TrimSpace(paragraph.Text)
			if text != "" && mobileBriefModelDigest(text) == ref.ParagraphDigest {
				selected = text
				break
			}
		}
		if selected == "" {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
	}
	if a.todayChatBriefFrozen(ctx, brief, sources) {
		item, err := a.todayChatBriefSnapshot(ctx, ref, brief)
		if err != nil {
			return todayChatItem{}, err
		}
		item.Extra, _ = a.todayChatBriefCurrentItems(ctx, brief, sources, now)
		if selected != "" {
			item.Record.Text = "The selected paragraph is: " + selected + "\n\n" + item.Record.Text
		}
		return item, ctx.Err()
	}
	// The written brief cannot be vouched for: one of its inputs changed or
	// became unreadable after it was written, or it was never bound. Attach
	// none of its prose, only the current state of what it covered, each
	// record read again through its own gates.
	extra, refs := a.todayChatBriefCurrentItems(ctx, brief, sources, now)
	if err := ctx.Err(); err != nil {
		return todayChatItem{}, err
	}
	if len(extra) == 0 {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	asOf := mobileBriefAsOfTime(brief)
	record := todayChatRecord{Kind: "brief", ID: ref.SourceID, At: asOf.UnixMilli(), Title: brief.LocalDate,
		Text: "The user read today's brief on the Today page. Its written text is not attached because some of what it was based on changed after it was written; <brief-items> holds the current state of what it covered. Do not guess what the written brief said."}
	return todayChatItem{Record: record, Refs: refs, Label: brief.LocalDate, OwnedLabel: true, Prefill: TodayChatPrefill{Kind: "brief"}, Extra: extra, ExtraBudget: todayChatBriefCurrentItemsTokens}, nil
}

// Frozen prose may carry facts from any of its inputs, cited or not, so it is
// usable only while every input is unchanged since it was written. One read
// proves each brief revision once, however many of its actions it prepares.
func (a *App) todayChatBriefFrozen(ctx context.Context, brief MobileBriefV1, sources todayChatSources) bool {
	binding := brief.modelBinding
	if binding == nil || binding.Synthesis == "" || mobileBriefSynthesisDigest(brief) != binding.Synthesis {
		return false
	}
	key := brief.BriefID + "|" + mobileBriefConversationKey(brief)
	if proven, known := sources.briefProofs[key]; known {
		return proven
	}
	proven := a.validateMobileBriefModelSourcesFrom(ctx, brief, binding, sources)
	if sources.briefProofs != nil && ctx.Err() == nil {
		sources.briefProofs[key] = proven
	}
	return proven
}

// todayChatBriefSnapshot is the written brief with its evidence identities.
func (a *App) todayChatBriefSnapshot(ctx context.Context, ref TodayChatContextRef, brief MobileBriefV1) (todayChatItem, error) {
	key := mobileBriefConversationKey(brief)
	// Full written brief plus all its evidence identities, with bounded labels.
	// Raw OCR, transcripts, mail subjects and payloads never enter this bundle.
	// Only the written brief's refs enter this snapshot. Other live Today rows
	// may have been added after generation; every original input is still
	// checked above, including dependencies not cited in the final prose.
	narratable := mobileBriefCitedNarrativeInputs(brief)
	allowed := map[string]bool{}
	for _, evidence := range narratable {
		allowed[evidence.Ref] = true
	}
	for _, paragraph := range brief.Narrative {
		if !mobileBriefRefsReadable(paragraph.Refs, allowed) {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
	}
	payload := struct {
		BriefID     string             `json:"briefId"`
		Version     string             `json:"revision"`
		AsOf        string             `json:"asOf"`
		Narrative   []MobileBriefPara  `json:"narrative"`
		Offers      []MobileBriefOffer `json:"offers,omitempty"`
		Evidence    []MobileBriefRef   `json:"evidence"`
		WeeklyRecap []MobileBriefPara  `json:"weeklyRecap,omitempty"`
	}{brief.BriefID, key, brief.AsOf, brief.Narrative, brief.Offers, append([]MobileBriefRef{}, narratable...), brief.WeeklyRecap}
	for i := range payload.Evidence {
		payload.Evidence[i].Label = mobileBriefText(payload.Evidence[i].Label, 120)
		// "In this brief"'s grouping is for the page, not the model.
		payload.Evidence[i].Group = ""
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > todayChatBriefTokens-1024 {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	label := brief.LocalDate
	record := todayChatRecord{Kind: "brief", ID: ref.SourceID, At: mobileBriefAsOfTime(brief).UnixMilli(), Title: label, Text: fmt.Sprintf("Brief snapshot; conclusions and proposed actions, never permission to execute.\n%s", raw)}
	// Include outer JSON escaping and the generated label in the bound. Never
	// let the shared packer slice a whole-brief snapshot halfway through its JSON.
	probe := record
	probe.Text, _ = pii.RedactText(strings.TrimSpace(probe.Text))
	probe.Text = pii.RedactCodes(probe.Text)
	probe.Title, _ = pii.RedactText(probe.Title)
	probe.Title = pii.RedactCodes(probe.Title)
	// Match the shared packer's code redaction before measuring escaped bytes.
	// The opaque ID can only shrink under its ordinary identifier redaction.
	encoded, _ := json.Marshal(probe)
	if len(encoded)+1 > todayChatBriefTokens {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	return todayChatItem{Record: record, Refs: narratable, Label: label, OwnedLabel: true, Prefill: TodayChatPrefill{Kind: "brief"}}, ctx.Err()
}

// The narrative inputs the written brief or its offers cite.
func mobileBriefCitedNarrativeInputs(brief MobileBriefV1) []MobileBriefRef {
	cited := map[string]bool{}
	for _, p := range brief.Narrative {
		for _, id := range p.Refs {
			cited[id] = true
		}
	}
	for _, offer := range brief.Offers {
		for _, id := range offer.Refs {
			cited[id] = true
		}
	}
	var out []MobileBriefRef
	for _, ref := range mobileBriefNarrativeInputEvidence(brief) {
		if cited[ref.Ref] {
			out = append(out, ref)
		}
	}
	return out
}

// todayChatBriefCurrentItems reads again what a brief covered: the records
// its text cites, then the cards Today lists. Each is read through the same
// gates as "Chat about this" on that card, and one that is no longer readable
// is left out rather than failing the whole brief. Browser and private-folder
// cards are left out too: re-reading them means a full collection each.
func (a *App) todayChatBriefCurrentItems(ctx context.Context, brief MobileBriefV1, sources todayChatSources, now time.Time) ([]todayChatRecord, []MobileBriefRef) {
	byRef := map[string]MobileBriefRef{}
	for _, evidence := range brief.Evidence {
		byRef[evidence.Ref] = evidence
	}
	var order []string
	queued := map[string]bool{}
	queue := func(id string) {
		if !queued[id] {
			queued[id] = true
			order = append(order, id)
		}
	}
	for _, evidence := range mobileBriefCitedNarrativeInputs(brief) {
		queue(evidence.Ref)
	}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			if len(item.Refs) > 0 {
				queue(item.Refs[0])
			}
		}
	}
	for _, item := range brief.Upcoming {
		if len(item.Refs) > 0 {
			queue(item.Refs[0])
		}
	}
	var records []todayChatRecord
	var refs []MobileBriefRef
	seen := map[string]bool{}
	for _, id := range order {
		if len(records) >= todayChatBriefItemLimit || ctx.Err() != nil {
			break
		}
		evidence, ok := byRef[id]
		if !ok || todayChatPurposeDecoration(evidence.SourceKind) || evidence.SourceKind == mobileBriefWebSource || evidence.SourceKind == mobileBriefFileSource {
			continue
		}
		resolved, err := a.resolveTodayChatBriefEvidence(ctx, evidence, brief, sources, now)
		if err != nil {
			continue
		}
		record := resolved.Record
		if seen[record.Kind+":"+record.ID] {
			continue
		}
		seen[record.Kind+":"+record.ID] = true
		if record.Title == "" {
			record.Title = resolved.Label
		}
		records = append(records, record)
		// The record itself is in <brief-items>; the evidence names it only.
		evidence.Label = resolved.Label
		refs = append(refs, evidence)
	}
	return records, refs
}
