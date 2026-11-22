package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The attachment belongs to this admitted turn. Re-read its source when the
// person corrects it, including the revision and sentence they replied to.
func (t *chatCorrectionTool) attachedTarget(ctx context.Context) (*mobileChatAbout, error) {
	ref := t.ref
	if ref == nil {
		return nil, nil
	}
	switch ref.SourceKind {
	case todayChatWorkKind:
		return &mobileChatAbout{Kind: "work_item", ID: ref.SourceID}, nil
	case "principal":
		return &mobileChatAbout{Kind: "person", ID: ref.SourceID}, nil
	case "brief":
		if ref.ParagraphDigest != "" && !ref.WeekAsk {
			brief, err := t.app.correctionBrief(ctx, ref.BriefID)
			if err != nil {
				return nil, err
			}
			if ref.SourceID != mobileBriefConversationKey(brief) {
				return nil, errTodayChatItemUnavailable
			}
			var id string
			for i, p := range brief.Narrative {
				if mobileBriefModelDigest(strings.TrimSpace(p.Text)) != ref.ParagraphDigest {
					continue
				}
				candidate := workBriefSentenceID(brief.BriefID, i)
				if ref.AboutID != "" && ref.AboutID != candidate {
					continue
				}
				if id != "" {
					return nil, errors.New("the attached sentence is ambiguous")
				}
				id = candidate
			}
			if id == "" {
				return nil, errTodayChatItemUnavailable
			}
			return &mobileChatAbout{Kind: "brief_sentence", ID: id}, nil
		}
	}
	return nil, errors.New("this attachment does not support corrections")
}

func (a *App) correctionBrief(ctx context.Context, id string) (MobileBriefV1, error) {
	if err := ctx.Err(); err != nil {
		return MobileBriefV1{}, err
	}
	svc := a.mobileBriefSource()
	if svc == nil {
		return MobileBriefV1{}, errMobileStoreUnavailable
	}
	if service, ok := svc.(*mobileBriefService); ok {
		if !mobileBriefIDPattern.MatchString(id) {
			return MobileBriefV1{}, errTodayChatItemUnavailable
		}
		source, err := service.store.readBriefFile(id + ".json")
		if errors.Is(err, os.ErrNotExist) {
			source, err = service.store.readBriefFile(mobileBriefTodayFileName)
			if err == nil && source.LocalDate != service.readDay() {
				return MobileBriefV1{}, errTodayChatItemUnavailable
			}
		}
		if err != nil {
			return MobileBriefV1{}, err
		}
		if source.BriefID != id {
			return MobileBriefV1{}, errTodayChatItemUnavailable
		}
		brief, err := service.finishedRead(source)
		if err != nil {
			return MobileBriefV1{}, err
		}
		return brief, ctx.Err()
	}
	brief, ok := svc.GetMobileBrief(id)
	if !ok {
		return brief, errTodayChatItemUnavailable
	}
	return brief, ctx.Err()
}

func chatCorrectionBefore(item WorkItem) string {
	for _, value := range []string{item.Work.Text, item.Noticed, item.Work.Title} {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func (t *chatCorrectionTool) apply(ctx context.Context, p *chatCorrectionPayload) error {
	// Personal notes need no observation. A target must still pass the
	// same read gates as its Chat attachment.
	if (t.ref != nil || p.About != nil) && t.app.IsClosedEyesModeEnabled() {
		return errTodayChatObservationPaused
	}
	attached, err := t.attachedTarget(ctx)
	if err != nil {
		return err
	}
	if attached != nil {
		if p.About != nil && *p.About != *attached {
			return errors.New("the target differs from the attachment")
		}
		p.About = attached
	}
	p.Applied = "note"
	if target := p.About; target != nil {
		switch target.Kind {
		case "person":
			person, err := t.app.store.GetPrincipal(ctx, target.ID)
			if err != nil {
				return err
			}
			allowed, err := t.app.peopleProfileRead(ctx, person, nil)
			if err != nil {
				return err
			}
			if !allowed {
				return errors.New("the person's profile could not be read")
			}
			target.Title = person.DisplayName
		case "brief_sentence":
			id, i, ok := workParseBriefSentenceID(target.ID)
			if !ok {
				return errTodayChatItemUnavailable
			}
			brief, err := t.app.correctionBrief(ctx, id)
			if err != nil {
				return err
			}
			if i >= len(brief.Narrative) {
				return errTodayChatItemUnavailable
			}
			before := strings.TrimSpace(brief.Narrative[i].Text)
			ref := TodayChatContextRef{SourceKind: "brief", SourceID: mobileBriefConversationKey(brief), BriefID: id, ParagraphDigest: mobileBriefModelDigest(before)}
			if t.ref != nil && attached != nil && t.ref.ParagraphDigest != ref.ParagraphDigest {
				return errTodayChatItemUnavailable
			}
			if _, _, err := t.app.loadTodayChatContextItem(ctx, ref); err != nil {
				return err
			}
			target.Title = answersBound(before)
			p.Applied = "not_right"
			return t.applyWork(ctx, p, WorkAnswerInput{Action: workActionNo, Before: before, After: p.Statement})
		case "work_item":
			list, err := t.app.buildWorkList(ctx, time.Now())
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			item, ok := workFind(list, target.ID)
			if !ok {
				return errors.New("the work item could not be read")
			}
			target.Title = item.Work.Title
			if item.Source == workSourceBrief && item.Yes.Does == "agent" {
				send := false
				p.Applied = "edit"
				return t.applyWork(ctx, p, WorkAnswerInput{Action: workActionEdit, Before: chatCorrectionBefore(item), After: p.Statement, Send: &send})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.About != nil && t.app.IsClosedEyesModeEnabled() {
		return errTodayChatObservationPaused
	}
	note := chatCorrectionNote(p.Statement)
	if target := p.About; target != nil {
		note = renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": extractTitle(note), "source_ids": []string{"chat-" + target.Kind + ":" + target.ID}}, p.Statement)
	}
	saved, err := t.app.SaveMemoryEntry("", note)
	if err != nil {
		return err
	}
	p.MemoryID = saved.ID
	if p.About != nil && p.About.Kind == "person" {
		t.app.touchDerivedViews(derivedPersonDetail)
	}
	return nil
}

func (t *chatCorrectionTool) applyWork(ctx context.Context, p *chatCorrectionPayload, input WorkAnswerInput) error {
	p.EntryID = uuid.NewString()
	_, err := t.app.answerWork(ctx, p.About.ID, input, workAnswerMeta{Surface: t.surface, correctionEntry: p.EntryID})
	return err
}

func (a *App) reverseChatCorrection(ctx context.Context, p chatCorrectionPayload, surface string) error {
	switch p.Applied {
	case "", "note":
		if !strings.HasPrefix(p.MemoryID, "memory-") {
			return errors.New("invalid correction note")
		}
		err := a.DeleteMemoryEntry(p.MemoryID)
		if err == nil && p.About != nil && p.About.Kind == "person" {
			a.touchDerivedViews(derivedPersonDetail)
		}
		return err
	case "edit", "not_right":
		if p.About == nil || p.EntryID == "" {
			return errors.New("invalid correction entry")
		}
		_, err := a.answerWork(ctx, p.About.ID, WorkAnswerInput{Action: workActionUndo}, workAnswerMeta{Surface: surface, expectedEntry: p.EntryID})
		return err
	default:
		return errors.New("unknown correction action")
	}
}
