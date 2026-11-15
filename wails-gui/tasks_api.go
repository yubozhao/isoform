package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
)

type CreateTaskInput struct {
	Title   string `json:"title"`
	Brief   string `json:"brief"`
	WorkDir string `json:"workDir"`
}

var errTaskStoreNotReady = errors.New("the handoff store is not ready")

func validateCreateTask(input CreateTaskInput) (CreateTaskInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len(input.Title) > mobileHandoffTitleMax {
		return input, errors.New("the title must be 1 to 200 bytes")
	}
	if len(input.Brief) > mobileChatBriefMax {
		return input, errors.New("the brief must be at most 8 KiB")
	}
	input.WorkDir = strings.TrimSpace(input.WorkDir)
	if input.WorkDir != "" {
		info, err := os.Stat(input.WorkDir)
		if !filepath.IsAbs(input.WorkDir) || err != nil || !info.IsDir() {
			return input, errors.New("the work folder must be an absolute folder on this computer")
		}
		input.WorkDir = filepath.Clean(input.WorkDir)
	}
	input.Brief = strings.TrimSpace(input.Brief)
	return input, nil
}

func (a *App) createTaskEntry(input CreateTaskInput, source handoff.Source) (handoff.Entry, error) {
	return a.createTaskEntryFrom(input, source, nil)
}

// createTaskEntryFrom saves new work with where it came from, which opens its
// thread ("From Up next · you tapped Do it at 9:02 AM").
func (a *App) createTaskEntryFrom(input CreateTaskInput, source handoff.Source, origin *handoff.Origin) (handoff.Entry, error) {
	if a == nil || a.handoffStore == nil {
		return handoff.Entry{}, errTaskStoreNotReady
	}
	now := time.Now().UnixMilli()
	id := "hf_" + uuid.NewString()
	entry := newTaskEntryFrom(input, source, origin, id, now)
	saved, err := a.handoffStore.Save(entry)
	if err != nil {
		return handoff.Entry{}, err
	}
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": saved.ID, "status": string(saved.Status)})
	a.emitBrowserEvent("handoff:created", map[string]any{"id": saved.ID, "title": saved.Title})
	return saved, nil
}

// newTaskEntryFrom is shared by phone handoffs and staged list batches.
func newTaskEntryFrom(input CreateTaskInput, source handoff.Source, origin *handoff.Origin, id string, now int64) handoff.Entry {
	entry := handoff.Entry{
		ID: id, Title: input.Title, Status: handoff.StatusActive, Source: source,
		SourceID: id, Workdir: input.WorkDir, Body: input.Brief, CreatedAt: now, UpdatedAt: now,
	}
	if origin != nil {
		stamped := *origin
		stamped.At = now
		entry.Thread = &handoff.Thread{Origin: &stamped, OpenedAt: now}
	}
	return entry
}

func (a *App) CreateTask(input CreateTaskInput) (HandoffEntryView, error) {
	input, err := validateCreateTask(input)
	if err != nil {
		return HandoffEntryView{}, err
	}
	saved, err := a.createTaskEntry(input, handoff.SourceDesktop)
	if err != nil {
		return HandoffEntryView{}, err
	}
	return a.productHandoffEntryView(saved), nil
}

type TaskSuggestionView struct {
	ID           string               `json:"id"`
	Kind         string               `json:"kind"`
	Title        string               `json:"title"`
	Why          string               `json:"why"`
	WhyLocalized map[string]string    `json:"whyLocalized,omitempty"`
	Prompt       string               `json:"prompt"`
	Version      string               `json:"version"`
	DeckKind     string               `json:"deckKind,omitempty"`
	Evidence     *mobileOfferEvidence `json:"evidence,omitempty"`
}

func taskSuggestionView(offer mobileOffer) TaskSuggestionView {
	return TaskSuggestionView{ID: offer.ID, Kind: offer.Kind, Title: offer.Title,
		Why: offer.Why, WhyLocalized: offer.WhyLocalized, Prompt: offer.Prompt, Version: offer.Version,
		DeckKind: offer.DeckKind, Evidence: offer.Evidence}
}

func (a *App) ListTaskSuggestions() (perfValue []TaskSuggestionView, perfErr error) {
	defer logPageBinding("ListTaskSuggestions", time.Now(), &perfValue)
	result := []TaskSuggestionView{}
	view, err := a.mobileOffers(context.Background())
	if errors.Is(err, errMobileStoreUnavailable) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, offer := range view.Offers {
		if offer.Kind == "suggestion" {
			continue
		}
		result = append(result, taskSuggestionView(offer))
	}
	return result, nil
}

var errTaskSuggestionNotFound = errors.New("this idea is no longer on your computer")

func (a *App) decideTaskSuggestion(ctx context.Context, id, decision, reason string, channel engagement.Channel) error {
	return a.decideTaskSuggestionWithHolds(ctx, id, decision, reason, channel, false)
}

func (a *App) decideTaskSuggestionWithHolds(ctx context.Context, id, decision, reason string, channel engagement.Channel, holds bool) error {
	view, err := a.mobileOffersWithHolds(ctx, holds)
	if err != nil {
		return err
	}
	for _, offer := range view.Offers {
		// Legacy suggestions are retired from desktop Tasks, but remain visible
		// and dismissible in the phone's Ideas list.
		if offer.ID != id || (offer.Kind == "suggestion" && channel != engagement.ChannelPush) {
			continue
		}
		switch offer.Kind {
		case "suggestion":
			_, err = a.resolveTodaySuggestionOn(TodaySuggestionDecision{ID: strings.TrimPrefix(id, "suggestion."), Decision: decision, Reason: reason}, channel)
		case "topic_find":
			err = a.recordTopicFindFeedbackOn(TopicFindFeedbackInput{FindID: strings.TrimPrefix(id, "find."), Action: "less"}, channel)
			if err == nil {
				a.emitBrowserEvent("topics:finds")
			}
		default:
			return fmt.Errorf("unsupported idea kind %q", offer.Kind)
		}
		if err == nil {
			noteProposalAnswered(judgmentOffer, id, false)
		}
		return err
	}
	return errTaskSuggestionNotFound
}

func (a *App) DecideTaskSuggestion(id, decision string) error {
	if !mobileValidID(id) {
		return errors.New("the idea id is not valid")
	}
	if decision != "dismiss" && decision != "not_now" {
		return errors.New("decision must be dismiss or not_now")
	}
	mobileOfferDecisionMu.Lock()
	defer mobileOfferDecisionMu.Unlock()
	return a.decideTaskSuggestion(context.Background(), id, decision, "Not for me in Tasks on this computer.", engagement.ChannelDesktop)
}
