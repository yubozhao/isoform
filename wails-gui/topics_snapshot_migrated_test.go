package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

const (
	migratedTopicID   = "migrated-topic-a"
	survivorTopicID   = "referenced-topic-b"
	migratedAppID     = "com.example.migrated"
	migratedSessionID = "migrated-source-session"
)

type migratedTopicFixture struct {
	app     *App
	service *topicsService
	dir     string
}

func newMigratedTopicFixture(t *testing.T) migratedTopicFixture {
	t.Helper()
	previousSensitiveApps := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previousSensitiveApps) })
	pii.SetUserSensitiveApps(map[string]string{})

	dir := t.TempDir()
	t.Setenv(topicsStateDirEnvVar, dir)
	app := newPrivacyReadTestApp(t)
	policy, err := currentTopicPolicyHash()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	migrated := TopicView{
		ID: migratedTopicID, Kind: "activity", Name: "Migrated topic A",
		Status: "active", PinnedToSidebar: true,
	}
	state := newTopicsStore(dir)
	if _, err := state.update(func(file *topicsStateFile) {
		file.Today = &topicTodaySnapshot{Views: []TopicView{migrated}, Policy: policy, At: at}
		file.SidebarPins = map[string]bool{migratedTopicID: true, survivorTopicID: true}
	}); err != nil {
		t.Fatal(err)
	}

	service := app.topicsSvc()
	t.Cleanup(func() {
		if service.privacyWithdrawalUnsubscribe != nil {
			service.privacyWithdrawalUnsubscribe()
			service.privacyWithdrawalUnsubscribe = nil
		}
	})
	snapshot := service.snapshots.Current()
	if snapshot == nil || len(snapshot.Topics) != 1 ||
		snapshot.Topics[0].Topic.ID != migratedTopicID || len(snapshot.Topics[0].Refs) != 0 {
		t.Fatalf("legacy Today migration = %+v", snapshot)
	}
	seeded := *snapshot
	seeded.Topics = append(append([]topicSnapshotEntry(nil), snapshot.Topics...), topicSnapshotEntry{
		Topic: TopicView{
			ID: survivorTopicID, Kind: "activity", Name: "Referenced survivor B",
			Status: "active", PinnedToSidebar: true,
		},
		Refs: []topicSourceReference{
			{Kind: topicRefApp, Value: "com.example.survivor"},
			{Kind: topicRefSession, Value: "unrelated-survivor-session"},
		},
	})
	if err := service.snapshots.Replace(&seeded); err != nil {
		t.Fatal(err)
	}
	return migratedTopicFixture{app: app, service: service, dir: dir}
}

func TestTopicSnapshotMigratedEntriesAreRemovedByAppWithdrawal(t *testing.T) {
	fixture := newMigratedTopicFixture(t)
	pii.SetUserSensitiveApps(map[string]string{migratedAppID: "Code"})
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: migratedAppID})

	assertMigratedTopicReadPaths(t, fixture.app)
	assertMigratedSnapshotIDs(t, fixture.dir, false)
}

func TestTopicSnapshotMigratedEntriesAreRemovedBySessionDeletion(t *testing.T) {
	fixture := newMigratedTopicFixture(t)
	main, err := fixture.app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.SaveSession(t.Context(), storage.SessionRow{
		LocalID: migratedSessionID, ProjectID: main.ProjectID, WorkspaceID: main.WorkspaceID,
		Label: "Migrated source",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.DeleteSessionData(migratedSessionID); err != nil {
		t.Fatal(err)
	}

	assertMigratedTopicReadPaths(t, fixture.app)
	assertMigratedSnapshotIDs(t, fixture.dir, false)
}

func TestTopicSnapshotMigratedEntriesHideOnPolicyMismatchAfterRestart(t *testing.T) {
	fixture := newMigratedTopicFixture(t)
	pii.SetUserSensitiveApps(map[string]string{migratedAppID: "Code"})

	restartedApp := newPrivacyReadTestApp(t)
	service := restartedApp.topicsSvc()
	t.Cleanup(func() {
		if service.privacyWithdrawalUnsubscribe != nil {
			service.privacyWithdrawalUnsubscribe()
			service.privacyWithdrawalUnsubscribe = nil
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	beforeSnapshot := make(chan struct{})
	service.registryBeforeSnapshot = func() {
		close(beforeSnapshot)
		<-ctx.Done()
	}
	buildDone := make(chan struct{})
	go func() {
		defer close(buildDone)
		_, _ = service.registryViews(ctx)
	}()
	select {
	case <-beforeSnapshot:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("registry build did not reach the pre-snapshot hold")
	}

	assertMigratedTopicReadPaths(t, restartedApp)
	assertMigratedSnapshotIDs(t, fixture.dir, true)

	cancel()
	select {
	case <-buildDone:
	case <-time.After(5 * time.Second):
		t.Fatal("held registry build did not stop after cancellation")
	}
}

func TestTopicSnapshotBuildReplacesMigratedEntriesEvenWhenCosmeticFieldsMatch(t *testing.T) {
	dir := t.TempDir()
	view := TopicView{ID: migratedTopicID, Name: "Migrated topic A", Status: "active"}
	published, err := json.Marshal(map[string]any{
		"version":    topicSnapshotSchemaVersion,
		"policyHash": [32]byte{},
		"at":         time.Now(),
		"topics": []any{
			map[string]any{"topic": view, "unproven": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, topicSnapshotFileName), published, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots := newTopicSnapshotStore(dir)
	if snapshots.Load() == nil {
		t.Fatal("failed to load migrated snapshot fixture")
	}
	service := &topicsService{snapshots: snapshots}
	candidate := &topicSnapshot{
		Version: topicSnapshotSchemaVersion,
		Topics: []topicSnapshotEntry{{
			Topic: view,
			Refs: []topicSourceReference{
				{Kind: topicRefApp, Value: "com.example.survivor"},
				{Kind: topicRefSession, Value: "unrelated-survivor-session"},
			},
		}},
	}
	if !service.commitBuiltSnapshot(candidate, true, false) {
		t.Error("first build did not replace a cosmetically identical migrated entry")
	}
	current := snapshots.Current()
	if current == nil || len(current.Topics) != 1 || len(current.Topics[0].Refs) == 0 {
		t.Errorf("published snapshot still has an unreferenced entry: %+v", current)
	}
}

func assertMigratedTopicReadPaths(t *testing.T, app *App) {
	t.Helper()
	today, err := app.GetTodayTopics()
	if err != nil {
		t.Errorf("GetTodayTopics: %v", err)
	} else {
		assertMigratedTopicIDs(t, "Today", topicViewIDs(today.Topics))
	}

	sidebar, err := app.ListSidebarTopics()
	if err != nil {
		t.Errorf("ListSidebarTopics: %v", err)
	} else {
		assertMigratedTopicIDs(t, "sidebar", topicViewIDs(sidebar))
	}

	bridge := &mobileBridge{app: app, ctx: t.Context()}
	phone := mobileTopicTestList(t, bridge)
	phoneIDs := make([]string, 0, len(phone))
	for _, view := range phone {
		phoneIDs = append(phoneIDs, view.ID)
	}
	assertMigratedTopicIDs(t, "phone", phoneIDs)
}

func assertMigratedTopicIDs(t *testing.T, surface string, ids []string) {
	t.Helper()
	foundMigrated, foundSurvivor := false, false
	for _, id := range ids {
		foundMigrated = foundMigrated || id == migratedTopicID
		foundSurvivor = foundSurvivor || id == survivorTopicID
	}
	if foundMigrated || !foundSurvivor {
		t.Errorf("%s topic IDs = %v; migrated=%t survivor=%t", surface, ids, foundMigrated, foundSurvivor)
	}
}

func topicViewIDs(views []TopicView) []string {
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	return ids
}

func assertMigratedSnapshotIDs(t *testing.T, dir string, wantMigrated bool) {
	t.Helper()
	store := newTopicSnapshotStore(dir)
	snapshot := store.Load()
	if snapshot == nil {
		t.Error("fresh snapshot store could not reload the published snapshot")
		return
	}
	ids := make([]string, 0, len(snapshot.Topics))
	for _, entry := range snapshot.Topics {
		ids = append(ids, entry.Topic.ID)
	}
	foundMigrated, foundSurvivor := false, false
	for _, id := range ids {
		foundMigrated = foundMigrated || id == migratedTopicID
		foundSurvivor = foundSurvivor || id == survivorTopicID
	}
	if foundMigrated != wantMigrated || !foundSurvivor {
		t.Errorf("persisted topic IDs = %v; migrated=%t want %t, survivor=%t", ids, foundMigrated, wantMigrated, foundSurvivor)
	}
}
