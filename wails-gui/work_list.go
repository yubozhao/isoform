package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Your work: the one list of what Isoform puts to the person, the same on the
// computer's Today (C6) and the phone's (B2), with the same ids, states and
// answers (phone-map v1.0.0 section 7; PROTOCOL.md section 26). It is a
// derived view: built in the background, read as a snapshot, rebuilt when a
// source changes or an answer is given.
type WorkList struct {
	V              int        `json:"v"`
	Version        string     `json:"version"`
	GeneratedAt    string     `json:"generatedAt"`
	ToAnswer       int        `json:"toAnswer"`
	BackFromAgents int        `json:"backFromAgents"`
	UndoUntil      string     `json:"undoUntil,omitempty"`
	Items          []WorkItem `json:"items"`
}

// WorkItem is one thing put to the person. Its words are composed here, once,
// so every device says the same thing.
type WorkItem struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Row       string         `json:"row"`
	State     string         `json:"state"`
	Source    string         `json:"source"`
	Ref       WorkRef        `json:"ref"`
	First     []WorkTextPart `json:"first"`
	Noticed   string         `json:"noticed"`
	Work      WorkBody       `json:"work"`
	Yes       WorkYes        `json:"yes"`
	Why       []WorkLink     `json:"why,omitempty"`
	Gets      []WorkGets     `json:"gets,omitempty"`
	Reasons   []string       `json:"reasons"`
	Answer    *WorkAnswer    `json:"answer,omitempty"`
	Agent     *WorkAgent     `json:"agent,omitempty"`
	CreatedAt string         `json:"createdAt"`
	Order     int            `json:"order"`
}

// WorkRef names the item's source records.
type WorkRef struct {
	BriefID        string `json:"briefId,omitempty"`
	ItemKey        string `json:"itemKey,omitempty"`
	OfferID        string `json:"offerId,omitempty"`
	TopicID        string `json:"topicId,omitempty"`
	ProposalID     string `json:"proposalId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	SaidAt         string `json:"saidAt,omitempty"`
	EventID        string `json:"eventId,omitempty"`
	HandoffID      string `json:"handoffId,omitempty"`
	JobID          string `json:"jobId,omitempty"`
	PassID         string `json:"passId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
}

// WorkTextPart is one part of the item's first line; ref, when set, names
// what the part opens.
type WorkTextPart struct {
	Text string `json:"text"`
	Ref  string `json:"ref,omitempty"`
}

type WorkBody struct {
	Title   string `json:"title"`
	Text    string `json:"text,omitempty"`
	Minutes int    `json:"minutes,omitempty"`
}

// WorkYes says what Yes does: keep (with a meeting), mail_draft, agent, right
// (a claim) or open.
type WorkYes struct {
	Does  string `json:"does"`
	Line  string `json:"line"`
	At    string `json:"at,omitempty"`
	Agent string `json:"agent,omitempty"`
}

type WorkLink struct {
	Label string `json:"label"`
	Ref   string `json:"ref,omitempty"`
}

type WorkGets struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Ref       string `json:"ref,omitempty"`
	Removable bool   `json:"removable"`
}

// WorkAnswer is the person's answer on an item.
type WorkAnswer struct {
	Action    string      `json:"action"`
	Reason    string      `json:"reason,omitempty"`
	Edited    *WorkEdited `json:"edited,omitempty"`
	At        string      `json:"at"`
	Surface   string      `json:"surface"`
	Line      string      `json:"line,omitempty"`
	UndoUntil string      `json:"undoUntil,omitempty"`
}

type WorkEdited struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type WorkAgent struct {
	Name  string `json:"name"`
	Since string `json:"since,omitempty"`
}

const (
	workKindDone    = "done"
	workKindOffered = "offered"
	workKindClaim   = "claim"
	workKindResult  = "result"
	workKindCheck   = "check"

	workStateOpen      = "open"
	workStateAnswered  = "answered"
	workStateWithAgent = "with_agent"
	workStateBack      = "back"

	workSourceBrief = "brief"
	workSourceTopic = "topic"

	// workOpenPerDay is the most open items the list holds (redesign v1.0.0
	// rule 15).
	workOpenPerDay = 10

	workUpdatedEvent = "work:updated"
	// workAnsweredEvent is emitted when the person answers an item; it
	// rebuilds the list behind the read.
	workAnsweredEvent = "work:answered"
)

// The reasons a No may carry, by kind (phone-map rule 7).
var (
	workReasonsOffered = []string{"already_done", "not_now", "not_useful", "myself"}
	workReasonsDone    = []string{"not_needed", "wrong_facts", "not_how_id_say", "myself"}
	workReasonsClaim   = []string{"was_true_once", "only_sometimes", "not_at_all"}
)

// GetWorkList returns Your work from its snapshot.
func (a *App) GetWorkList() (perfValue WorkList, perfErr error) {
	defer logPageBinding("GetWorkList", time.Now(), &perfValue)
	return derivedRead(a, derivedWorkList, "", func() (WorkList, error) { return a.buildWorkList(context.Background(), time.Now()) }, nil)
}

// buildWorkList reads every source once and orders the list: the brief's
// items in the brief's order, then the rest newest first, a result back on
// top of the rest, at most workOpenPerDay open.
func (a *App) buildWorkList(ctx context.Context, now time.Time) (WorkList, error) {
	list := WorkList{V: 1, GeneratedAt: mobileTimestamp(now), Items: []WorkItem{}}
	brief := a.applyWorkAnswers(a.workBriefItems(ctx))
	rest := a.applyWorkAnswers(a.workTopicItems(ctx))
	sort.SliceStable(rest, func(i, j int) bool {
		if (rest[i].State == workStateBack) != (rest[j].State == workStateBack) {
			return rest[i].State == workStateBack
		}
		return rest[i].CreatedAt > rest[j].CreatedAt
	})
	open := 0
	for _, item := range append(brief, rest...) {
		if item.State == workStateOpen {
			if open >= workOpenPerDay {
				continue
			}
			open++
		}
		item.Order = len(list.Items)
		list.Items = append(list.Items, item)
	}
	for _, item := range list.Items {
		switch item.State {
		case workStateOpen:
			list.ToAnswer++
		case workStateBack:
			list.ToAnswer++
			list.BackFromAgents++
		}
	}
	if svc := a.mobileBriefSource(); svc != nil {
		if schedule, err := svc.Schedule(); err == nil {
			if slots := mobileBriefSlotTimes(now, time.Local, schedule); len(slots) > 0 {
				list.UndoUntil = slots[0].At
			}
		}
	}
	list.Version = workListVersion(list)
	return list, nil
}

// workListVersion changes exactly when what the list shows changes; the time
// it was built is left out.
func workListVersion(list WorkList) string {
	raw, _ := json.Marshal(struct {
		Items     []WorkItem `json:"items"`
		UndoUntil string     `json:"undoUntil"`
	}{list.Items, list.UndoUntil})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// workBriefItems are the current brief's offers, read from its phone package
// so both devices judge exactly what the phone shows.
func (a *App) workBriefItems(ctx context.Context) []WorkItem {
	svc := a.mobileBriefSource()
	if svc == nil {
		return nil
	}
	current, ok := currentMobileBrief(svc)
	if !ok {
		return nil
	}
	packager, ok := svc.(mobileBriefPackagerContext)
	if !ok {
		return nil
	}
	pkg, ok := packager.BriefPackageContext(ctx, current.BriefID)
	if !ok {
		return nil
	}
	var phone struct {
		BriefID string             `json:"briefId"`
		AsOf    string             `json:"asOf"`
		Offers  []MobileBriefOffer `json:"offers"`
	}
	if json.Unmarshal(pkg.Bytes, &phone) != nil || phone.BriefID != current.BriefID {
		return nil
	}
	items := make([]WorkItem, 0, len(phone.Offers))
	for _, offer := range phone.Offers {
		if offer.ID == "" || offer.Label == "" {
			continue
		}
		row := "offered"
		switch offer.Kind {
		case "prepare":
			row = "prep"
		case "reply":
			row = "draft"
		}
		noticed := offer.Text
		if noticed == "" {
			noticed = offer.Label
		}
		items = append(items, WorkItem{
			ID: workBriefOfferID(phone.BriefID, offer.ID), Kind: workKindOffered, Row: row, State: workStateOpen,
			Source: workSourceBrief, Ref: WorkRef{BriefID: phone.BriefID, OfferID: offer.ID},
			First:   []WorkTextPart{{Text: offer.Label}, {Text: "from the brief", Ref: "brief:" + phone.BriefID}},
			Noticed: noticed, Work: WorkBody{Title: offer.Label},
			Yes:     WorkYes{Does: "agent", Line: workYesAgentLine},
			Reasons: workReasonsOffered, CreatedAt: phone.AsOf,
		})
	}
	return items
}

// workYesAgentLine is the Yes line of offered work until the person's agent
// is a setting (B4): a Yes starts the work as a task and its result comes
// back to Your work.
const workYesAgentLine = "Yes starts it with your agent. What it makes comes back here."

func workBriefOfferID(briefID, offerID string) string {
	return "brief:" + briefID + ":offer:" + offerID
}

func workTopicProposalID(topicID, proposalID string) string {
	return "topic:" + topicID + ":" + proposalID
}

// workTopicItems are the daily topics' proposals, as the phone may see them,
// while Proposals from your topics is on.
func (a *App) workTopicItems(ctx context.Context) []WorkItem {
	view, err := a.dailyTopicsForPhone(ctx)
	if err != nil || !view.Enabled {
		return nil
	}
	created := ""
	if view.BuiltAt > 0 {
		created = mobileTimestamp(time.UnixMilli(view.BuiltAt))
	}
	items := []WorkItem{}
	for _, topic := range view.Topics {
		p := topic.Proposal
		if p == nil || p.ID == "" || p.Title == "" {
			continue
		}
		kind := "a subject"
		if topic.Kind == "project" {
			kind = "a project"
		}
		item := WorkItem{
			ID: workTopicProposalID(topic.ID, p.ID), Kind: workKindOffered, Row: "offered", State: workStateOpen,
			Source: workSourceTopic, Ref: WorkRef{TopicID: topic.ID, ProposalID: p.ID, TaskID: p.TaskID},
			First:   []WorkTextPart{{Text: topic.Name, Ref: "topic:" + topic.ID}, {Text: kind}, {Text: "from your topics"}},
			Noticed: p.Why, Work: WorkBody{Title: p.Title},
			Yes:     WorkYes{Does: "agent", Line: workYesAgentLine},
			Reasons: workReasonsOffered, CreatedAt: created,
		}
		if item.Noticed == "" {
			item.Noticed = p.Title
		}
		switch p.State {
		case "running":
			item.State = workStateWithAgent
			item.Answer = &WorkAnswer{Action: "yes", Line: p.Title + ": with your agent"}
			item.Agent = &WorkAgent{Name: "your agent"}
		case "done", "failed":
			item.State, item.Kind, item.Row = workStateBack, workKindResult, "result"
			item.Work.Text = p.Result
			item.Answer = &WorkAnswer{Action: "yes", Line: p.Title + ": back from your agent"}
		case "declined":
			item.State = workStateAnswered
			item.Answer = &WorkAnswer{Action: "no", Line: p.Title + ": not this"}
		case "not_mine":
			item.State = workStateAnswered
			item.Answer = &WorkAnswer{Action: "not_mine", Line: p.Title + ": not yours"}
		}
		items = append(items, item)
	}
	return items
}
