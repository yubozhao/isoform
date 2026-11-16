package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// Brief cards in main chat (backlog item 56). One route serves Today and the
// phone: the click names the brief and the card, and the computer reads the
// brief as delivered now, finds the card (a card whose source can no longer be
// read has left; one whose facts changed was written again from the current
// facts), reads the card's sources through their own gates, and sends the
// card's own words. No client builds or edits that text.
const (
	todayChatBriefCardKind  = "brief_card"
	todayChatCardActionKind = "card_action"
)

// todayChatDeliveredBrief reads a stored brief the way Today and the phone
// show it: through the current delivery gates, with its cards.
func (a *App) todayChatDeliveredBrief(briefID string) (MobileBriefV1, bool) {
	return a.todayChatDeliveredBriefFrom(context.Background(), briefID, nil)
}

func (a *App) todayChatDeliveredBriefFrom(ctx context.Context, briefID string, sources *todayChatSources) (MobileBriefV1, bool) {
	dir, err := mobileBriefStateDir()
	if err != nil {
		return MobileBriefV1{}, false
	}
	store := newMobileBriefStore(dir)
	brief, ok := store.load(briefID)
	if !ok {
		brief, ok = store.currentToday()
		ok = ok && brief.BriefID == briefID
	}
	if !ok {
		return MobileBriefV1{}, false
	}
	return a.mobileBriefWithCardsFrom(ctx, a.mobileBriefForDeliveryContext(ctx, brief), sources), true
}

// todayChatSealedBrief is the sealed read of a brief, the copy the phone reads.
func (a *App) todayChatSealedBrief(briefID string) (MobileBriefV1, bool) {
	svc, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || svc == nil {
		return MobileBriefV1{}, false
	}
	sealed, found, err := svc.finishedBrief(briefID)
	return sealed, err == nil && found
}

// resolveTodayChatBriefCard is the item a card's turn attaches: the card's
// subject read now through its own gates, with the card's exact words.
func (a *App) resolveTodayChatBriefCard(ctx context.Context, ref TodayChatContextRef, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if err := ctx.Err(); err != nil {
		return todayChatItem{}, err
	}
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, ref.BriefID, &sources)
	if !ok || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	card, ok := a.todayChatCardNow(ctx, brief, ref.SourceID, sources)
	if !ok {
		// The phone holds the sealed read (seal v2). A Today refresh renumbers
		// the stored copy's evidence, so a card the phone shows may exist under
		// its id in the sealed read only. Its subject is still read now, through
		// its own gates, from that read's evidence.
		if sealed, found := a.todayChatSealedBrief(ref.BriefID); found {
			brief = sealed
			card, ok = a.todayChatCardNow(ctx, brief, ref.SourceID, sources)
		}
	}
	if !ok || card.Mode != mobileBriefCardSend {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	item, err := a.todayChatCardSubject(ctx, brief, card, sources, now)
	if err != nil {
		return todayChatItem{}, err
	}
	if card.Kind == mobileBriefCardQuestion && card.Paragraph != nil {
		index := *card.Paragraph
		if index < 0 || index >= len(brief.Narrative) {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
		if id := brief.Narrative[index].TopicID; id != "" {
			topic, err := a.resolveTodayChatTopic(ctx, id, sources)
			if err != nil {
				return todayChatItem{}, err
			}
			item.topicID, item.topicRefs = id, topic.topicRefs
			item.Extra = append(item.Extra, topic.Record)
		}
	}
	if ref.BriefDraft {
		item.Prefill.Kind = "brief_draft"
	} else if card.Kind == mobileBriefCardAction {
		item.Prefill.Kind = todayChatCardActionKind
	}
	return item, nil
}

// todayChatCardNow is a card as it may go out now. Words the brief's model
// wrote stay only while every input that model read is unchanged
// (todayChatBriefFrozen), as for the brief's own text; otherwise the card is
// Isoform's own sentence from the current facts.
func (a *App) todayChatCardNow(ctx context.Context, brief MobileBriefV1, id string, sources todayChatSources) (MobileBriefCard, bool) {
	card, ok := mobileBriefFindCard(brief, id)
	if !ok {
		return mobileBriefLegacyOfferCard(brief, id, a.currentUILanguageName())
	}
	if ok && card.Kind == mobileBriefCardAction && !mobileBriefAnswerMatches(brief, card) {
		return MobileBriefCard{}, false
	}
	if !ok || card.Writer != mobileBriefCardWriterModel || a.todayChatBriefFrozen(ctx, brief, sources) {
		return card, ok
	}
	if card.AnswerTo != "" {
		return MobileBriefCard{}, false
	}
	for _, pattern := range mobileBriefPatternCards(brief, a.currentUILanguageName()) {
		if pattern.ID == id && pattern.Kind == card.Kind {
			return pattern, true
		}
	}
	return MobileBriefCard{}, false
}

// todayChatCardSubject reads a card's sources now through their own gates and
// returns them with the card's exact words, naming the subject as it reads
// now, so a corrected title never goes out.
func (a *App) todayChatCardSubject(ctx context.Context, brief MobileBriefV1, card MobileBriefCard, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if len(card.Refs) == 0 || strings.TrimSpace(card.Message) == "" {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	item, err := a.resolveTodayChatBriefRefs(ctx, brief, card.Refs, sources, now)
	if err != nil {
		return todayChatItem{}, err
	}
	// A subject whose title is withheld leaves nothing to name.
	if item.Label == "" || item.Label == item.Record.ID {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	message := card.Message
	if subject, found := mobileBriefCardSubject(brief, card.Refs); found {
		if stored, current := mobileBriefCardTitle(subject), todayChatItemLabel(item); stored != "" && current != "" && stored != current {
			message = strings.ReplaceAll(message, stored, current)
			// The user's edited words may still name the stored title.
			item.PriorLabels = append(item.PriorLabels, stored)
		}
	}
	item.CardMessage = message
	return item, nil
}

// TodayBriefOpened is "Talk about this brief" after Isoform spoke first: its
// opening line is already in the main chat, and the brief, prepared like any
// Today action, goes with the user's answer.
type TodayBriefOpened struct {
	Ref   TodayChatContextRef `json:"ref"`
	Draft TodayChatDraft      `json:"draft"`
}

// OpenTodayBriefConversation posts the brief's opener as Isoform's own
// message in the main chat and returns the brief to attach to the answer.
// The brief and the opener's sources are read now, as at any click: an opener
// whose subject no longer reads is not posted.
func (a *App) OpenTodayBriefConversation(briefID string) (TodayBriefOpened, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return a.openTodayBriefConversation(ctx, briefID)
}

func (a *App) openTodayBriefConversation(ctx context.Context, briefID string) (TodayBriefOpened, error) {
	if a == nil || a.store == nil {
		return TodayBriefOpened{}, errTodayChatItemUnavailable
	}
	if a.IsClosedEyesModeEnabled() {
		return TodayBriefOpened{}, errTodayChatObservationPaused
	}
	now := time.Now()
	sources := a.collectTodayChatSources(ctx, now)
	// Paragraph topic links are navigation decorations, not part of the
	// conversation key or its privacy proof. Opening the whole brief does
	// not need to validate every unrelated topic.
	sources.skipBriefTopics = true
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, briefID, &sources)
	if err := ctx.Err(); err != nil {
		return TodayBriefOpened{}, err
	}
	if !ok {
		return TodayBriefOpened{}, errTodayChatItemUnavailable
	}
	card, ok := a.todayChatCardNow(ctx, brief, mobileBriefOpenerCardID, sources)
	if !ok {
		return TodayBriefOpened{}, errTodayChatItemUnavailable
	}
	opener, err := a.todayChatCardSubject(ctx, brief, card, sources, now)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: brief.ConversationKey, BriefID: brief.BriefID}
	item, err := a.resolveTodayChatWholeBriefFrom(ctx, ref, sources, now)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	draft, err := a.prepareLoadedTodayChatDraft(ctx, item, sources)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	if err := a.postTodayBriefOpener(ctx, main.LocalID, opener.CardMessage, "today_brief_opener"); err != nil {
		return TodayBriefOpened{}, err
	}
	return TodayBriefOpened{Ref: ref, Draft: draft}, nil
}

// PrepareTodayBriefParagraph validates the selected paragraph without posting
// an opener. The returned brief key is revalidated when the question is sent.
func (a *App) PrepareTodayBriefParagraph(briefID string, paragraph int, text string) (TodayChatContextRef, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	brief, _, err := a.todayBriefParagraphMessage(ctx, briefID, "", paragraph, text, true)
	if err != nil {
		return TodayChatContextRef{}, err
	}
	return TodayChatContextRef{SourceKind: "brief", SourceID: brief.ConversationKey, BriefID: brief.BriefID, ParagraphDigest: mobileBriefModelDigest(strings.TrimSpace(text))}, nil
}

// OpenTodayBriefParagraph starts a main-chat conversation about one body
// paragraph, quoting the text that the user selected.
func (a *App) OpenTodayBriefParagraph(briefID string, paragraph int, text string) (TodayBriefOpened, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	brief, message, err := a.todayBriefParagraphMessage(ctx, briefID, "", paragraph, text, true)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: brief.ConversationKey, BriefID: brief.BriefID}
	draft, err := a.PrepareTodayChatDraft(ref)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return TodayBriefOpened{}, err
	}
	if err := a.postTodayBriefOpener(ctx, main.LocalID, message, "today_brief_paragraph"); err != nil {
		return TodayBriefOpened{}, err
	}
	return TodayBriefOpened{Ref: ref, Draft: draft}, nil
}

func (a *App) todayBriefParagraphMessage(ctx context.Context, briefID, version string, paragraph int,
	text string, matchText bool) (MobileBriefV1, string, error) {
	if a == nil || a.store == nil {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	if a.IsClosedEyesModeEnabled() {
		return MobileBriefV1{}, "", errTodayChatObservationPaused
	}
	now := time.Now()
	sources := a.collectTodayChatSources(ctx, now)
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, briefID, &sources)
	if err := ctx.Err(); err != nil {
		return MobileBriefV1{}, "", err
	}
	if !ok || version != "" && version != brief.ConversationKey {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	text = strings.TrimSpace(text)
	if matchText && text == "" {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	closing := -1
	if last := len(brief.Narrative) - 1; last >= 0 && strings.ContainsAny(brief.Narrative[last].Text, "?？") {
		closing = last
	}
	selected := -1
	if paragraph >= 0 && paragraph < len(brief.Narrative) &&
		(!matchText || strings.TrimSpace(brief.Narrative[paragraph].Text) == text) {
		selected = paragraph
	} else if matchText {
		for index, para := range brief.Narrative {
			if index != closing && strings.TrimSpace(para.Text) == text {
				selected = index
				break
			}
		}
	}
	if selected < 0 || selected == closing {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	body := strings.TrimSpace(brief.Narrative[selected].Text)
	if body == "" {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	prefix := "About this part of your brief:"
	if mobileBriefVoiceLang(a.currentUILanguageName()) == "zh" {
		prefix = "关于简报里的这一段："
	}
	quoted := make([]string, 0, strings.Count(body, "\n")+1)
	for _, line := range strings.Split(body, "\n") {
		quoted = append(quoted, "> "+line)
	}
	message := prefix + "\n\n" + strings.Join(quoted, "\n")
	return brief, message, nil
}

// todayBriefOpener reads the brief as delivered now and its opener through
// the opener subject's own gates, and returns the words Isoform says first.
// A non-empty version must be the revision the reader showed (the phone's
// briefVersion).
func (a *App) todayBriefOpener(ctx context.Context, briefID, version string) (MobileBriefV1, string, error) {
	if a == nil || a.store == nil {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	if a.IsClosedEyesModeEnabled() {
		return MobileBriefV1{}, "", errTodayChatObservationPaused
	}
	now := time.Now()
	sources := a.collectTodayChatSources(ctx, now)
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, briefID, &sources)
	if !ok || version != "" && version != brief.ConversationKey {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	card, ok := a.todayChatCardNow(ctx, brief, mobileBriefOpenerCardID, sources)
	if !ok {
		return MobileBriefV1{}, "", errTodayChatItemUnavailable
	}
	opener, err := a.todayChatCardSubject(ctx, brief, card, sources, now)
	if err != nil {
		return MobileBriefV1{}, "", err
	}
	return brief, opener.CardMessage, nil
}

// postTodayBriefOpener appends the opener as Isoform's message, once: a second
// click right after the first does not repeat it.
func (a *App) postTodayBriefOpener(ctx context.Context, mainID, message, reason string) error {
	mainChatPostMu.Lock()
	defer mainChatPostMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if page, err := a.store.LoadStreamItemsLatest(ctx, mainID, 1); err == nil && len(page.Items) == 1 {
		last := page.Items[0]
		if last.Kind == "assistant" && last.Content == message {
			return nil
		}
	}
	if a.appendMainChatItem(ctx, mainID, storage.StreamItemRow{ID: uuid.NewString(), Kind: "assistant", Content: message}, reason) == "" {
		return errors.New("could not post brief opener")
	}
	return nil
}
