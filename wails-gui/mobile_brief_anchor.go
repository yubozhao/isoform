package main

import "strings"

// What may become a task, an attention row or a button on the brief (backlog
// item 56). An item is anchored when it has something real behind it: a
// meeting, a due date, a person waiting, a bill or order, a document,
// Isoform's own work, or a goal or commitment the user holds in the Purpose
// Map. Everything else was only seen in activity: what the user typed into
// Claude, Codex, ChatGPT or a terminal, and the apps they used. Activity-only
// items count only toward "What you worked on"; the brief text, "What needs
// your attention" and the cards never use them. Grounding still decides
// whether an anchored item needs the user (mobileBriefAttention). Checked
// activity_noticed suggestions have an Activity-validated anchor of their own;
// they fill only the lowest pick tier and never become attention rows.
const (
	mobileBriefAnchorMeeting  = "meeting"
	mobileBriefAnchorDue      = "due"
	mobileBriefAnchorPerson   = "person"
	mobileBriefAnchorOrder    = "order"
	mobileBriefAnchorDocument = "document"
	mobileBriefAnchorOwnWork  = "own_work"
	mobileBriefAnchorGoal     = "goal"
	mobileBriefAnchorNoticed  = "noticed"
)

// mobileBriefSourceAnchor is the anchor a source kind carries on its own.
func mobileBriefSourceAnchor(kind string) string {
	switch kind {
	case mobileBriefCalendarSource, "calendar_day", mobileBriefAttendanceSource:
		return mobileBriefAnchorMeeting
	case mobileBriefWaitingReplySource, mobileBriefMessageAskSource:
		return mobileBriefAnchorPerson
	case "history", "handoff", "schedule", "schedule_run", "deliverable":
		return mobileBriefAnchorOwnWork
	case mobileBriefFileSource:
		return mobileBriefAnchorDocument
	case mobileBriefNoticedSource:
		return mobileBriefAnchorNoticed
	case "purpose":
		return mobileBriefAnchorGoal
	default:
		return ""
	}
}

// mobileBriefOutcomeAnchor anchors an observed thread only when it ended at a
// bill or order or at a document; an answer from an AI tool, or research, is
// activity.
func mobileBriefOutcomeAnchor(outcome *ActivityThreadOutcome) string {
	if outcome == nil {
		return ""
	}
	switch outcome.Kind {
	case "purchase":
		return mobileBriefAnchorOrder
	case "document":
		return mobileBriefAnchorDocument
	default:
		return ""
	}
}

// mobileBriefItemFactsAnchor reads the anchor from an item's own facts.
func mobileBriefItemFactsAnchor(kind, status string, due bool, outcome *ActivityThreadOutcome) string {
	if due {
		return mobileBriefAnchorDue
	}
	switch kind {
	case "waiting_on_reply":
		return mobileBriefAnchorPerson
	case "one_time_work":
		return mobileBriefAnchorOwnWork
	case "meeting", mobileBriefAttendanceSource:
		return mobileBriefAnchorMeeting
	case "goal", "commitment":
		return mobileBriefAnchorGoal
	}
	if status == "no_recent_touch" {
		return mobileBriefAnchorPerson
	}
	return mobileBriefOutcomeAnchor(outcome)
}

// mobileBriefCandidateAnchor is the anchor of an item while the brief is built.
func mobileBriefCandidateAnchor(item mobileBriefCandidate) string {
	// Words that narrate typing never name a task, whatever holds them.
	if item.EvidenceUnavailable || threadOutcomeTypingNarration.MatchString(item.Title) {
		return ""
	}
	if anchor := mobileBriefItemFactsAnchor(item.Kind, item.Status, !item.DueAt.IsZero(), item.Outcome); anchor != "" {
		return anchor
	}
	return mobileBriefSourceAnchor(item.SourceKind)
}

// mobileBriefItemAnchor is the anchor of a stored item: its own facts, or the
// source its primary ref names.
func mobileBriefItemAnchor(item MobileBriefItem, evidence map[string]MobileBriefRef) string {
	if item.EvidenceUnavailable || threadOutcomeTypingNarration.MatchString(item.Title) {
		return ""
	}
	if anchor := mobileBriefItemFactsAnchor(item.Kind, item.Status, !mobileBriefParseTime(item.DueAt).IsZero(), item.Outcome); anchor != "" {
		return anchor
	}
	if item.PurposeID != "" {
		return mobileBriefAnchorGoal
	}
	if len(item.Refs) == 0 {
		return ""
	}
	return mobileBriefSourceAnchor(evidence[item.Refs[0]].SourceKind)
}

// mobileBriefAnchorInput moves observed threads that are only activity out of
// the open loops: a thread in an AI tool or a terminal is what the user worked
// on, never a loop. Its next step stays with it under "What you worked on".
func mobileBriefAnchorInput(in mobileBriefInput) mobileBriefInput {
	var loops []mobileBriefCandidate
	for _, item := range in.OpenLoops {
		if item.Kind == mobileBriefThreadOutcomeSource && mobileBriefCandidateAnchor(item) == "" {
			in.Moved = append(in.Moved, item)
			continue
		}
		loops = append(loops, item)
	}
	in.OpenLoops = loops
	return in
}

// mobileBriefEvidenceByRef indexes every evidence ref of a brief.
func mobileBriefEvidenceByRef(brief MobileBriefV1) map[string]MobileBriefRef {
	out := make(map[string]MobileBriefRef, len(brief.Evidence))
	for _, ref := range brief.Evidence {
		out[ref.Ref] = ref
	}
	return out
}

// mobileBriefWithoutUnanchoredAttention clears the attention line of stored
// items that are only activity, so a brief written before the rule cannot list
// them under "What needs your attention".
func mobileBriefWithoutUnanchoredAttention(brief MobileBriefV1) MobileBriefV1 {
	evidence := mobileBriefEvidenceByRef(brief)
	clear := func(items []MobileBriefItem) []MobileBriefItem {
		if items == nil {
			return nil
		}
		out := make([]MobileBriefItem, len(items))
		for i, item := range items {
			if strings.TrimSpace(item.AttentionReason) != "" && mobileBriefItemAnchor(item, evidence) == "" {
				item.AttentionReason, item.AttentionRank = "", 0
			}
			out[i] = item
		}
		return out
	}
	s := &brief.Sections
	s.Moved, s.Doing, s.OpenLoops, s.People = clear(s.Moved), clear(s.Doing), clear(s.OpenLoops), clear(s.People)
	s.Deliverables, s.WorkingToward, s.Commitments, s.WaitingOnYou = clear(s.Deliverables), clear(s.WorkingToward), clear(s.Commitments), clear(s.WaitingOnYou)
	s.Meetings = clear(s.Meetings)
	brief.Upcoming = clear(brief.Upcoming)
	return brief
}
