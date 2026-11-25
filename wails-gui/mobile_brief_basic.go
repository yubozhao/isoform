package main

// Filter before both the builder and the writer. A Purpose can stand on its
// own evidence after its optional Chat report link has been removed.
func mobileBriefBasicInput(in mobileBriefInput) mobileBriefInput {
	in.Watching = nil
	for _, group := range []*[]mobileBriefCandidate{
		&in.Moved, &in.Doing, &in.Meetings, &in.TomorrowMeetings, &in.People, &in.OpenLoops,
		&in.Learned, &in.Deliverables, &in.Schedules, &in.WorkingToward, &in.Commitments,
		&in.Noticed, &in.WaitingOnYou, &in.Conversations, &in.Marked,
	} {
		*group = mobileBriefNonChatCandidates(*group)
	}
	if in.Today != nil {
		today := *in.Today
		today.Upcoming = mobileBriefNonChatCandidates(today.Upcoming)
		in.Today = &today
	}
	return in
}

func mobileBriefNonChatCandidates(items []mobileBriefCandidate) []mobileBriefCandidate {
	var kept []mobileBriefCandidate
	for _, item := range items {
		if item.SourceKind == "chat_message" {
			continue
		}
		item.ChatSessionID, item.ChatMessageID = "", ""
		item.Evidence = mobileBriefNonChatCandidates(item.Evidence)
		kept = append(kept, item)
	}
	return kept
}
