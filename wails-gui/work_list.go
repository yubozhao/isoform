package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/topicday"
	"wails-gui/internal/workonit"
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
	BriefID string `json:"briefId,omitempty"`
	// Paragraph is the brief paragraph the item sits under (zero-based), when
	// the brief cites what the item is about.
	Paragraph      *int   `json:"paragraph,omitempty"`
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
// what the part opens. Key names the fixed phrase, so a client can word it
// in its own language; at is a time. The snapshot never holds day or clock
// words: a part with key "when" or "clock" gets its text at read time for the
// reader's now (workListWordedAt), and a client may word its at instead.
type WorkTextPart struct {
	Text string `json:"text"`
	Ref  string `json:"ref,omitempty"`
	Key  string `json:"key,omitempty"`
	At   string `json:"at,omitempty"`
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
	// Kind is code or else: which of the person's agents takes the work
	// (phone-agent v1.1.0, B4). Set on work a Yes hands to an agent.
	Kind string `json:"kind,omitempty"`
}

// workCodeApps are the apps whose work is code: an editor or a terminal.
var workCodeApps = []string{"xcode", "code", "cursor", "terminal", "iterm", "ghostty", "warp", "goland", "intellij",
	"pycharm", "webstorm", "android studio", "zed", "sublime", "nova"}

// workTopicIsCode says whether a topic's work is code: it was seen in a code
// tool (phone-agent v1.1.0 §4: the work's kind decides which agent takes it).
func workTopicIsCode(apps []string) bool {
	for _, app := range apps {
		name := strings.ToLower(app)
		for _, tool := range workCodeApps {
			if strings.Contains(name, tool) {
				return true
			}
		}
	}
	return false
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
	// NotSent marks a Yes kept for the hand-off sheet: nothing went yet.
	NotSent bool `json:"notSent,omitempty"`
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
	workSourcePrep  = "prep"
	workSourceAgent = "agent"

	// workResultsPerDay bounds the finished tasks Your work shows that no
	// item of its own started.
	workResultsPerDay = 3

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
	list, err := a.workListNow()
	if err == nil {
		// The computer's Today put these items in front of the person.
		a.noteWorkShown(list, workSurfaceComputer)
	}
	return list, err
}

// workListNow is Your work as a reader sees it now, for the computer's own
// uses (Today's version, a notice's item); it records nothing.
func (a *App) workListNow() (WorkList, error) {
	list, err := derivedRead(a, derivedWorkList, "", func() (WorkList, error) { return a.buildWorkList(context.Background(), time.Now()) }, nil)
	if err != nil {
		return list, err
	}
	return workListWordedAt(list, time.Now()), nil
}

// workListWordedAt fills the text of each day and clock part for the
// reader's now, at read time, so a snapshot built yesterday never says
// "tomorrow" about today (the 11-15 day drift). The snapshot itself holds no
// day words; a client may word the part from its at instead.
func workListWordedAt(list WorkList, now time.Time) WorkList {
	items := make([]WorkItem, len(list.Items))
	for i, item := range list.Items {
		parts := make([]WorkTextPart, len(item.First))
		for j, part := range item.First {
			if at, err := time.Parse(time.RFC3339, part.At); err == nil {
				switch part.Key {
				case "when":
					part.Text = workDayWord(at, now)
				case "clock":
					part.Text = at.In(time.Local).Format("3:04 PM")
				}
			}
			parts[j] = part
		}
		item.First = parts
		items[i] = item
	}
	list.Items = items
	return list
}

// workDayWord says when a time falls for the reader: today, tomorrow,
// yesterday, else the weekday or the date.
func workDayWord(at, now time.Time) string {
	local, today := at.In(time.Local), now.In(time.Local)
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }
	switch int(day(local).Sub(day(today)).Hours() / 24) {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	case -1:
		return "yesterday"
	}
	if diff := day(local).Sub(day(today)); diff > 0 && diff < 7*24*time.Hour {
		return local.Format("Monday")
	}
	return local.Format("Jan 2")
}

// buildWorkList reads every source once and orders the list: the brief's
// items in the brief's order, then the rest newest first, a result back on
// top of the rest, at most workOpenPerDay open.
func (a *App) buildWorkList(ctx context.Context, now time.Time) (WorkList, error) {
	list := WorkList{V: 1, GeneratedAt: mobileTimestamp(now), Items: []WorkItem{}}
	brief := a.applyWorkAnswers(a.workBriefItems(ctx))
	rest := a.applyWorkAnswers(append(append(a.workTopicItems(ctx), a.workPrepItems(now)...), a.workResultItems(now, brief)...))
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
	for i := range list.Items {
		if list.Items[i].Yes.Does == "agent" && list.Items[i].Yes.Kind == "" {
			list.Items[i].Yes.Kind = workonit.KindElse
		}
	}
	a.workMarkNotRight(list.Items)
	list.Version = workListVersion(list)
	return list, nil
}

// workListVersion changes exactly when what the list shows changes; the time
// it was built is left out.
func workListVersion(list WorkList) string {
	day := ""
	if at, err := time.Parse(time.RFC3339, list.GeneratedAt); err == nil {
		day = at.In(time.Local).Format("2006-01-02")
	}
	raw, _ := json.Marshal(struct {
		Items     []WorkItem `json:"items"`
		UndoUntil string     `json:"undoUntil"`
		Day       string     `json:"day"`
	}{list.Items, list.UndoUntil, day})
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
		BriefID   string             `json:"briefId"`
		AsOf      string             `json:"asOf"`
		Offers    []MobileBriefOffer `json:"offers"`
		Narrative []MobileBriefPara  `json:"narrative"`
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
			Source: workSourceBrief, Ref: WorkRef{BriefID: phone.BriefID, OfferID: offer.ID, Paragraph: workOfferParagraph(phone.Narrative, offer.Refs)},
			First:   []WorkTextPart{{Text: offer.Label}, {Text: "from the brief", Ref: "brief:" + phone.BriefID, Key: "from_brief"}},
			Noticed: noticed, Work: WorkBody{Title: offer.Label},
			Yes:     WorkYes{Does: "agent", Line: workYesAgentLine},
			Reasons: workReasonsOffered, CreatedAt: phone.AsOf,
		})
	}
	return items
}

// workOfferParagraph is the first paragraph that cites one of the offer's
// sources, or nil.
func workOfferParagraph(narrative []MobileBriefPara, refs []string) *int {
	cited := map[string]bool{}
	for _, ref := range refs {
		cited[ref] = true
	}
	for i, para := range narrative {
		for _, ref := range para.Refs {
			if cited[ref] {
				index := i
				return &index
			}
		}
	}
	return nil
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
	// Each topic's apps (code or not) and when its proposal was answered,
	// for "with your agent since".
	apps := map[string][]string{}
	answeredAt := map[string]int64{}
	if store, err := a.topicsDailyStore(); err == nil {
		if saved, ok := store.Load(); ok {
			for _, topic := range saved.Topics {
				apps[topic.ID] = topic.Apps
				if topic.Proposal != nil {
					answeredAt[topic.Proposal.ID] = topic.Proposal.AnsweredAt
				}
			}
		}
	}
	items := []WorkItem{}
	for _, topic := range view.Topics {
		p := topic.Proposal
		if p == nil || p.ID == "" || p.Title == "" {
			continue
		}
		kind, kindKey := "a subject", "kind_subject"
		if topic.Kind == "project" {
			kind, kindKey = "a project", "kind_project"
		}
		item := WorkItem{
			ID: workTopicProposalID(topic.ID, p.ID), Kind: workKindOffered, Row: "offered", State: workStateOpen,
			Source: workSourceTopic, Ref: WorkRef{TopicID: topic.ID, ProposalID: p.ID, TaskID: p.TaskID},
			First:   []WorkTextPart{{Text: topic.Name, Ref: "topic:" + topic.ID}, {Text: kind, Key: kindKey}, {Text: "from your topics", Key: "from_topics"}},
			Noticed: p.Why, Work: WorkBody{Title: p.Title},
			Yes:     WorkYes{Does: "agent", Line: workYesAgentLine},
			Reasons: workReasonsOffered, CreatedAt: created,
		}
		if workTopicIsCode(apps[topic.ID]) {
			item.Yes.Kind = workonit.KindCode
		}
		if item.Noticed == "" {
			item.Noticed = p.Title
		}
		switch p.State {
		case "running":
			item.State = workStateWithAgent
			since := created
			if at := answeredAt[p.ID]; at > 0 {
				since = mobileTimestamp(time.UnixMilli(at))
			}
			item.Answer = &WorkAnswer{Action: "yes", At: since, Line: p.Title + ": with your agent"}
			item.Agent = &WorkAgent{Name: "your agent", Since: since}
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

// workPrepItems is the next meeting's prep, as Today's Before you talk has it
// (phone-today rule 8): kept with the meeting on a Yes, Not needed on a No.
func (a *App) workPrepItems(now time.Time) []WorkItem {
	prep, err := a.GetTodayBeforeYouTalk()
	if err != nil || prep == nil || prep.MeetingID == "" || strings.TrimSpace(prep.PersonName) == "" {
		return nil
	}
	name := strings.TrimSpace(prep.PersonName)
	lines := []string{}
	if asked := strings.TrimSpace(prep.TheyAsked); asked != "" {
		lines = append(lines, name+" asked: "+asked)
	}
	if owe := strings.TrimSpace(prep.YouOwe); owe != "" {
		lines = append(lines, "You owe "+name+": "+owe)
	}
	if last := strings.TrimSpace(prep.LastTime); last != "" {
		lines = append(lines, "Last time: "+last)
	}
	if len(lines) == 0 {
		return nil
	}
	start := time.UnixMilli(prep.StartAt).In(time.Local)
	opens := start.Add(-5 * time.Minute)
	return []WorkItem{{
		ID: workPrepID(prep.MeetingID), Kind: workKindDone, Row: "prep", State: workStateOpen, Source: workSourcePrep,
		Ref: WorkRef{EventID: prep.MeetingID},
		First: []WorkTextPart{{Text: "For your meeting with " + name, Key: "for_meeting", At: mobileTimestamp(start)},
			{Key: "when", At: mobileTimestamp(start)}},
		Noticed: lines[0], Work: WorkBody{Title: "Prep for " + name, Text: strings.Join(lines, "\n")},
		Yes:     WorkYes{Does: "keep", At: mobileTimestamp(opens), Line: "Yes keeps it with the meeting."},
		Reasons: workReasonsDone, CreatedAt: mobileTimestamp(start.Add(-time.Hour)),
	}}
}

func workPrepID(meetingID string) string { return "prep:" + meetingID }

// workResultItems are tasks that finished today without an item of their own
// (a brief's offer or a topic's proposal holds its own result): the result
// back from the agent, to be judged Useful or Not useful.
func (a *App) workResultItems(now time.Time, held []WorkItem) []WorkItem {
	if a.handoffStore == nil {
		return nil
	}
	owned := map[string]bool{}
	for _, item := range held {
		if item.Ref.TaskID != "" {
			owned[item.Ref.TaskID] = true
		}
	}
	for _, record := range a.answersLedger().current() {
		if record.TaskID != "" {
			owned[record.TaskID] = true
		}
	}
	entries, err := a.handoffStore.List()
	if err != nil {
		return nil
	}
	day := now.In(time.Local).Format("2006-01-02")
	items := []WorkItem{}
	for _, entry := range entries {
		if owned[entry.ID] || entry.Thread != nil && entry.Thread.Origin != nil && (entry.Thread.Origin.Kind == topicsDailyTaskOrigin || entry.Thread.Origin.Kind == workBriefTaskOrigin) {
			continue
		}
		state, result := dailyTopicTaskState(entry)
		if state != topicday.StateDone {
			continue
		}
		finished := time.UnixMilli(entry.UpdatedAt).In(time.Local)
		title := strings.TrimSpace(entry.Title)
		if finished.Format("2006-01-02") != day || title == "" {
			continue
		}
		items = append(items, WorkItem{
			ID: workResultID(entry.ID), Kind: workKindResult, Row: "result", State: workStateBack, Source: workSourceAgent,
			Ref:     WorkRef{HandoffID: entry.ID, TaskID: entry.ID},
			First:   []WorkTextPart{{Text: "Back from your agent", Key: "back_from_agent"}, {Key: "clock", At: mobileTimestamp(finished)}},
			Noticed: title + " is ready.", Work: WorkBody{Title: title, Text: result},
			Yes:     WorkYes{Does: "open", Line: "Was it useful?"},
			Reasons: []string{}, CreatedAt: mobileTimestamp(finished),
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	if len(items) > workResultsPerDay {
		items = items[:workResultsPerDay]
	}
	return items
}

func workResultID(handoffID string) string { return "result:" + handoffID }
