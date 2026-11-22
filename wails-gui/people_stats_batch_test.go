package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

func listPrincipalsWithStatsReference(a *App) ([]PrincipalWithStatsDTO, error) {
	ctx := context.Background()
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	principals, err := store.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	statsMap, err := store.GetAllPrincipalStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PrincipalWithStatsDTO, 0, len(principals))
	for _, p := range principals {
		dto := principalToDTO(p)
		handles, err := store.ListAppHandlesByPrincipal(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("list handles for %s: %w", p.ID, err)
		}
		dto.attachHandles(handles)
		stats := statsMap[p.ID]
		if stats.PrincipalID == "" {
			stats.PrincipalID = p.ID
		}
		out = append(out, PrincipalWithStatsDTO{
			Principal: dto,
			Stats:     principalStatsToDTO(a.peopleStatsReadable(ctx, p, stats, nil)),
		})
	}
	if err := decorateContactDTOsReference(ctx, store, out); err != nil {
		return nil, err
	}
	return out, nil
}

func decorateContactDTOsReference(ctx context.Context, store *storage.Store, out []PrincipalWithStatsDTO) error {
	cards, err := store.ListImportedContactCards(ctx)
	if err != nil {
		return err
	}
	byRef := map[string]systemContact{}
	for _, card := range cards {
		var c systemContact
		if json.Unmarshal([]byte(card.Payload), &c) == nil {
			byRef[card.ExternalRef] = c
		}
	}
	for i := range out {
		dto := &out[i].Principal
		linked, err := store.LinkedContactRef(ctx, dto.ID)
		if err != nil {
			return err
		}
		dto.ContactLinked = linked != ""
		ref := linked
		if ref == "" {
			for _, h := range dto.AppHandles {
				if h.App == "contacts" {
					ref = h.HandleID
					break
				}
			}
		}
		if c, ok := byRef[ref]; ok {
			dto.ContactName = contactDisplayName(c)
			dto.ContactPhoto = contactPhoto(c.Photo)
		}
	}
	return nil
}

func newPeopleStatsBatchFixture(t *testing.T) *App {
	t.Helper()
	a := newTodayContextTestApp(t, true)
	ctx := context.Background()
	people := make([]storage.PrincipalRow, 0, 50)
	for i := 0; i < 50; i++ {
		p, err := a.CreateContact(fmt.Sprintf("Batch Person %02d", i), "colleague", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		row, err := a.store.GetPrincipal(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		people = append(people, row)
	}
	if _, err := a.store.BindAppHandle(ctx, people[0].ID, "discord", "z-handle", "Zed"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[0].ID, "discord", "a-handle", "Ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[0].ID, "slack", "same-person", "Same Person"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[1].ID, "contacts", "contact-handle", "Contact Handle"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[4].ID, "secret", "secret-handle", "Secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[5].ID, "discord", "profile-handle", "Profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(ctx, people[6].ID, "discord", "missing-session", "Missing"); err != nil {
		t.Fatal(err)
	}
	for i := 7; i < 15; i++ {
		if _, err := a.store.BindAppHandle(ctx, people[i].ID, "discord", fmt.Sprintf("person-%02d", i), fmt.Sprintf("Person %02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	payload, err := json.Marshal(systemContact{ID: "explicit-ref", FullName: "Explicit Contact", Phones: []string{"+15550001"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveImportedContactCard(ctx, storage.ImportedContactCard{ExternalRef: "explicit-ref", Payload: string(payload)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	payload, err = json.Marshal(systemContact{ID: "contact-handle", FullName: "Handle Contact", Phones: []string{"+15550002"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveImportedContactCard(ctx, storage.ImportedContactCard{ExternalRef: "contact-handle", Payload: string(payload)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleProfileStorageDB(a.store).ExecContext(ctx, `INSERT INTO principal_contact_links(principal_id, external_ref, receipt) VALUES (?, ?, ?)`, people[2].ID, "explicit-ref", `{}`); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Hour).UnixMilli()
	for _, sessionID := range []string{"tie-z", "tie-a"} {
		if err := a.store.BumpPrincipalSighting(ctx, people[16].ID, "discord", sessionID, base, ""); err != nil {
			t.Fatal(err)
		}
	}
	readableEvent := activitymon.ActivityEvent{Kind: activitymon.EventFocus, Timestamp: base, AppName: "Code", AppBundleID: "com.example.code", WindowTitle: "work"}
	if err := a.activity.store.SaveSession(ActivitySessionRow{ID: "batch-readable", StartTime: base, EndTime: base + 1000, AppNames: `["Code"]`, TriageStatus: "done"}, []activitymon.ActivityEvent{readableEvent}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := a.store.BumpPrincipalSighting(ctx, people[i].ID, "discord", "batch-readable", base+int64(i), fmt.Sprintf("preview-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.BumpPrincipalSighting(ctx, people[5].ID, "discord", "batch-readable", base+5, "profile preview"); err != nil {
		t.Fatal(err)
	}
	deniedEvent := activitymon.ActivityEvent{Kind: activitymon.EventFocus, Timestamp: base, AppName: "Code", WindowTitle: "private", PrivateWindow: true}
	if err := a.activity.store.SaveSession(ActivitySessionRow{ID: "batch-denied", StartTime: base, EndTime: base + 1000, AppNames: `["Code"]`, TriageStatus: "done"}, []activitymon.ActivityEvent{deniedEvent}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BumpPrincipalSighting(ctx, people[3].ID, "discord", "batch-denied", base, "denied preview"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BumpPrincipalSighting(ctx, people[6].ID, "discord", "batch-missing", base, "missing preview"); err != nil {
		t.Fatal(err)
	}
	if err := a.activity.store.SaveSession(ActivitySessionRow{
		ID: "batch-protected", StartTime: base, EndTime: base + 1000,
		AppNames: `["Code"]`, TriageStatus: "protected",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BumpPrincipalSighting(ctx, people[8].ID, "discord", "batch-protected", base, "protected preview"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BumpPrincipalSighting(ctx, people[4].ID, "secret", "batch-readable", base, "secret preview"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetUIState(ctx, "sensitive_apps", `{"secret":"Secret"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleProfileStorageDB(a.store).ExecContext(ctx, `UPDATE principals SET created_from = 'Passwords' WHERE id = ?`, people[5].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleProfileStorageDB(a.store).ExecContext(ctx, `UPDATE principals SET merged_into = ? WHERE id = ?`, people[0].ID, people[7].ID); err != nil {
		t.Fatal(err)
	}
	return a
}

func comparePeopleStatsReference(t *testing.T, a *App) []PrincipalWithStatsDTO {
	t.Helper()
	want, err := listPrincipalsWithStatsReference(a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.listPrincipalsWithStats(newPeopleReadGateCache())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batched result differs from per-person reference:\n got=%+v\nwant=%+v", got, want)
	}
	return got
}

func peopleStatsForName(rows []PrincipalWithStatsDTO, name string) (PrincipalWithStatsDTO, bool) {
	for _, row := range rows {
		if row.Principal.DisplayName == name {
			return row, true
		}
	}
	return PrincipalWithStatsDTO{}, false
}

func peopleStatsAreZero(stats PrincipalStatsDTO) bool {
	return stats.TotalMessages == 0 && stats.FirstSeenAt == 0 && stats.LastSeenAt == 0 && len(stats.ByApp) == 0 && len(stats.RecentMessages) == 0
}

func TestListPrincipalsWithStatsMatchesPerPersonReference(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	readable := comparePeopleStatsReference(t, a)
	if _, ok := peopleStatsForName(readable, "Batch Person 07"); ok {
		t.Fatal("merged principal remained in the result")
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 49"); !ok || !peopleStatsAreZero(row.Stats) {
		t.Fatalf("never-sighted principal has unexpected stats: %+v", row)
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 00"); !ok || len(row.Principal.AppHandles) != 3 ||
		row.Principal.AppHandles[0].App != "discord" || row.Principal.AppHandles[0].DisplayHandle != "Ada" ||
		row.Principal.AppHandles[1].App != "discord" || row.Principal.AppHandles[1].DisplayHandle != "Zed" ||
		row.Principal.AppHandles[2].App != "slack" {
		t.Fatalf("handles are not in per-principal order: %+v", row.Principal.AppHandles)
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 01"); !ok || row.Principal.ContactLinked || row.Principal.ContactName != "Handle Contact" {
		t.Fatalf("contact handle did not resolve to its imported card: %+v", row.Principal)
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 02"); !ok || !row.Principal.ContactLinked || row.Principal.ContactName != "Explicit Contact" {
		t.Fatalf("explicit contact link did not resolve: %+v", row.Principal)
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 03"); !ok || row.Principal.ContactLinked || row.Principal.ContactName != "" {
		t.Fatalf("unlinked principal unexpectedly resolved a contact: %+v", row.Principal)
	}
	for _, name := range []string{"Batch Person 03", "Batch Person 04", "Batch Person 06"} {
		if row, ok := peopleStatsForName(readable, name); !ok || !peopleStatsAreZero(row.Stats) {
			t.Fatalf("%s has unreadable session/app stats: %+v", name, row.Stats)
		}
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 05"); !ok ||
		row.Stats.TotalMessages == 0 || len(row.Stats.RecentMessages) != 0 {
		t.Fatalf("profile denial did not drop previews while preserving readable counts: %+v", row.Stats)
	}
	if row, ok := peopleStatsForName(readable, "Batch Person 08"); !ok || !peopleStatsAreZero(row.Stats) {
		t.Fatalf("protected session remained readable through the merged preload: %+v", row.Stats)
	}

	a.closedEyes.enabled.Store(true)
	for _, row := range comparePeopleStatsReference(t, a) {
		if !peopleStatsAreZero(row.Stats) {
			t.Fatalf("Closed Eyes left nonzero stats for %s: %+v", row.Principal.DisplayName, row.Stats)
		}
	}
	a.closedEyes.enabled.Store(false)

	activity := a.activity
	a.activity = nil
	for _, row := range comparePeopleStatsReference(t, a) {
		if !peopleStatsAreZero(row.Stats) {
			t.Fatalf("missing activity store left nonzero stats for %s: %+v", row.Principal.DisplayName, row.Stats)
		}
	}
	a.activity = activity
}

func TestListPrincipalsWithStatsProtectedSessionMatchesPerPersonReference(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	rows := comparePeopleStatsReference(t, a)
	row, ok := peopleStatsForName(rows, "Batch Person 08")
	if !ok || !peopleStatsAreZero(row.Stats) {
		t.Fatalf("protected session differs from the per-person reference: %+v", row.Stats)
	}
}

func TestPeopleSessionAppCacheMatchesUncachedVerdicts(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	session, err := a.activity.store.GetSessionByID("batch-readable")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, appNames, meetingApp string
	}{
		{name: "empty meeting app", appNames: `["Code"]`},
		{name: "empty app names", meetingApp: "Code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := *session
			row.AppNames, row.MeetingApp = tc.appNames, tc.meetingApp
			wantPolicy, err := a.activitySessionAllowsWithPolicyAndInputs(a.activity.store, row, policy, nil)
			if err != nil {
				t.Fatal(err)
			}
			cache := newPeopleReadGateCache()
			gotPolicy, err := a.activitySessionAllowsWithPolicyAndInputsAndCache(a.activity.store, row, policy, nil, cache)
			if err != nil {
				t.Fatal(err)
			}
			if gotPolicy != wantPolicy {
				t.Fatalf("cached policy verdict = %t, uncached = %t", gotPolicy, wantPolicy)
			}
			wantReadable := a.peopleSessionReadable(context.Background(), row, nil)
			gotReadable := a.peopleSessionReadable(context.Background(), row, cache)
			if gotReadable != wantReadable {
				t.Fatalf("cached readable verdict = %t, uncached = %t", gotReadable, wantReadable)
			}
		})
	}
}

func TestPeopleViewsMatchWithAndWithoutSessionPreload(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	wantOverview, err := a.listPeopleOverview(nil)
	if err != nil {
		t.Fatal(err)
	}
	gotOverview, err := a.listPeopleOverview(newPeopleReadGateCache())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotOverview, wantOverview) {
		t.Fatal("ListPeopleOverview changed with session preload enabled")
	}

	principals, err := a.store.ListPrincipals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) == 0 {
		t.Fatal("fixture has no principals")
	}
	wantDetail, err := a.getPersonDetail(principals[0].ID, newPeopleReadGateCache())
	if err != nil {
		t.Fatal(err)
	}
	cache := newPeopleReadGateCache()
	a.preloadPeopleSessionRows(context.Background(), principals, cache)
	gotDetail, err := a.getPersonDetail(principals[0].ID, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDetail, wantDetail) {
		t.Fatalf("GetPersonDetail changed with session preload enabled:\n got=%+v\nwant=%+v", gotDetail, wantDetail)
	}
}

func TestListPrincipalsWithStatsMemoizesPolicyStatePerRead(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	probe := instrumentPeopleUIStateReads(t, a)

	first, err := a.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	assertPeopleUIStateReadCounts(t, probe)
	if row, ok := peopleStatsForName(first, "Batch Person 04"); !ok || !peopleStatsAreZero(row.Stats) {
		t.Fatalf("sensitive app was readable before its policy changed: %+v", row.Stats)
	}

	if err := a.store.SetUIState(context.Background(), "sensitive_apps", `{}`); err != nil {
		t.Fatal(err)
	}
	probe.reset()
	second, err := a.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	assertPeopleUIStateReadCounts(t, probe)
	if row, ok := peopleStatsForName(second, "Batch Person 04"); !ok || row.Stats.TotalMessages == 0 {
		t.Fatalf("second read did not observe the updated sensitive-app policy: %+v", row.Stats)
	}
}

func TestActivityReadPrivacyPolicyIsSharedOnlyWithinRead(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	a.saveObservationPauses([]observationPause{{Start: 123}})
	cache := newPeopleReadGateCache()
	first, err := a.activityReadPrivacyPolicyContextWithPeopleCache(context.Background(), cache)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.activityReadPrivacyPolicyContextWithPeopleCache(context.Background(), cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.pauses) != 1 || &first.pauses[0] != &second.pauses[0] {
		t.Fatal("policy pause slice was rebuilt within one read")
	}
	a.saveObservationPauses([]observationPause{{Start: 456}})
	next, err := a.activityReadPrivacyPolicyContextWithPeopleCache(context.Background(), newPeopleReadGateCache())
	if err != nil {
		t.Fatal(err)
	}
	if len(next.pauses) != 1 || next.pauses[0].Start != 456 {
		t.Fatalf("next read retained stale pauses: %+v", next.pauses)
	}
}

func TestListPrincipalsWithStatsRefreshesProvenanceAfterEventInsert(t *testing.T) {
	a := newPeopleStatsBatchFixture(t)
	first, err := a.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := peopleStatsForName(first, "Batch Person 00"); !ok || peopleStatsAreZero(row.Stats) {
		t.Fatalf("readable source did not produce stats before event change: %+v", row.Stats)
	}

	scansBefore := a.activity.store.readPrivacySources.sourceScans.Load()
	eventRowsBefore := a.activity.store.readPrivacySources.eventRows.Load()
	if _, err := a.activity.store.db.ExecContext(context.Background(), `INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order)
		VALUES('batch-readable',0,'focus','{"privateWindow":true,"windowTitle":"Private"}',1)`); err != nil {
		t.Fatal(err)
	}
	second, err := a.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := peopleStatsForName(second, "Batch Person 00"); !ok || !peopleStatsAreZero(row.Stats) {
		t.Fatalf("second read used stale provenance after event insert: %+v", row.Stats)
	}
	if got := a.activity.store.readPrivacySources.sourceScans.Load() - scansBefore; got != 1 {
		t.Fatalf("event change rescanned %d sources, want only the stale session", got)
	}
	if got := a.activity.store.readPrivacySources.eventRows.Load() - eventRowsBefore; got != 2 {
		t.Fatalf("event change reread %d event rows, want both rows for the stale session", got)
	}
}

func assertPeopleUIStateReadCounts(t *testing.T, probe *peopleUIStateReadProbe) {
	t.Helper()
	counts := probe.countsSnapshot()
	for key, count := range counts {
		if count > 1 {
			t.Errorf("ui_state key %q read %d times in one People read", key, count)
		}
	}
	for _, key := range []string{"sensitive_apps", observationPausesKey} {
		if counts[key] != 1 {
			t.Errorf("ui_state key %q read %d times in one People read, want exactly once", key, counts[key])
		}
	}
}

type peopleBatchFailureConnector struct {
	driver driver.Driver
	path   string
	needle string
	failed *atomic.Bool
}

func (c peopleBatchFailureConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return peopleBatchFailureConn{Conn: conn, needle: c.needle, failed: c.failed}, nil
}

func (c peopleBatchFailureConnector) Driver() driver.Driver { return c.driver }

type peopleBatchFailureConn struct {
	driver.Conn
	needle string
	failed *atomic.Bool
}

func (c peopleBatchFailureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, c.needle) && c.failed.CompareAndSwap(false, true) {
		return nil, errors.New("injected People batch query failure")
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func withPeopleBatchFailure(t *testing.T, a *App, database, needle string) *atomic.Bool {
	t.Helper()
	failed := &atomic.Bool{}
	instrument := func(original *sql.DB, replace func(*sql.DB)) {
		db := sql.OpenDB(peopleBatchFailureConnector{
			driver: original.Driver(), path: peopleProfileDBPath(t, original), needle: needle, failed: failed,
		})
		db.SetMaxOpenConns(original.Stats().MaxOpenConnections)
		replace(db)
		t.Cleanup(func() {
			replace(original)
			_ = db.Close()
		})
	}
	if database == "activity" {
		store := a.activity.store
		if store.reader() != store.db {
			instrument(store.readDB, func(db *sql.DB) { store.readDB = db })
			instrument(store.db, func(db *sql.DB) { store.db, store.readWriter = db, db })
		} else {
			instrument(store.db, func(db *sql.DB) { store.db = db })
		}
	} else {
		instrument(peopleProfileStorageDB(a.store), func(db *sql.DB) { setPeopleProfileStorageDB(a.store, db) })
		instrument(peopleProfileStorageReadDB(a.store), func(db *sql.DB) { setPeopleProfileStorageReadDB(a.store, db) })
	}
	return failed
}

func TestListPrincipalsWithStatsBatchFailureFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, database, needle string
	}{
		{"handles", "state", "ORDER BY principal_id, app, display_handle"},
		{"contacts", "state", "SELECT principal_id, external_ref"},
		{"sightings", "state", "SELECT pss.principal_id, pss.session_id"},
		{"provenance metadata", "activity", "SELECT s.id,s.start_time"},
		{"provenance", "activity", "SELECT session_id, data FROM activity_events WHERE session_id IN"},
		{"provenance snapshots", "activity", "SELECT session_id, COALESCE(app, '')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newPeopleStatsBatchFixture(t)
			failed := withPeopleBatchFailure(t, a, tc.database, tc.needle)
			got, err := a.listPrincipalsWithStats(newPeopleReadGateCache())
			if err != nil {
				t.Fatal(err)
			}
			if !failed.Load() {
				t.Fatal("batch query failure was not injected")
			}
			want, err := listPrincipalsWithStatsReference(a)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("batch fallback differs from per-person reference:\n got=%+v\nwant=%+v", got, want)
			}
		})
	}
}
