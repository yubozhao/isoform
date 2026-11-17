package topicday

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A named topic leads the copy and survives the MaxTopics cut, however old.
func TestNamedTopicsLeadAndAreNeverCut(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	candidates := []Candidate{{ID: "named", Kind: KindSubject, Name: "Kitchen remodel", LastSeenAt: "2026-01-01T00:00:00Z", Named: true}}
	for i := 0; i < MaxTopics+3; i++ {
		candidates = append(candidates, Candidate{ID: fmt.Sprintf("t%d", i), Kind: KindProject, Name: fmt.Sprintf("Project %d", i),
			LastSeenAt: now.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339)})
	}
	none := func(context.Context, []Topic, []Feedback) (map[string]ProposalDraft, error) { return nil, nil }
	copy, err := Build(context.Background(), "2026-11-16", now, candidates, Choices{}, nil, nil, none, func() string { return "p" })
	if err != nil {
		t.Fatal(err)
	}
	if len(copy.Topics) != MaxTopics || copy.Topics[0].ID != "named" || !copy.Topics[0].Named {
		t.Fatalf("copy = %d topics, first %+v", len(copy.Topics), copy.Topics[0])
	}
}
