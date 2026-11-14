package main

import (
	"testing"
	"time"
)

// The balance comes from the local time of the request, and scheduled slots
// keep their own balance.
func TestRankedBalanceSlotFollowsLocalTimeForOnRequest(t *testing.T) {
	nine := time.Date(2026, 11, 11, 9, 59, 0, 0, time.UTC)
	noon := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		slot string
		now  time.Time
		want string
	}{
		{mobileBriefSlotOnRequest, nine, mobileBriefSlotMorning},
		{mobileBriefSlotOnRequest, noon, mobileBriefSlotEvening},
		{mobileBriefSlotOnRequest, time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC), mobileBriefSlotEvening},
		{mobileBriefSlotMorning, noon, mobileBriefSlotMorning},
		{mobileBriefSlotEvening, nine, mobileBriefSlotEvening},
	}
	for _, c := range cases {
		if got := rankedBalanceSlot(c.slot, c.now); got != c.want {
			t.Fatalf("rankedBalanceSlot(%s, %s) = %s, want %s", c.slot, c.now.Format(time.RFC3339), got, c.want)
		}
	}
}
