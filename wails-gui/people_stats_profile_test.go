package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

const (
	peopleProfileFixtureHomeEnv = "YANSU_PEOPLE_PROFILE_FIXTURE_HOME"
	peopleProfileRunEnv         = "ISOFORM_PEOPLE_PERF_FIXTURE"
	peopleProfileFixtureMarker  = "people-profile-fixture-v1.json"
	peopleProfilePrincipalCount = 1500
	peopleProfileSessionCount   = 8000
	peopleProfileSightingCount  = 20000
	peopleProfileEventCount     = 60000
)

type peopleProfileFixture struct {
	app          *App
	principalIDs []string
}

func newPeopleProfileFixture(tb testing.TB) *peopleProfileFixture {
	tb.Helper()
	priorSensitiveApps := pii.GetUserSensitiveApps()
	pii.SetUserSensitiveApps(map[string]string{})
	tb.Cleanup(func() { pii.SetUserSensitiveApps(priorSensitiveApps) })

	app := newPeopleBenchApp(tb)
	home := os.Getenv("HOME")
	marker := filepath.Join(home, peopleProfileFixtureMarker)
	if _, err := os.Stat(marker); err == nil {
		fixture := &peopleProfileFixture{app: app, principalIDs: peopleProfilePrincipalIDs()}
		validatePeopleProfileFixture(tb, app)
		return fixture
	} else if !os.IsNotExist(err) {
		tb.Fatal(err)
	}

	ids := peopleProfilePrincipalIDs()
	seedPeopleProfileState(tb, app, ids)
	seedPeopleProfileVoiceSamples(tb, app, ids[0])
	seedPeopleProfileActivity(tb, app)
	seedPeopleProfileContextFiles(tb)
	validatePeopleProfileFixture(tb, app)
	if err := os.WriteFile(marker, []byte(`{"version":1}`), 0600); err != nil {
		tb.Fatal(err)
	}
	return &peopleProfileFixture{app: app, principalIDs: ids}
}

func validatePeopleProfileFixture(tb testing.TB, app *App) {
	tb.Helper()
	for table, want := range map[string]int64{
		"principals":                  peopleProfilePrincipalCount,
		"app_handles":                 3750,
		"principal_sightings":         peopleProfilePrincipalCount * 3,
		"principal_session_sightings": peopleProfileSightingCount,
		"principal_contact_links":     peopleProfilePrincipalCount / 20,
		"imported_contact_cards":      peopleProfilePrincipalCount / 20,
	} {
		var got int64
		if err := peopleProfileStorageDB(app.store).QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			tb.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			tb.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
	for table, want := range map[string]int64{
		"activity_sessions": peopleProfileSessionCount,
		"activity_events":   peopleProfileEventCount,
	} {
		var got int64
		if err := app.activity.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			tb.Fatalf("count activity %s: %v", table, err)
		}
		if got != want {
			tb.Fatalf("activity %s rows = %d, want %d", table, got, want)
		}
	}
}

func seedPeopleProfileVoiceSamples(tb testing.TB, app *App, principalID string) {
	tb.Helper()
	for i := range 3 {
		_, err := app.store.CreateVoiceEmbedding(context.Background(), storage.VoiceEmbeddingRow{
			PrincipalID:  principalID,
			Embedding:    []float32{0.1, 0.2, 0.3, float32(i)},
			SampleRate:   16000,
			AudioSeconds: 4,
			Label:        fmt.Sprintf("fixture sample %d", i),
			CapturedAt:   time.Now().Add(time.Duration(i) * time.Minute).UnixMilli(),
		})
		if err != nil {
			tb.Fatal(err)
		}
	}
}

func peopleProfilePrincipalIDs() []string {
	ids := make([]string, peopleProfilePrincipalCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("people-profile-%04d", i)
	}
	return ids
}

func seedPeopleProfileState(tb testing.TB, app *App, ids []string) {
	tb.Helper()
	db := peopleProfileStorageDB(app.store)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()

	principalStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO principals
			(id, kind, display_name, relationship, notes, created_from, confidence, created_at, updated_at, source, external_ref, merged_into)
		VALUES (?, 'contact', ?, 'colleague', '', 'people_profile_fixture', 1.0, ?, ?, 'people_profile_fixture', '', '')`)
	if err != nil {
		tb.Fatal(err)
	}
	defer principalStmt.Close()
	handleStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO app_handles (principal_id, app, handle_id, display_handle, created_at)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer handleStmt.Close()
	sightingStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO principal_sightings (principal_id, app, message_count, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer sightingStmt.Close()
	sessionSightingStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO principal_session_sightings (principal_id, session_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer sessionSightingStmt.Close()

	base := time.Now().Add(-7 * 24 * time.Hour).Truncate(time.Minute).UnixMilli()
	handleApps := []string{"discord", "email", "slack", "telegram"}
	sightingApps := []string{"discord", "slack", "telegram"}
	for i, id := range ids {
		at := base + int64(i)*1000
		name := fmt.Sprintf("Profile Person %04d", i)
		if _, err := principalStmt.ExecContext(context.Background(), id, name, at, at); err != nil {
			tb.Fatalf("insert principal %s: %v", id, err)
		}
		handleCount := 1 + i%4
		for handle := 0; handle < handleCount; handle++ {
			appName := handleApps[(i+handle)%len(handleApps)]
			handleID := fmt.Sprintf("profile-%04d-%d", i, handle)
			if _, err := handleStmt.ExecContext(context.Background(), id, appName, handleID, name, at); err != nil {
				tb.Fatalf("insert handle %s/%s: %v", id, appName, err)
			}
		}
		for appIndex, appName := range sightingApps {
			first := at + int64(appIndex)*100
			last := first + int64(1000+i%100)
			if _, err := sightingStmt.ExecContext(context.Background(), id, appName, 10+i%40, first, last); err != nil {
				tb.Fatalf("insert sighting %s/%s: %v", id, appName, err)
			}
		}
	}

	for i := 0; i < peopleProfileSightingCount; i++ {
		principalIndex := i % len(ids)
		sessionIndex := i % peopleProfileSessionCount
		at := base + int64(sessionIndex)*60_000 + int64(principalIndex%60_000)
		sessionID := fmt.Sprintf("people-profile-session-%05d", sessionIndex)
		if _, err := sessionSightingStmt.ExecContext(context.Background(), ids[principalIndex], sessionID, at, at+1000); err != nil {
			tb.Fatalf("insert principal session sighting %d: %v", i, err)
		}
	}

	cardStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO imported_contact_cards (external_ref, payload) VALUES (?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer cardStmt.Close()
	linkStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO principal_contact_links (principal_id, external_ref, receipt) VALUES (?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer linkStmt.Close()
	for i := 0; i < len(ids); i += 20 {
		ref := fmt.Sprintf("people-profile-contact-%04d", i)
		card, err := json.Marshal(systemContact{
			ID:       ref,
			FullName: fmt.Sprintf("Imported Profile Person %04d", i),
			Phones:   []string{fmt.Sprintf("+1555000%04d", i)},
			Emails:   []string{fmt.Sprintf("person-%04d@example.test", i)},
		})
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := cardStmt.ExecContext(context.Background(), ref, string(card)); err != nil {
			tb.Fatalf("insert imported contact %s: %v", ref, err)
		}
		if _, err := linkStmt.ExecContext(context.Background(), ids[i], ref, `{"fixture":true}`); err != nil {
			tb.Fatalf("link imported contact %s: %v", ids[i], err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func seedPeopleProfileActivity(tb testing.TB, app *App) {
	tb.Helper()
	db := app.activity.store.db
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()

	sessionStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO activity_sessions
			(id, start_time, end_time, trigger_kind, app_names, event_count, segment_count,
			 snapshot_count, total_size_bytes, triage_status, is_meeting, meeting_app,
			 transcript_path, audio_path, audio_size_bytes, created_at)
		VALUES (?, ?, ?, 'focus', '["Code"]', ?, 0, 0, 0, 'done', 0, '', '', '', 0, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer sessionStmt.Close()
	eventStmt, err := tx.PrepareContext(context.Background(), `
		INSERT INTO activity_events (session_id, timestamp, kind, data, sort_order)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer eventStmt.Close()

	base := time.Now().Add(-7 * 24 * time.Hour).Truncate(time.Minute).UnixMilli()
	for i := 0; i < peopleProfileSessionCount; i++ {
		sessionID := fmt.Sprintf("people-profile-session-%05d", i)
		start := base + int64(i)*60_000
		count := 7
		if i < peopleProfileSessionCount/2 {
			count++
		}
		end := start + int64(count)*1000
		if _, err := sessionStmt.ExecContext(context.Background(), sessionID, start, end, count, end); err != nil {
			tb.Fatalf("insert activity session %s: %v", sessionID, err)
		}
		for eventIndex := 0; eventIndex < count; eventIndex++ {
			eventAt := start + int64(eventIndex)*1000
			eventJSON, err := json.Marshal(activitymon.ActivityEvent{
				Kind:        activitymon.EventFocus,
				Timestamp:   eventAt,
				AppName:     "Code",
				AppBundleID: "com.example.code",
				WindowTitle: "People performance fixture",
			})
			if err != nil {
				tb.Fatal(err)
			}
			if _, err := eventStmt.ExecContext(context.Background(), sessionID, eventAt, string(activitymon.EventFocus), string(eventJSON), eventIndex); err != nil {
				tb.Fatalf("insert activity event %s/%d: %v", sessionID, eventIndex, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func seedPeopleProfileContextFiles(tb testing.TB) {
	tb.Helper()
	memory, err := memoryDir()
	if err != nil {
		tb.Fatal(err)
	}
	knowledge, err := knowledgeDir()
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 160; i++ {
		if err := os.WriteFile(filepath.Join(memory, fmt.Sprintf("profile-memory-%03d.md", i)),
			[]byte(fmt.Sprintf("# Memory %03d\nProfile fixture context for directory-stamp timing.\n", i)), 0600); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(knowledge, fmt.Sprintf("profile-knowledge-%03d.md", i)),
			[]byte(fmt.Sprintf("# Knowledge %03d\n**Date**: 2026-01-01\n**Apps**: Code\nFixture context for directory-stamp timing.\n", i)), 0600); err != nil {
			tb.Fatal(err)
		}
	}
}

func peopleProfileStorageDB(store *storage.Store) *sql.DB {
	return peopleProfileStorageField(store, "db")
}

func setPeopleProfileStorageDB(store *storage.Store, db *sql.DB) {
	setPeopleProfileStorageField(store, "db", db)
}

// The store's read pool (streamReadDB) serves UI-state reads, so a probe
// counts reads on it as well as on the writer.
func peopleProfileStorageReadDB(store *storage.Store) *sql.DB {
	return peopleProfileStorageField(store, "streamReadDB")
}

func setPeopleProfileStorageReadDB(store *storage.Store, db *sql.DB) {
	setPeopleProfileStorageField(store, "streamReadDB", db)
}

func peopleProfileStorageField(store *storage.Store, name string) *sql.DB {
	field := reflect.ValueOf(store).Elem().FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Ptr {
		panic("storage.Store database field unavailable")
	}
	return *(**sql.DB)(unsafe.Pointer(field.UnsafeAddr()))
}

func setPeopleProfileStorageField(store *storage.Store, name string, db *sql.DB) {
	field := reflect.ValueOf(store).Elem().FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Ptr {
		panic("storage.Store database field unavailable")
	}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(db))
}

type peopleUIStateRead struct {
	key, caller string
	count       int
}

type peopleUIStateReadProbe struct {
	mu          sync.Mutex
	counts      map[string]int
	callers     map[string]int
	queryCounts map[string]int
}

type peopleSQLStatementKey struct {
	database string
	query    string
}

type peopleSQLStatementMeasurement struct {
	database   string
	query      string
	executions int
	rowsTotal  int64
	minRows    int64
	maxRows    int64
	duration   time.Duration
}

type peopleSQLTimingProbe struct {
	mu         sync.Mutex
	statements map[peopleSQLStatementKey]*peopleSQLStatementMeasurement
}

func newPeopleSQLTimingProbe() *peopleSQLTimingProbe {
	return &peopleSQLTimingProbe{statements: map[peopleSQLStatementKey]*peopleSQLStatementMeasurement{}}
}

func normalizePeopleSQL(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(query))), " ")
}

func (p *peopleSQLTimingProbe) record(database, query string, duration time.Duration, rows int64) {
	key := peopleSQLStatementKey{database: database, query: normalizePeopleSQL(query)}
	p.mu.Lock()
	defer p.mu.Unlock()
	measurement := p.statements[key]
	if measurement == nil {
		measurement = &peopleSQLStatementMeasurement{
			database: database,
			query:    key.query,
			minRows:  rows,
			maxRows:  rows,
		}
		p.statements[key] = measurement
	}
	measurement.executions++
	measurement.rowsTotal += rows
	if rows < measurement.minRows {
		measurement.minRows = rows
	}
	if rows > measurement.maxRows {
		measurement.maxRows = rows
	}
	measurement.duration += duration
}

func (p *peopleSQLTimingProbe) snapshot() []peopleSQLStatementMeasurement {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]peopleSQLStatementMeasurement, 0, len(p.statements))
	for _, measurement := range p.statements {
		result = append(result, *measurement)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].duration != result[j].duration {
			return result[i].duration > result[j].duration
		}
		if result[i].database != result[j].database {
			return result[i].database < result[j].database
		}
		return result[i].query < result[j].query
	})
	return result
}

func (p *peopleSQLTimingProbe) reset() {
	p.mu.Lock()
	p.statements = map[peopleSQLStatementKey]*peopleSQLStatementMeasurement{}
	p.mu.Unlock()
}

func newPeopleUIStateReadProbe() *peopleUIStateReadProbe {
	return &peopleUIStateReadProbe{
		counts:      map[string]int{},
		callers:     map[string]int{},
		queryCounts: map[string]int{},
	}
}

func (p *peopleUIStateReadProbe) recordQuery(database string) {
	p.mu.Lock()
	p.queryCounts[database]++
	p.mu.Unlock()
}

func (p *peopleUIStateReadProbe) record(key string) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var callerFrames []string
	for len(callerFrames) < 6 {
		frame, more := frames.Next()
		name := frame.Function
		if name != "" &&
			!strings.HasPrefix(name, "runtime.") &&
			!strings.HasPrefix(name, "database/sql.") &&
			!strings.HasPrefix(name, "database/sql/") &&
			!strings.HasPrefix(name, "modernc.org/sqlite.") &&
			!strings.Contains(name, "peopleUIStateCountingConn.QueryContext") {
			callerFrames = append(callerFrames, name)
		}
		if !more {
			break
		}
	}
	caller := strings.Join(callerFrames, " <- ")
	p.mu.Lock()
	p.counts[key]++
	p.callers[key+"\x00"+caller]++
	p.mu.Unlock()
}

func (p *peopleUIStateReadProbe) snapshot() []peopleUIStateRead {
	p.mu.Lock()
	defer p.mu.Unlock()
	reads := make([]peopleUIStateRead, 0, len(p.callers))
	for compound, count := range p.callers {
		parts := strings.SplitN(compound, "\x00", 2)
		reads = append(reads, peopleUIStateRead{key: parts[0], caller: parts[1], count: count})
	}
	sort.Slice(reads, func(i, j int) bool {
		if reads[i].key != reads[j].key {
			return reads[i].key < reads[j].key
		}
		return reads[i].caller < reads[j].caller
	})
	return reads
}

func (p *peopleUIStateReadProbe) countsSnapshot() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := make(map[string]int, len(p.counts))
	for key, count := range p.counts {
		counts[key] = count
	}
	return counts
}

func (p *peopleUIStateReadProbe) queryCountsSnapshot() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := make(map[string]int, len(p.queryCounts))
	for database, count := range p.queryCounts {
		counts[database] = count
	}
	return counts
}

func (p *peopleUIStateReadProbe) reset() {
	p.mu.Lock()
	p.counts = map[string]int{}
	p.callers = map[string]int{}
	p.queryCounts = map[string]int{}
	p.mu.Unlock()
}

type peopleUIStateCountingConnector struct {
	driver   driver.Driver
	path     string
	database string
	probe    *peopleUIStateReadProbe
	timings  *peopleSQLTimingProbe
}

func (c peopleUIStateCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return peopleUIStateCountingConn{Conn: conn, database: c.database, probe: c.probe, timings: c.timings}, nil
}

func (c peopleUIStateCountingConnector) Driver() driver.Driver { return c.driver }

type peopleUIStateCountingConn struct {
	driver.Conn
	database string
	probe    *peopleUIStateReadProbe
	timings  *peopleSQLTimingProbe
}

func (c peopleUIStateCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.probe.recordQuery(c.database)
	if strings.EqualFold(strings.TrimSpace(query), "SELECT value FROM ui_state WHERE key = ?") && len(args) > 0 {
		if key, ok := args[0].Value.(string); ok {
			c.probe.record(key)
		}
	}
	started := time.Now()
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		if c.timings != nil {
			c.timings.record(c.database, query, time.Since(started), 0)
		}
		return nil, err
	}
	if c.timings == nil {
		return rows, nil
	}
	return &peopleSQLTimingRows{
		Rows:     rows,
		probe:    c.timings,
		database: c.database,
		query:    query,
		started:  started,
	}, nil
}

func (c peopleUIStateCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	started := time.Now()
	result, err := execer.ExecContext(ctx, query, args)
	if c.timings != nil {
		c.timings.record(c.database, query, time.Since(started), 0)
	}
	return result, err
}

type peopleSQLTimingRows struct {
	driver.Rows
	probe    *peopleSQLTimingProbe
	database string
	query    string
	started  time.Time
	rows     int64
	close    sync.Once
}

func (r *peopleSQLTimingRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == nil {
		r.rows++
	}
	return err
}

func (r *peopleSQLTimingRows) Close() error {
	err := r.Rows.Close()
	r.close.Do(func() {
		r.probe.record(r.database, r.query, time.Since(r.started), r.rows)
	})
	return err
}

func peopleProfileDBPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var sequence int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func instrumentPeopleUIStateReads(t *testing.T, app *App) *peopleUIStateReadProbe {
	t.Helper()
	original := peopleProfileStorageDB(app.store)
	path := peopleProfileDBPath(t, original)
	probe := newPeopleUIStateReadProbe()
	instrumented := sql.OpenDB(peopleUIStateCountingConnector{driver: original.Driver(), path: path, database: "state", probe: probe})
	instrumented.SetMaxOpenConns(1)
	setPeopleProfileStorageDB(app.store, instrumented)
	originalRead := peopleProfileStorageReadDB(app.store)
	instrumentedRead := sql.OpenDB(peopleUIStateCountingConnector{driver: original.Driver(), path: path, database: "state", probe: probe})
	setPeopleProfileStorageReadDB(app.store, instrumentedRead)
	t.Cleanup(func() {
		setPeopleProfileStorageDB(app.store, original)
		setPeopleProfileStorageReadDB(app.store, originalRead)
		_ = instrumented.Close()
		_ = instrumentedRead.Close()
	})
	return probe
}

func instrumentPeopleProfileQueryCounts(t *testing.T, app *App) *peopleUIStateReadProbe {
	probe, _ := instrumentPeopleProfileQueries(t, app, nil)
	return probe
}

func instrumentPeopleProfileSQLTimings(t *testing.T, app *App) (*peopleUIStateReadProbe, *peopleSQLTimingProbe) {
	timings := newPeopleSQLTimingProbe()
	probe, _ := instrumentPeopleProfileQueries(t, app, timings)
	return probe, timings
}

func instrumentPeopleProfileQueries(t *testing.T, app *App, timings *peopleSQLTimingProbe) (*peopleUIStateReadProbe, *peopleSQLTimingProbe) {
	t.Helper()
	stateDB := peopleProfileStorageDB(app.store)
	activityStore := app.activity.store
	activityDB := activityStore.reader()
	activityWriterDB := activityStore.db
	activityReadDB := activityStore.readDB
	activityReadWriter := activityStore.readWriter
	statePath := peopleProfileDBPath(t, stateDB)
	activityPath := peopleProfileDBPath(t, activityDB)
	probe := newPeopleUIStateReadProbe()
	instrumentedState := sql.OpenDB(peopleUIStateCountingConnector{
		driver: stateDB.Driver(), path: statePath, database: "state", probe: probe, timings: timings,
	})
	instrumentedActivity := sql.OpenDB(peopleUIStateCountingConnector{
		driver: activityDB.Driver(), path: activityPath, database: "activity", probe: probe, timings: timings,
	})
	instrumentedState.SetMaxOpenConns(1)
	instrumentedActivity.SetMaxOpenConns(1)
	separateActivityReader := activityDB != activityWriterDB
	var instrumentedActivityWriter *sql.DB
	if separateActivityReader {
		activityWriterPath := peopleProfileDBPath(t, activityWriterDB)
		instrumentedActivityWriter = sql.OpenDB(peopleUIStateCountingConnector{
			driver: activityWriterDB.Driver(), path: activityWriterPath, database: "activity", probe: probe, timings: timings,
		})
		instrumentedActivityWriter.SetMaxOpenConns(1)
	}
	setPeopleProfileStorageDB(app.store, instrumentedState)
	stateReadDB := peopleProfileStorageReadDB(app.store)
	instrumentedStateRead := sql.OpenDB(peopleUIStateCountingConnector{
		driver: stateDB.Driver(), path: statePath, database: "state", probe: probe, timings: timings,
	})
	setPeopleProfileStorageReadDB(app.store, instrumentedStateRead)
	if separateActivityReader {
		activityStore.readDB = instrumentedActivity
		activityStore.db = instrumentedActivityWriter
		activityStore.readWriter = instrumentedActivityWriter
	} else {
		activityStore.db = instrumentedActivity
	}
	t.Cleanup(func() {
		setPeopleProfileStorageDB(app.store, stateDB)
		setPeopleProfileStorageReadDB(app.store, stateReadDB)
		_ = instrumentedStateRead.Close()
		activityStore.db = activityWriterDB
		activityStore.readDB = activityReadDB
		activityStore.readWriter = activityReadWriter
		_ = instrumentedState.Close()
		_ = instrumentedActivity.Close()
		if instrumentedActivityWriter != nil {
			_ = instrumentedActivityWriter.Close()
		}
	})
	return probe, timings
}

type peopleStatsPhaseTimes struct {
	stamp, principals, stats, preload, handles time.Duration
	loop, gates, dto, decorate, lockValidate   time.Duration
}

func profilePeopleStatsPhases(app *App) (peopleStatsPhaseTimes, []PrincipalWithStatsDTO, error) {
	ctx := context.Background()
	var phases peopleStatsPhaseTimes
	started := time.Now()
	stamp, err := app.peopleReadStamp(ctx)
	phases.stamp = time.Since(started)
	if err != nil {
		return phases, nil, err
	}
	store, err := app.requireStore()
	if err != nil {
		return phases, nil, err
	}
	started = time.Now()
	principals, err := store.ListPrincipals(ctx)
	phases.principals = time.Since(started)
	if err != nil {
		return phases, nil, err
	}
	started = time.Now()
	statsMap, err := store.GetAllPrincipalStats(ctx)
	phases.stats = time.Since(started)
	if err != nil {
		return phases, nil, err
	}
	cache := newPeopleReadGateCache()
	started = time.Now()
	app.preloadPeopleSessionRows(ctx, principals, cache)
	phases.preload = time.Since(started)
	started = time.Now()
	handlesByPrincipal, handlesErr := store.ListAppHandlesByPrincipals(ctx)
	phases.handles = time.Since(started)
	newGateCache := func() *peopleReadGateCache {
		result := newPeopleReadGateCache()
		result.sightings = cache.sightings
		result.sessionRows = cache.sessionRows
		result.sources = cache.sources
		return result
	}
	statsFor := func(principal storage.PrincipalRow) storage.PrincipalStatsRow {
		stats := statsMap[principal.ID]
		if stats.PrincipalID == "" {
			stats.PrincipalID = principal.ID
		}
		return stats
	}
	gatedStats := make([]storage.PrincipalStatsRow, len(principals))
	gateCache := newGateCache()
	started = time.Now()
	for i, principal := range principals {
		gatedStats[i] = app.peopleStatsReadable(ctx, principal, statsFor(principal), gateCache)
	}
	phases.gates = time.Since(started)
	started = time.Now()
	dtoRows := make([]PrincipalWithStatsDTO, len(principals))
	for i, principal := range principals {
		dto := principalToDTO(principal)
		var handles []storage.AppHandleRow
		if handlesErr != nil {
			handles, err = store.ListAppHandlesByPrincipal(ctx, principal.ID)
			if err != nil {
				return phases, nil, fmt.Errorf("list handles for %s: %w", principal.ID, err)
			}
		} else {
			handles = handlesByPrincipal[principal.ID]
		}
		dto.attachHandles(handles)
		dtoRows[i] = PrincipalWithStatsDTO{Principal: dto, Stats: principalStatsToDTO(gatedStats[i])}
	}
	runtime.KeepAlive(dtoRows)
	phases.dto = time.Since(started)
	out := make([]PrincipalWithStatsDTO, 0, len(principals))
	loopCache := newGateCache()
	started = time.Now()
	for _, principal := range principals {
		dto := principalToDTO(principal)
		var handles []storage.AppHandleRow
		if handlesErr != nil {
			handles, err = store.ListAppHandlesByPrincipal(ctx, principal.ID)
			if err != nil {
				return phases, nil, fmt.Errorf("list handles for %s: %w", principal.ID, err)
			}
		} else {
			handles = handlesByPrincipal[principal.ID]
		}
		dto.attachHandles(handles)
		out = append(out, PrincipalWithStatsDTO{
			Principal: dto,
			Stats:     principalStatsToDTO(app.peopleStatsReadable(ctx, principal, statsFor(principal), loopCache)),
		})
	}
	phases.loop = time.Since(started)
	dtos := make([]*PrincipalDTO, len(out))
	for i := range out {
		dtos[i] = &out[i].Principal
	}
	started = time.Now()
	if err := decorateContactDTOs(ctx, store, dtos); err != nil {
		return phases, nil, err
	}
	phases.decorate = time.Since(started)
	started = time.Now()
	release, err := app.lockPeoplePublication(ctx, stamp)
	phases.lockValidate = time.Since(started)
	if err != nil {
		return phases, nil, err
	}
	release()
	return phases, out, nil
}

func TestPeopleProfileWarmSQLAndGoPhaseAttribution(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to run the opt-in People profile")
	}
	fixture := newPeopleProfileFixture(t)
	probe, timings := instrumentPeopleProfileSQLTimings(t, fixture.app)
	if _, err := fixture.app.ListPrincipalsWithStats(); err != nil {
		t.Fatal(err)
	}
	probe.reset()
	timings.reset()
	store := fixture.app.activity.store
	sourceScans := store.readPrivacySources.sourceScans.Load()
	eventRows := store.readPrivacySources.eventRows.Load()
	snapshotRows := store.readPrivacySources.snapshotRows.Load()
	started := time.Now()
	rows, err := fixture.app.ListPrincipalsWithStats()
	wall := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != peopleProfilePrincipalCount {
		t.Fatalf("profile rows = %d, want %d", len(rows), peopleProfilePrincipalCount)
	}
	if got := store.readPrivacySources.sourceScans.Load() - sourceScans; got != 0 {
		t.Fatalf("warm source scans = %d, want 0", got)
	}
	if got := store.readPrivacySources.eventRows.Load() - eventRows; got != 0 {
		t.Fatalf("warm event rows = %d, want 0", got)
	}
	if got := store.readPrivacySources.snapshotRows.Load() - snapshotRows; got != 0 {
		t.Fatalf("warm snapshot rows = %d, want 0", got)
	}
	counts := probe.queryCountsSnapshot()
	t.Logf("warm ListPrincipalsWithStats wall=%s state_queries=%d activity_queries=%d source_scans=0 event_rows=0 snapshot_rows=0",
		wall, counts["state"], counts["activity"])
	for index, statement := range timings.snapshot() {
		if index == 20 {
			break
		}
		rowsPerExecution := float64(statement.rowsTotal) / float64(statement.executions)
		t.Logf("SQL db=%s total_ms=%.3f executions=%d rows_total=%d rows_per_execution=%.1f rows_min=%d rows_max=%d query=%s",
			statement.database, float64(statement.duration)/float64(time.Millisecond), statement.executions,
			statement.rowsTotal, rowsPerExecution, statement.minRows, statement.maxRows, statement.query)
	}
	phases, phaseRows, err := profilePeopleStatsPhases(fixture.app)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, phaseRows) {
		t.Fatal("timed phase wrapper differs from ListPrincipalsWithStats")
	}
	t.Logf("Go phases ms stamp=%.3f principals=%.3f stats=%.3f preload=%.3f handles=%.3f loop=%.3f gates=%.3f dto=%.3f decorate=%.3f lock_validate=%.3f",
		float64(phases.stamp)/float64(time.Millisecond), float64(phases.principals)/float64(time.Millisecond),
		float64(phases.stats)/float64(time.Millisecond), float64(phases.preload)/float64(time.Millisecond),
		float64(phases.handles)/float64(time.Millisecond), float64(phases.loop)/float64(time.Millisecond),
		float64(phases.gates)/float64(time.Millisecond), float64(phases.dto)/float64(time.Millisecond),
		float64(phases.decorate)/float64(time.Millisecond), float64(phases.lockValidate)/float64(time.Millisecond))
	planRows, err := peopleProfileStorageDB(fixture.app.store).QueryContext(context.Background(), `
		EXPLAIN QUERY PLAN
		SELECT pss.principal_id, pss.session_id, pss.first_seen_at, pss.last_seen_at
		  FROM principal_session_sightings pss
		 WHERE EXISTS (SELECT 1 FROM principals p WHERE p.id=pss.principal_id AND p.merged_into='')`)
	if err != nil {
		t.Fatal(err)
	}
	usedTempSort := false
	for planRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		t.Logf("session sightings query plan id=%d parent=%d detail=%s", id, parent, detail)
		usedTempSort = usedTempSort || strings.Contains(detail, "USE TEMP B-TREE")
	}
	if err := planRows.Err(); err != nil {
		planRows.Close()
		t.Fatal(err)
	}
	if err := planRows.Close(); err != nil {
		t.Fatal(err)
	}
	if usedTempSort {
		t.Fatal("principal session sightings query still uses a temporary B-tree")
	}
}

func TestPeopleProfileUIStateReadAttribution(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to run the opt-in People profile")
	}
	fixture := newPeopleProfileFixture(t)
	probe := instrumentPeopleProfileQueryCounts(t, fixture.app)

	rows, err := fixture.app.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != peopleProfilePrincipalCount {
		t.Fatalf("profile rows = %d, want %d", len(rows), peopleProfilePrincipalCount)
	}
	queryCounts := probe.queryCountsSnapshot()
	t.Logf("query counts state=%d activity=%d", queryCounts["state"], queryCounts["activity"])
	for _, read := range probe.snapshot() {
		t.Logf("ui_state key=%q reads=%d caller=%s", read.key, read.count, read.caller)
	}
}

func TestPeopleProfileWarmProvenanceCacheHits(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to run the opt-in People profile")
	}
	fixture := newPeopleProfileFixture(t)
	store := fixture.app.activity.store
	probe := instrumentPeopleProfileQueryCounts(t, fixture.app)
	scansBefore := store.readPrivacySources.sourceScans.Load()
	eventsBefore := store.readPrivacySources.eventRows.Load()
	snapshotsBefore := store.readPrivacySources.snapshotRows.Load()
	first, err := fixture.app.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != peopleProfilePrincipalCount {
		t.Fatalf("first call rows = %d, want %d", len(first), peopleProfilePrincipalCount)
	}
	if got := store.readPrivacySources.sourceScans.Load() - scansBefore; got != peopleProfileSessionCount {
		t.Fatalf("cold source scans = %d, want %d", got, peopleProfileSessionCount)
	}
	if got := store.readPrivacySources.eventRows.Load() - eventsBefore; got != peopleProfileEventCount {
		t.Fatalf("cold event rows = %d, want %d", got, peopleProfileEventCount)
	}
	if got := store.readPrivacySources.snapshotRows.Load() - snapshotsBefore; got != 0 {
		t.Fatalf("fixture snapshot rows = %d, want 0", got)
	}
	if got := activityPrivacySourceCacheEntryCount(store); got != peopleProfileSessionCount {
		t.Fatalf("cold source cache entries = %d, want %d", got, peopleProfileSessionCount)
	}

	probe.reset()
	scansBefore = store.readPrivacySources.sourceScans.Load()
	eventsBefore = store.readPrivacySources.eventRows.Load()
	snapshotsBefore = store.readPrivacySources.snapshotRows.Load()
	second, err := fixture.app.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != peopleProfilePrincipalCount {
		t.Fatalf("warm call rows = %d, want %d", len(second), peopleProfilePrincipalCount)
	}
	if got := store.readPrivacySources.sourceScans.Load() - scansBefore; got != 0 {
		t.Fatalf("warm source scans = %d, want 0", got)
	}
	if got := store.readPrivacySources.eventRows.Load() - eventsBefore; got != 0 {
		t.Fatalf("warm event rows = %d, want 0", got)
	}
	if got := store.readPrivacySources.snapshotRows.Load() - snapshotsBefore; got != 0 {
		t.Fatalf("warm snapshot rows = %d, want 0", got)
	}
	counts := probe.queryCountsSnapshot()
	t.Logf("warm call queries state=%d activity=%d sourceScans=0 eventRows=0 snapshotRows=0 storeCacheEntries=%d",
		counts["state"], counts["activity"], activityPrivacySourceCacheEntryCount(store))
}

func TestPeopleStatsClosedEyesSkipsProvenancePreload(t *testing.T) {
	app := newPeopleStatsBatchFixture(t)
	store := app.activity.store
	if got := activityPrivacySourceCacheEntryCount(store); got != 0 {
		t.Fatalf("initial provenance cache entries = %d, want 0", got)
	}
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.SetClosedEyesModeEnabled(false); err != nil {
			t.Errorf("disable Closed Eyes: %v", err)
		}
	})

	want, err := listPrincipalsWithStatsReference(app)
	if err != nil {
		t.Fatal(err)
	}
	if got := activityPrivacySourceCacheEntryCount(store); got != 0 {
		t.Fatalf("reference populated provenance cache entries = %d, want 0", got)
	}

	sourceScansBefore := store.readPrivacySources.sourceScans.Load()
	eventRowsBefore := store.readPrivacySources.eventRows.Load()
	snapshotRowsBefore := store.readPrivacySources.snapshotRows.Load()
	got, err := app.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Closed Eyes output differs from the per-person reference:\n got=%+v\nwant=%+v", got, want)
	}
	for _, row := range got {
		if !peopleStatsAreZero(row.Stats) {
			t.Fatalf("Closed Eyes left nonzero stats for %s: %+v", row.Principal.DisplayName, row.Stats)
		}
	}
	if delta := store.readPrivacySources.sourceScans.Load() - sourceScansBefore; delta != 0 {
		t.Errorf("Closed Eyes source scans = %d, want 0", delta)
	}
	if delta := store.readPrivacySources.eventRows.Load() - eventRowsBefore; delta != 0 {
		t.Errorf("Closed Eyes event rows = %d, want 0", delta)
	}
	if delta := store.readPrivacySources.snapshotRows.Load() - snapshotRowsBefore; delta != 0 {
		t.Errorf("Closed Eyes snapshot rows = %d, want 0", delta)
	}
	if got := activityPrivacySourceCacheEntryCount(store); got != 0 {
		t.Errorf("Closed Eyes populated provenance cache entries = %d, want 0", got)
	}
}

func activityPrivacySourceCacheEntryCount(store *ActivityStore) int {
	store.readPrivacySources.mu.Lock()
	defer store.readPrivacySources.mu.Unlock()
	return len(store.readPrivacySources.entries)
}

func TestPeopleProfileProvenanceCacheHeap(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to run the opt-in People profile")
	}
	fixture := newPeopleProfileFixture(t)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := fixture.app.ListPrincipalsWithStats(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	actualRetained := uint64(0)
	if after.HeapAlloc > before.HeapAlloc {
		actualRetained = after.HeapAlloc - before.HeapAlloc
	}
	actualEntries := activityPrivacySourceCacheEntryCount(fixture.app.activity.store)
	if actualEntries == 0 {
		t.Fatal("fixture source cache is empty after the People read")
	}
	t.Logf("fixture source cache retained entries=%d heap_bytes=%d bytes_per_entry=%.1f",
		actualEntries, actualRetained, float64(actualRetained)/float64(actualEntries))

	fixtureStore := fixture.app.activity.store
	fixtureStore.readPrivacySources.mu.Lock()
	var sampleState activityPrivacySourceState
	var sampleSource activitySourceProvenance
	found := false
	for _, entry := range fixtureStore.readPrivacySources.entries {
		sampleState, sampleSource, found = entry.state, entry.source, true
		break
	}
	fixtureStore.readPrivacySources.mu.Unlock()
	if !found {
		t.Fatal("fixture source cache is empty")
	}
	for _, entries := range []int{8000, 32768} {
		retained := peopleProfileSyntheticSourceCacheHeap(t, fixtureStore, sampleState, sampleSource, entries)
		t.Logf("fixture-derived source cache entries=%d retained_heap_bytes=%d bytes_per_entry=%.1f total_mib=%.2f",
			entries, retained, float64(retained)/float64(entries), float64(retained)/(1024*1024))
	}
}

func peopleProfileSyntheticSourceCacheHeap(t *testing.T, fixtureStore *ActivityStore, state activityPrivacySourceState, source activitySourceProvenance, count int) uint64 {
	t.Helper()
	cacheStore := &ActivityStore{db: fixtureStore.db}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("people-profile-heap-%05d", i)
		entryState := state
		entryState.db = cacheStore.db
		entryState.apps = strings.Clone(state.apps)
		entryState.status = strings.Clone(state.status)
		entryState.meeting = strings.Clone(state.meeting)
		entrySource := source
		entrySource.SessionID = id
		entrySource.Apps = make([]string, len(source.Apps))
		for j, app := range source.Apps {
			entrySource.Apps[j] = strings.Clone(app)
		}
		if source.MCPSnapshots != nil {
			snapshots := make([]activitySnapshotPrivacyIdentity, len(*source.MCPSnapshots))
			copy(snapshots, *source.MCPSnapshots)
			entrySource.MCPSnapshots = &snapshots
		}
		cacheStore.rememberActivitySessionProvenance(id, entryState, entrySource)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(cacheStore)
	if after.HeapAlloc <= before.HeapAlloc {
		return 0
	}
	return after.HeapAlloc - before.HeapAlloc
}

func TestPreparePeopleProfileFixture(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to prepare the opt-in People fixture")
	}
	if os.Getenv(peopleProfileFixtureHomeEnv) == "" {
		t.Skip("set YANSU_PEOPLE_PROFILE_FIXTURE_HOME to seed the persistent profile fixture")
	}
	fixture := newPeopleProfileFixture(t)
	if len(fixture.principalIDs) != peopleProfilePrincipalCount {
		t.Fatalf("fixture principals = %d", len(fixture.principalIDs))
	}
	t.Logf("fixture ready at %s: principals=%d handles=3750 sightings=%d sessions=%d session_sightings=%d events=%d contacts=75 context_files=320",
		os.Getenv(peopleProfileFixtureHomeEnv), peopleProfilePrincipalCount,
		peopleProfilePrincipalCount*3, peopleProfileSessionCount, peopleProfileSightingCount, peopleProfileEventCount)
}

func TestPeoplePageProfileLargeFixture(t *testing.T) {
	if os.Getenv(peopleProfileRunEnv) != "1" {
		t.Skip("set ISOFORM_PEOPLE_PERF_FIXTURE=1 to run the opt-in People profile")
	}
	fixture := newPeopleProfileFixture(t)
	rows, err := fixture.app.ListPrincipalsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != peopleProfilePrincipalCount {
		t.Fatalf("profile rows = %d, want %d", len(rows), peopleProfilePrincipalCount)
	}
}

func BenchmarkListPrincipalsWithStats(b *testing.B) {
	fixture := newPeopleProfileFixture(b)
	coldStarted := time.Now()
	rows, err := fixture.app.ListPrincipalsWithStats()
	cold := time.Since(coldStarted)
	if err != nil {
		b.Fatal(err)
	}
	if len(rows) != peopleProfilePrincipalCount {
		b.Fatalf("cold rows = %d, want %d", len(rows), peopleProfilePrincipalCount)
	}
	store := fixture.app.activity.store
	scansBefore := store.readPrivacySources.sourceScans.Load()
	eventsBefore := store.readPrivacySources.eventRows.Load()
	snapshotsBefore := store.readPrivacySources.snapshotRows.Load()
	b.ReportAllocs()
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		rows, err := fixture.app.ListPrincipalsWithStats()
		samples = append(samples, time.Since(start))
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != peopleProfilePrincipalCount {
			b.Fatalf("rows = %d, want %d", len(rows), peopleProfilePrincipalCount)
		}
	}
	b.StopTimer()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := (samples[(len(samples)-1)/2] + samples[len(samples)/2]) / 2
	p90 := samples[(9*len(samples)+9)/10-1]
	maximum := samples[len(samples)-1]
	b.ReportMetric(float64(p50.Nanoseconds())/1e6, "p50-ms/op")
	b.ReportMetric(float64(p90.Nanoseconds())/1e6, "p90-ms/op")
	b.ReportMetric(float64(maximum.Nanoseconds())/1e6, "max-ms/op")
	b.ReportMetric(float64(cold.Nanoseconds())/1e6, "cold-ms/op")
	b.ReportMetric(float64(store.readPrivacySources.sourceScans.Load()-scansBefore)/float64(b.N), "source-scans/op")
	b.ReportMetric(float64(store.readPrivacySources.eventRows.Load()-eventsBefore)/float64(b.N), "event-rows/op")
	b.ReportMetric(float64(store.readPrivacySources.snapshotRows.Load()-snapshotsBefore)/float64(b.N), "snapshot-rows/op")
}

func BenchmarkListPeopleOverview(b *testing.B) {
	fixture := newPeopleProfileFixture(b)
	coldStarted := time.Now()
	coldOverview, err := fixture.app.ListPeopleOverview()
	cold := time.Since(coldStarted)
	if err != nil {
		b.Fatal(err)
	}
	if coldOverview.KnownCount != peopleProfilePrincipalCount {
		b.Fatalf("cold overview knows %d people, want %d", coldOverview.KnownCount, peopleProfilePrincipalCount)
	}
	store := fixture.app.activity.store
	scansBefore := store.readPrivacySources.sourceScans.Load()
	b.ReportAllocs()
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		overview, err := fixture.app.ListPeopleOverview()
		samples = append(samples, time.Since(start))
		if err != nil {
			b.Fatal(err)
		}
		if overview.KnownCount != peopleProfilePrincipalCount {
			b.Fatalf("overview knows %d people, want %d", overview.KnownCount, peopleProfilePrincipalCount)
		}
	}
	b.StopTimer()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := (samples[(len(samples)-1)/2] + samples[len(samples)/2]) / 2
	p90 := samples[(9*len(samples)+9)/10-1]
	maximum := samples[len(samples)-1]
	b.ReportMetric(float64(p50.Nanoseconds())/1e6, "p50-ms/op")
	b.ReportMetric(float64(p90.Nanoseconds())/1e6, "p90-ms/op")
	b.ReportMetric(float64(maximum.Nanoseconds())/1e6, "max-ms/op")
	b.ReportMetric(float64(cold.Nanoseconds())/1e6, "cold-ms/op")
	b.ReportMetric(float64(store.readPrivacySources.sourceScans.Load()-scansBefore)/float64(b.N), "source-scans/op")
}
