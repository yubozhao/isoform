package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"wails-gui/storage"
)

func seedPerfStream(tb testing.TB, a *App, count int) string {
	tb.Helper()
	ctx := context.Background()
	if err := a.store.SaveProject(ctx, storage.ProjectRow{ID: "perf-project", Name: "Fixture", Path: "fixture"}); err != nil {
		tb.Fatal(err)
	}
	if err := a.store.SaveSession(ctx, storage.SessionRow{LocalID: "perf-chat", ProjectID: "perf-project", Label: "Fixture"}); err != nil {
		tb.Fatal(err)
	}
	rows := make([]storage.StreamItemRow, count)
	for i := range rows {
		rows[i] = storage.StreamItemRow{ID: fmt.Sprintf("perf-item-%06d", i), Kind: "user", Content: "Fixture message"}
	}
	if err := a.store.SaveStreamItems(ctx, "perf-chat", rows); err != nil {
		tb.Fatal(err)
	}
	return "perf-chat"
}

func BenchmarkLoadStreamItemsPage(b *testing.B) {
	fixture := newPeopleProfileFixture(b)
	session := seedPerfStream(b, fixture.app, 20000)
	start := time.Now()
	page, err := fixture.app.LoadStreamItemsPage(session, -1, 50)
	cold := time.Since(start)
	if err != nil || len(page.Items) != 50 {
		b.Fatalf("cold page: %d, %v", len(page.Items), err)
	}
	fixture.app.verifyStreamPageCopy(streamPageKey{session: session, limit: 50})
	for _, cursor := range []struct {
		name  string
		value int
	}{{"latest", -1}, {"older", 19000}} {
		b.Run(cursor.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := fixture.app.LoadStreamItemsPage(session, cursor.value, 50)
				if err != nil || len(page.Items) != 50 {
					b.Fatalf("page: %d, %v", len(page.Items), err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(cold)/float64(time.Millisecond), "cold-latest-ms")
		})
	}
}

func TestPeopleStatsSnapshotIsolationAndInvalidation(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	first, err := a.ListPrincipalsWithStats()
	if err != nil || len(first) == 0 {
		t.Fatalf("first: %d, %v", len(first), err)
	}
	original := first[0].Principal.DisplayName
	first[0].Principal.DisplayName = "changed by caller"
	again, err := a.ListPrincipalsWithStats()
	if err != nil || again[0].Principal.DisplayName != original {
		t.Fatalf("cached result changed: %v", err)
	}
	if err := a.store.SetUIState(context.Background(), "sensitive_apps", `{}`); err != nil {
		t.Fatal(err)
	}
	changed, err := a.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := peopleStatsForName(changed, "Batch Person 04"); !ok || row.Stats.TotalMessages == 0 {
		t.Fatal("policy change did not invalidate snapshot")
	}
}

func TestPeopleAndStreamBindingsDoNotBlockEachOther(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	session := seedPerfStream(t, a, 2000)
	// Prime and verify the latest-page copy before holding the People read.
	if _, err := a.LoadStreamItemsPage(session, -1, 50); err != nil {
		t.Fatal(err)
	}
	a.verifyStreamPageCopy(streamPageKey{session: session, limit: 50})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.peopleLineageProbe = func() { once.Do(func() { close(entered); <-release }) }
	peopleDone := make(chan error, 1)
	go func() { _, err := a.ListPrincipalsWithStats(); peopleDone <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("People read did not reach fixture gate")
	}
	streamDone := make(chan error, 1)
	go func() { _, err := a.LoadStreamItemsPage(session, -1, 50); streamDone <- err }()
	select {
	case err := <-streamDone:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		close(release)
		t.Fatal("stream waited for People read")
	}
	if err := <-peopleDone; err != nil {
		t.Fatal(err)
	}
}

func TestStreamBindingServesSnapshotDuringStateWrite(t *testing.T) {
	a, session := streamPageTestApp(t)
	latestPage(t, a, session)
	tx, err := peopleProfileStorageDB(a.store).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE stream_items SET content='pending' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.LoadStreamItemsPage(session, -1, 50); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		tx.Rollback()
		<-done
		t.Fatal("stream snapshot waited for a state writer")
	}
}

func TestStreamColdPageDoesNotWaitForStateWriter(t *testing.T) {
	a, session := streamPageTestApp(t)
	rows := make([]storage.StreamItemRow, 50)
	for i := range rows {
		rows[i] = storage.StreamItemRow{ID: fmt.Sprintf("assistant-%d", i), Kind: "assistant", Content: "reply"}
	}
	rows[0].Kind = "system"
	rows[1].Kind = "user"
	if err := a.store.SaveStreamItems(context.Background(), session, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleProfileStorageDB(a.store).Exec(`INSERT INTO stream_item_topics(stream_item_id,session_id,topic_id,created_at) VALUES (?,?,?,?)`, rows[0].ID, session, "topic", 1); err != nil {
		t.Fatal(err)
	}
	tx, err := peopleProfileStorageDB(a.store).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE stream_items SET content='pending' WHERE id='one'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		page, err := a.LoadStreamItemsPage(session, -1, 50)
		if err == nil {
			if len(page.Items) != 50 {
				err = fmt.Errorf("got %d items", len(page.Items))
			}
			for _, item := range page.Items {
				if item.ID == rows[0].ID && (len(item.TopicIDs) != 0 || item.LocalDay == "") {
					err = fmt.Errorf("system row should retain classification but omit unverified topics: %+v", item)
				}
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if _, reason := readRawStreamPageSnapshot(streamPageKey{session: session, limit: 50}); reason == "" {
			t.Fatal("incomplete topic projection was saved as a snapshot")
		}
		t.Logf("cold page while writer held: %s", time.Since(start))
	case <-time.After(300 * time.Millisecond):
		tx.Rollback()
		<-done
		t.Fatal("cold stream page waited for state writer")
	}
}

func TestPeopleBindingDoesNotWaitForStreamComputation(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	session := seedPerfStream(t, a, 2000)
	if _, err := a.ListPrincipalsWithStats(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.streamPages.mu.Lock()
	a.streamPages.settings()
	a.streamPages.computeGate = func() { once.Do(func() { close(entered); <-release }) }
	a.streamPages.mu.Unlock()
	streamDone := make(chan error, 1)
	go func() { _, err := a.LoadStreamItemsPage(session, -1, 50); streamDone <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("stream did not reach fixture gate")
	}
	peopleDone := make(chan error, 1)
	go func() { _, err := a.ListPrincipalsWithStats(); peopleDone <- err }()
	select {
	case err := <-peopleDone:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-streamDone
		t.Fatal("People waited for stream computation")
	}
	if err := <-streamDone; err != nil {
		t.Fatal(err)
	}
}
