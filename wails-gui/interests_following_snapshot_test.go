package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/interests"
)

// A pass's pre-model closure memoizes an allow for every reference it checks.
// A source protected or deleted while the model runs must not keep that allow
// in the verification snapshot: the snapshot is rebuilt from a fresh complete
// filter, so following withholds the withdrawn derivative under the current
// revision.
func TestInterestsFollowingSnapshotIgnoresPreModelDecisions(t *testing.T) {
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	configureTopicRegistryTest(a, topicRegistryTestNow)
	start, end := topicRegistryTestNow.Add(-time.Hour), topicRegistryTestNow.Add(-time.Minute)
	seedThreadMoment(t, a, threadFixtureMoment{id: "work", title: "Atlas robotics research", app: "Code", start: start, end: end})
	seedThreadMoment(t, a, threadFixtureMoment{id: "stable", title: "Kepler telescope notes", app: "Code", start: start, end: end})
	work := testInterest("work", "Atlas robotics research", "You follow Atlas robotics.")
	stable := testInterest("stable", "Kepler telescope notes", "You keep Kepler telescope notes.")
	storeTestInterests(t, a, work, stable)
	svc := a.interestsSvc()
	svc.readFilter = a.interestsReadFilter

	// The pass's pre-model closure authorizes both references and memoizes
	// the decisions, exactly as maybeRun's replay checks do.
	stale := a.interestsReadFilter()
	if !stale(work) || !stale(stable) {
		t.Fatal("the pre-model closure did not authorize the fixtures")
	}
	// The user protects work's session before the snapshot is taken.
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-work'`); err != nil {
		t.Fatal(err)
	}
	svc.rememberFollowing(t.Context(), svc.store.load().Interests)
	if !stale(work) {
		t.Fatal("the memoized closure lost its stale decision")
	}
	revision, err := a.topicsSvc().captureRegistryRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.following(t.Context(), revision)
	if err != nil {
		t.Fatal(err)
	}
	if followingFixtureContains(got, "work") {
		t.Fatalf("a pre-withdrawal decision authorized the protected derivative: %v", followingFixtureIDs(got))
	}
	if !followingFixtureContains(got, "stable") {
		t.Fatalf("the stable source lost its followed interest: %v", followingFixtureIDs(got))
	}
}

// The whole pass with the withdrawal landing inside the model callback: the
// replay checks warm the closure, the answer retains every interest, and the
// snapshot published after the store write must still withhold both the
// protected and the deleted derivative, agreeing with a fresh complete reader.
func TestInterestsPassWithdrawalDuringModelCallPublishesNoStaleDecisions(t *testing.T) {
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	configureTopicRegistryTest(a, topicRegistryTestNow)
	titles := map[string]string{
		"work":   "Atlas robotics research",
		"play":   "Delta synthesizer teardown",
		"stable": "Kepler telescope notes",
	}
	start, end := topicRegistryTestNow.Add(-time.Hour), topicRegistryTestNow.Add(-time.Minute)
	for _, id := range []string{"work", "play", "stable", "filler-a", "filler-b", "filler-c"} {
		title := titles[id]
		if title == "" {
			title = "Filler moment " + id
		}
		seedThreadMoment(t, a, threadFixtureMoment{id: id, title: title, app: "Code", start: start, end: end})
	}
	stored := []interests.Interest{
		testInterest("work", titles["work"], "You follow Atlas robotics."),
		testInterest("play", titles["play"], "You study Delta synthesizers."),
		testInterest("stable", titles["stable"], "You keep Kepler telescope notes."),
	}
	svc := a.interestsSvc()
	svc.readFilter = a.interestsReadFilter
	svc.allowed = func() bool { return true }
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = stored
		st.RefreshedAt = topicRegistryTestNow.Add(-21 * time.Hour).UTC().Format(time.RFC3339Nano)
		st.ConsolidatedAt = topicRegistryTestNow.UTC().Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}

	withdrawn := false
	svc.model = func(ctx context.Context, prompt, name string) (string, error) {
		// The user withdraws two sources while the model call is pending:
		// work's session is protected and play's activity is deleted.
		if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-work'`); err != nil {
			t.Fatal(err)
		}
		if _, err := a.activity.store.db.Exec(`DELETE FROM activities WHERE id='play'`); err != nil {
			t.Fatal(err)
		}
		withdrawn = true
		// Retain every stored interest, citing its prompt evidence id.
		cited := map[string]string{}
		for _, line := range strings.Split(prompt, "\n") {
			if !strings.HasPrefix(line, "[A") {
				continue
			}
			close := strings.Index(line, "]")
			if close < 0 {
				continue
			}
			for id, title := range titles {
				if strings.Contains(line, title) {
					cited[id] = line[1:close]
				}
			}
		}
		var b strings.Builder
		b.WriteString(`{"interests":[`)
		for i, id := range []string{"work", "play", "stable"} {
			if cited[id] == "" {
				t.Fatalf("the prompt held no activity evidence for %q", titles[id])
			}
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"` + titles[id] + `","why":"You still follow this.","kind":"project","confidence":0.9,"evidence":["` + cited[id] + `"],"queries":["` + titles[id] + ` news"]}`)
		}
		b.WriteString(`],"people":[]}`)
		return b.String(), nil
	}
	if !svc.maybeRun(t.Context(), "test") {
		t.Fatal("the pass did not run")
	}
	if !withdrawn {
		t.Fatal("the pass never reached the model callback")
	}

	// Without the snapshot the bounded filter would allow everything here, so
	// only the snapshot's own decisions can withhold the withdrawn derivatives.
	svc.followingFilter = func(ctx context.Context) func(interests.Interest) bool {
		return func(interests.Interest) bool { return true }
	}
	revision, err := a.topicsSvc().captureRegistryRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.following(t.Context(), revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"work", "play"} {
		if followingFixtureContains(got, id) {
			t.Fatalf("a stale decision authorized the withdrawn derivative %q: %v", id, followingFixtureIDs(got))
		}
	}
	if !followingFixtureContains(got, "stable") {
		t.Fatalf("the stable source lost its followed interest: %v", followingFixtureIDs(got))
	}
	// Following agrees with a fresh complete reader.
	complete := followingFixtureIDs(svc.list())
	followed := followingFixtureIDs(got)
	sort.Strings(complete)
	sort.Strings(followed)
	if strings.Join(complete, ",") != strings.Join(followed, ",") {
		t.Fatalf("following %v disagrees with the complete reader %v", followed, complete)
	}
}

func TestInterestsFollowingReadsOldPinnedEvidenceWithoutSnapshot(t *testing.T) {
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	configureTopicRegistryTest(a, topicRegistryTestNow)
	pinned := map[string]bool{}
	list := make([]interests.Interest, 0, 10)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("following-%d", i)
		at := topicRegistryTestNow.Add(-time.Hour)
		if i < 8 {
			id = fmt.Sprintf("old-%d", i)
			at = topicRegistryTestNow.Add(-interests.RefreshWindow - 24*time.Hour)
			pinned[id] = true
		}
		seedThreadMoment(t, a, threadFixtureMoment{id: id, title: "Research topic " + id, app: "Code", start: at.Add(-time.Hour), end: at})
		item := testInterest(id, "Research topic "+id, "Current research")
		item.Evidence[0].At = at.UnixMilli()
		list = append(list, item)
	}
	storeTestInterests(t, a, list...)
	svc := a.interestsSvc()
	svc.readFilter = a.interestsReadFilter
	svc.choices = func() interestsUserState { return interestsUserState{Pinned: pinned} }
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-following-9'`); err != nil {
		t.Fatal(err)
	}

	// The bounded activity pre-pass verifies recent sources, not the old pinned
	// interests outside its window. There is no background snapshot yet.
	for _, item := range list[8:] {
		a.interestsReadFilter()(item)
	}
	revision, err := a.topicsSvc().captureRegistryRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	got, err := svc.following(t.Context(), revision)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	complete := followingFixtureIDs(svc.list())
	followed := followingFixtureIDs(got)
	sort.Strings(complete)
	sort.Strings(followed)
	if len(complete) != 9 || strings.Join(complete, ",") != strings.Join(followed, ",") {
		t.Fatalf("following %v disagrees with the nine readable interests %v", followed, complete)
	}
	if followingFixtureContains(got, "following-9") {
		t.Fatal("withdrawn evidence remained visible")
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("following exceeded 200ms: %s", elapsed)
	}
}

func followingFixtureContains(list []interests.Interest, id string) bool {
	for _, item := range list {
		if item.ID == id {
			return true
		}
	}
	return false
}

func followingFixtureIDs(list []interests.Interest) []string {
	ids := make([]string, 0, len(list))
	for _, item := range list {
		ids = append(ids, item.ID)
	}
	return ids
}
