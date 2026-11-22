package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/interests"
)

func TestTodayColdFollowingAuthorizesOriginalSources(t *testing.T) {
	for _, reader := range []string{"desktop", "phone"} {
		t.Run(reader, func(t *testing.T) {
			t.Setenv(topicsStateDirEnvVar, t.TempDir())
			a := newThreadTestApp(t)
			configureTopicRegistryTest(a, topicRegistryTestNow)
			seedThreadMoment(t, a, threadFixtureMoment{id: "work", title: "Atlas", app: "Code", start: topicRegistryTestNow.Add(-time.Hour), end: topicRegistryTestNow.Add(-time.Minute), entities: []Entity{{Type: entityProject, Name: "Atlas"}}})
			interest := testInterest("cold-following", "Optical camera modules", "Current research")
			interest.Evidence = []interests.Ref{{Key: "activity:work", Source: interests.SourceActivity, At: topicRegistryTestNow.Add(-time.Minute).UnixMilli()}}
			storeTestInterests(t, a, interest)
			a.interestsSvc().readFilter = a.interestsReadFilter
			if _, err := collectAndReadTopicViewsForTest(a); err != nil {
				t.Fatal(err)
			}
			read := func() []TopicView {
				t.Helper()
				var views []TopicView
				if reader == "phone" {
					phone := mobileTopicTestList(t, &mobileBridge{app: a, ctx: t.Context()})
					views = make([]TopicView, 0, len(phone))
					for _, topic := range phone {
						views = append(views, TopicView{Name: topic.Name})
					}
					return views
				}
				response, err := a.GetTodayTopics()
				if err != nil || response.Refreshing {
					t.Fatalf("desktop snapshot read: %+v, %v", response, err)
				}
				views = response.Topics
				return views
			}
			contains := func(views []TopicView) bool {
				for _, view := range views {
					if view.Name == interest.Name {
						return true
					}
				}
				return false
			}
			if !contains(read()) {
				t.Fatal("readable original source lost its followed label")
			}
			a.closedEyes.enabled.Store(true)
			response, err := a.GetTodayTopics()
			if err != nil || !contains(response.Topics) {
				t.Fatalf("actual Closed Eyes lost its stored-label path: %+v, %v", response, err)
			}
			a.closedEyes.enabled.Store(false)
			for _, ref := range []interests.Ref{
				{Key: "activity:missing", Source: interests.SourceActivity},
				{Key: "named:unverified", Source: interests.SourceNamed},
			} {
				mixed := interest
				mixed.Evidence = append(append([]interests.Ref(nil), interest.Evidence...), ref)
				storeTestInterests(t, a, mixed)
				if _, err := collectAndReadTopicViewsForTest(a); err != nil {
					t.Fatal(err)
				}
				if contains(read()) {
					t.Fatal("partly authorized evidence closure leaked its label")
				}
			}
			storeTestInterests(t, a, interest)
			if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-work'`); err != nil {
				t.Fatal(err)
			}
			if list := a.interestsSvc().list(); len(list) != 0 {
				t.Fatalf("normal interest read failed to withhold source: %+v", list)
			}
			if _, err := collectAndReadTopicViewsForTest(a); err != nil {
				t.Fatal(err)
			}
			if contains(read()) {
				t.Fatal("full build leaked followed label from protected original source")
			}
		})
	}
}

func TestTodayColdPrivacyBudgetIncludesPauseHistory(t *testing.T) {
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	configureTopicRegistryTest(a, topicRegistryTestNow)
	seedThreadMoment(t, a, threadFixtureMoment{id: "work", title: "Atlas", app: "Code", start: topicRegistryTestNow.Add(-time.Hour), end: topicRegistryTestNow.Add(-time.Minute), entities: []Entity{{Type: entityProject, Name: "Atlas"}}})
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	svc := a.topicsSvc()
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	svc.registryBeforeSnapshot = func() { close(entered); <-ctx.Done() }
	svc.startRegistryWorker(ctx)
	defer func() { cancel(); svc.stopRegistryWorker() }()
	a.RefreshTodayTopics()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("builder did not reach snapshot")
	}
	readable := func() bool {
		response, err := a.GetTodayTopics()
		return err == nil && response.Refreshing && len(response.Topics) == 1 && response.Topics[0].Name == "Atlas"
	}
	if !readable() {
		t.Fatal("readable source lost its work topic")
	}
	// Snapshot reads must not wait for the store used by source proofs.
	release := blockStateStoreReads(t, a.store)
	defer release()
	type result struct {
		response TodayTopicsView
		err      error
	}
	elapsed := coldReadBestOfThree(t, func() {
		done := make(chan result, 1)
		go func() { response, err := a.GetTodayTopics(); done <- result{response, err} }()
		var got result
		select {
		case got = <-done:
		case <-time.After(300 * time.Millisecond):
			// Release the fixture before failing, so an old uncancellable read
			// drains and never leaks a goroutine or holds up later tests.
			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("privacy read did not drain after release")
			}
			t.Fatal("pause-history read exceeded the cold read budget")
		}
		if got.err != nil || !got.response.Refreshing || len(got.response.Topics) != 1 {
			t.Fatalf("snapshot blocked by privacy store: %+v %v", got.response, got.err)
		}
	})
	release()
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("pause-history read best=%s", elapsed)
	}
	if !readable() {
		t.Fatal("temporary privacy read failure destroyed readable topics")
	}
}
