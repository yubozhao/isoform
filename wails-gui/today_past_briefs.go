package main

// todayPastBriefsMax is how many earlier briefs Today's Past briefs lists,
// the same two weeks the phone's brief history shows.
const todayPastBriefsMax = 14

// ListPastBriefs is Today's Past briefs: the phone's brief index, newest
// first, read the same way (sealed reads; a lead only from a delivered
// narrative), so the computer never lists words the phone would withhold.
// Each row opens through GetTodayBriefByID.
func (a *App) ListPastBriefs() ([]MobileBriefSummary, error) {
	svc := a.ensureMobileBriefService()
	if svc == nil {
		return []MobileBriefSummary{}, nil
	}
	return mobileBriefSummariesWithLeads(svc, todayPastBriefsMax), nil
}
