package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"wails-gui/storage"
)

// See them (computer-data v1.1.0): what the assistant was given this week,
// from the call record (assistant_call_record.go). The record holds kinds,
// ids and times only; the words shown here (a question, a conversation's
// name, a proposal's title) are read now from their own stores, so an
// entry whose source is gone is not shown.

// assistantGivenStretchGap is the longest quiet between two readings of
// the day that still makes them one stretch of work.
const assistantGivenStretchGap = 30 * time.Minute

// AssistantGivenSource is one kind of source with how many of it.
type AssistantGivenSource struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// AssistantGivenStretch is one stretch of work the assistant read.
type AssistantGivenStretch struct {
	From  int64 `json:"from"`
	To    int64 `json:"to"`
	Times int   `json:"times"`
	// Screen is whether text on the screen was read; EveryMinutes is about
	// how often, over the stretch.
	Screen       bool `json:"screen"`
	EveryMinutes int  `json:"everyMinutes"`
	WeChat       int  `json:"wechat"`
	Calls        int  `json:"calls"`
	Messages     int  `json:"messages"`
}

// AssistantGivenWatch is one watch conversation read.
type AssistantGivenWatch struct {
	From int64  `json:"from"`
	To   int64  `json:"to"`
	Name string `json:"name,omitempty"`
}

// AssistantGivenBrief is one brief and what it was written from.
type AssistantGivenBrief struct {
	BriefID string                 `json:"briefId"`
	At      int64                  `json:"at"`
	Sources []AssistantGivenSource `json:"sources"`
}

// AssistantGivenAnswer is one answer in Chat and what it read. Removable is
// false for an answer asked outside Chat's turns, which names no item.
type AssistantGivenAnswer struct {
	ItemID    string                 `json:"itemId,omitempty"`
	At        int64                  `json:"at"`
	Question  string                 `json:"question,omitempty"`
	Sources   []AssistantGivenSource `json:"sources"`
	Removable bool                   `json:"removable"`
}

// AssistantGivenProposal is one proposal written from a topic.
type AssistantGivenProposal struct {
	ProposalID string `json:"proposalId"`
	At         int64  `json:"at"`
	Title      string `json:"title,omitempty"`
}

// AssistantGivenHour is one hour of a background pass's calls in a group
// (design T10): "10 AM · Learning about you · 3 times". Removable is
// whether it names what it read, so Forget on any of it removes the row;
// one that doesn't is counted by the hour (see.byHour).
type AssistantGivenHour struct {
	Group     string                 `json:"group"`
	From      int64                  `json:"from"`
	To        int64                  `json:"to"`
	Times     int                    `json:"times"`
	Made      int                    `json:"made,omitempty"`
	Sources   []AssistantGivenSource `json:"sources"`
	Removable bool                   `json:"removable"`
}

// AssistantGivenPass is one summary-reading pass for the week (design T14):
// "{name} · {n} times · From: summaries Isoform already wrote".
type AssistantGivenPass struct {
	Pass  string `json:"pass"`
	Times int    `json:"times"`
}

// AssistantGivenView is the sheet. Sense, About and Work hold the
// background passes by hour; Held counts those with no group yet, which the
// sheet doesn't show.
type AssistantGivenView struct {
	Stretches []AssistantGivenStretch  `json:"stretches"`
	Watch     []AssistantGivenWatch    `json:"watch"`
	Sense     []AssistantGivenHour     `json:"sense"`
	About     []AssistantGivenHour     `json:"about"`
	Work      []AssistantGivenHour     `json:"work"`
	Briefs    []AssistantGivenBrief    `json:"briefs"`
	Answers   []AssistantGivenAnswer   `json:"answers"`
	Proposals []AssistantGivenProposal `json:"proposals"`
	// Passes are the summary-reading passes, in Learning about you after its
	// hours, in learningPasses order.
	Passes []AssistantGivenPass `json:"passes"`
	Held   int                  `json:"held"`
}

// GetAssistantGiven returns this week's See them. A failed read is an
// error, never an empty sheet.
func (a *App) GetAssistantGiven() (perfValue AssistantGivenView, perfErr error) {
	defer logPageBinding("GetAssistantGiven", time.Now(), &perfValue)
	if a == nil || a.store == nil {
		return AssistantGivenView{}, errors.New("the store is not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	assistantCallWrites.Wait()
	calls, err := a.store.ListAssistantCalls(ctx, time.Now().Add(-yourDataWeek).UnixMilli())
	if err != nil {
		return AssistantGivenView{}, err
	}
	view := assistantGivenFrom(calls)
	if err := a.resolveAssistantGiven(ctx, &view); err != nil {
		return AssistantGivenView{}, err
	}
	return view, nil
}

// assistantGivenFrom groups the record's entries into the sheet.
func assistantGivenFrom(calls []storage.AssistantCall) AssistantGivenView {
	view := AssistantGivenView{Stretches: []AssistantGivenStretch{}, Watch: []AssistantGivenWatch{}, Briefs: []AssistantGivenBrief{},
		Answers: []AssistantGivenAnswer{}, Proposals: []AssistantGivenProposal{},
		Sense: []AssistantGivenHour{}, About: []AssistantGivenHour{}, Work: []AssistantGivenHour{}, Passes: []AssistantGivenPass{}}
	passTimes := map[string]int{}
	var day []storage.AssistantCall
	for _, c := range calls {
		switch c.Purpose {
		case storage.AssistantCallDay:
			day = append(day, c)
		case storage.AssistantCallBrief:
			view.Briefs = append(view.Briefs, AssistantGivenBrief{BriefID: c.Ref, At: c.At, Sources: assistantGivenKinds(c.Sources)})
		case storage.AssistantCallAnswer:
			view.Answers = append(view.Answers, AssistantGivenAnswer{ItemID: c.Ref, At: c.At, Sources: assistantGivenKinds(c.Sources),
				Removable: c.Ref != "" || assistantGivenNamesItems(c.Sources)})
		case storage.AssistantCallProposal:
			view.Proposals = append(view.Proposals, AssistantGivenProposal{ProposalID: c.Ref, At: c.At})
		case storage.AssistantCallLearning:
			if pass := learningPassOf(c.Ref); pass != "" {
				passTimes[pass] += c.Times
				continue
			}
			from, to := c.SpanStart, c.SpanEnd
			if from == 0 {
				from = c.At
			}
			hour := AssistantGivenHour{Group: learningGroupOf(c.Ref), From: from, To: to, Times: c.Times, Made: c.Made,
				Sources: assistantGivenKinds(assistantGivenNamed(c.Sources)), Removable: assistantGivenNamesItems(c.Sources)}
			switch hour.Group {
			case learningGroupSense:
				view.Sense = append(view.Sense, hour)
			case learningGroupAbout:
				view.About = append(view.About, hour)
			case learningGroupWork:
				view.Work = append(view.Work, hour)
			default:
				view.Held += c.Times
			}
		}
	}
	for _, pass := range learningPasses {
		if n := passTimes[pass]; n > 0 {
			view.Passes = append(view.Passes, AssistantGivenPass{Pass: pass, Times: n})
		}
	}
	view.Stretches, view.Watch = assistantGivenDay(day)
	return view
}

// assistantGivenNamesItems reports whether an entry names at least one
// item it read: then forgetting that item removes it.
func assistantGivenNamesItems(sources []storage.AssistantCallSource) bool {
	for _, s := range sources {
		if s.Ref != "" {
			return true
		}
	}
	return false
}

// assistantGivenNamed keeps the sources that name an item; a kind-only
// marker says "what Isoform saw that hour" instead.
func assistantGivenNamed(sources []storage.AssistantCallSource) []storage.AssistantCallSource {
	var out []storage.AssistantCallSource
	for _, s := range sources {
		if s.Ref != "" {
			out = append(out, s)
		}
	}
	return out
}

// assistantGivenKinds sums an entry's sources by kind.
func assistantGivenKinds(sources []storage.AssistantCallSource) []AssistantGivenSource {
	counts := map[string]int{}
	var order []string
	for _, s := range sources {
		if counts[s.Kind] == 0 {
			order = append(order, s.Kind)
		}
		counts[s.Kind] += s.Count
	}
	sort.Strings(order)
	out := make([]AssistantGivenSource, 0, len(order))
	for _, kind := range order {
		out = append(out, AssistantGivenSource{Kind: kind, Count: counts[kind]})
	}
	return out
}

// assistantGivenDay makes the day's entries into stretches of work (on one
// local date, with no more than assistantGivenStretchGap between them) and
// watch conversations, newest first.
func assistantGivenDay(day []storage.AssistantCall) ([]AssistantGivenStretch, []AssistantGivenWatch) {
	stretches := []AssistantGivenStretch{}
	watch := []AssistantGivenWatch{}
	span := func(c storage.AssistantCall) (int64, int64) {
		from, to := c.SpanStart, c.SpanEnd
		if from == 0 {
			from = c.At
		}
		if to < from {
			to = from
		}
		return from, to
	}
	sort.Slice(day, func(i, j int) bool {
		fi, _ := span(day[i])
		fj, _ := span(day[j])
		return fi < fj
	})
	var cur *AssistantGivenStretch
	var screenTimes int
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Screen && screenTimes > 0 {
			cur.EveryMinutes = max(1, int(time.Duration(cur.To-cur.From)*time.Millisecond/time.Minute)/screenTimes)
		}
		stretches = append(stretches, *cur)
		cur, screenTimes = nil, 0
	}
	for _, c := range day {
		from, to := span(c)
		var screen, wechat, calls, messages, watchHours int
		for _, s := range c.Sources {
			switch s.Kind {
			case "screen":
				screen += s.Count
			case "wechat":
				wechat += s.Count
			case "call":
				calls += s.Count
			case "messages":
				messages += s.Count
			case "watch":
				watchHours += s.Count
			}
		}
		if watchHours > 0 && screen == 0 && calls == 0 && wechat == 0 && messages == 0 {
			watch = append(watch, AssistantGivenWatch{From: from, To: to})
			continue
		}
		sameDay := cur != nil && time.UnixMilli(cur.From).Local().Format("2006-01-02") == time.UnixMilli(from).Local().Format("2006-01-02")
		if cur == nil || !sameDay || time.Duration(from-cur.To)*time.Millisecond > assistantGivenStretchGap {
			flush()
			cur = &AssistantGivenStretch{From: from, To: to}
		}
		if to > cur.To {
			cur.To = to
		}
		cur.Times += c.Times
		if screen > 0 {
			cur.Screen = true
			screenTimes += c.Times
		}
		cur.WeChat += wechat
		cur.Calls += calls
		cur.Messages += messages
	}
	flush()
	sort.Slice(stretches, func(i, j int) bool { return stretches[i].From > stretches[j].From })
	sort.Slice(watch, func(i, j int) bool { return watch[i].From > watch[j].From })
	return stretches, watch
}

// resolveAssistantGiven reads, now, the words each entry shows: a
// question's text and a proposal's title. An answer whose question was
// deleted is dropped, and its entry with it; a read that fails is an error.
func (a *App) resolveAssistantGiven(ctx context.Context, view *AssistantGivenView) error {
	answers := view.Answers[:0]
	for _, ans := range view.Answers {
		if ans.ItemID != "" {
			item, err := a.store.LoadStreamItem(ctx, ans.ItemID)
			if err != nil {
				return err
			}
			if item == nil {
				a.forgetAssistantCall(storage.AssistantCallAnswer, ans.ItemID)
				continue
			}
			ans.Question = item.Content
		}
		answers = append(answers, ans)
	}
	view.Answers = answers
	if len(view.Proposals) == 0 {
		return nil
	}
	titles := map[string]string{}
	if store, err := a.topicsDailyStore(); err == nil {
		if saved, ok := store.Load(); ok {
			for _, t := range saved.Topics {
				if t.Proposal != nil {
					titles[t.Proposal.ID] = t.Proposal.Title
				}
			}
		}
	}
	for i := range view.Proposals {
		view.Proposals[i].Title = titles[view.Proposals[i].ProposalID]
	}
	return nil
}
