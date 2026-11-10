package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/recentfiles"
	"wails-gui/storage"
)

// fakeSpotlight answers queries per folder from a fixed list; a folder in
// hidden returns nothing, as Spotlight does for a folder macOS has not
// opened to Isoform.
type fakeSpotlight struct {
	items   map[string][]recentfiles.Item
	hidden  map[string]bool
	queries []string
}

func (f *fakeSpotlight) query(text, scope string, limit int) ([]recentfiles.Item, error) {
	f.queries = append(f.queries, text)
	if f.hidden[scope] {
		return nil, nil
	}
	if text == recentfiles.ProbeQuery {
		if len(f.items[scope]) > 0 {
			return f.items[scope][:1], nil
		}
		return []recentfiles.Item{{Path: scope + "/old.txt", Name: "old.txt"}}, nil
	}
	return f.items[scope], nil
}

type filesTestReader struct {
	*filesActivityReader
	store    *ActivityStore
	spot     *fakeSpotlight
	marker   *int64
	enabled  *bool
	paused   *bool
	private  *[]string
	pauseLog *[]observationPause
}

const filesTestHome = "/Users/sam"

func newTestFilesReader(t *testing.T, now time.Time) filesTestReader {
	t.Helper()
	store := newTestStore(t)
	spot := &fakeSpotlight{items: map[string][]recentfiles.Item{}, hidden: map[string]bool{}}
	marker := now.Add(-48 * time.Hour).UnixMilli()
	enabled, paused := true, false
	private := []string{}
	pauseLog := []observationPause{}
	r := &filesActivityReader{
		home:    filesTestHome,
		roots:   recentfiles.Roots(filesTestHome),
		now:     func() time.Time { return now },
		store:   func() *ActivityStore { return store },
		enabled: func() bool { return enabled },
		paused:  func() bool { return paused },
		pauses:  func() ([]observationPause, error) { return pauseLog, nil },
		sensitive: func(bundleID, name string) bool {
			return bundleID == "com.agilebits.onepassword7" || name == "Secret App"
		},
		privateFolders: func() []string { return private },
		setLastRead:    func(time.Time) {},
		query:          spot.query,
		exists: func(path string) bool {
			return !strings.HasSuffix(path, "com~apple~CloudDocs")
		},
		kicks: make(chan string, 4),
	}
	r.since = func() (int64, bool) { return marker, marker != 0 }
	r.setSince = func(ms int64) { marker = ms }
	return filesTestReader{r, store, spot, &marker, &enabled, &paused, &private, &pauseLog}
}

func filesTestItem(path string, lastUsed, modified, created time.Time) recentfiles.Item {
	return recentfiles.Item{
		Path:     path,
		Name:     path[strings.LastIndex(path, "/")+1:],
		TypeTree: []string{"public.composite-content", "public.content", "public.item"},
		LastUsed: lastUsed,
		Modified: modified,
		Created:  created,
	}
}

func listAllFiles(t *testing.T, store *ActivityStore) map[string]FileActivityRow {
	t.Helper()
	rows, err := store.ListFileActivity(0, time.Now().Add(365*24*time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]FileActivityRow{}
	for _, row := range rows {
		out[row.Name] = row
	}
	return out
}

func TestFileDisplayNamePreservesDocumentIDsAndRedactsSecrets(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{"2410.14224v1.pdf", "2410.14224v1.pdf"},
		{"2510.14224v1.pdf", "2510.14224v1.pdf"},
		{"Report 2026-10-17.pdf", "Report 2026-10-17.pdf"},
		{"ghp_1234567890abcdefghijklmnopqrstuvwxyz.txt", "[REDACTED:api_key].txt"},
		{"verification code 482913.txt", "verification code [REDACTED:one_time_password].txt"},
		{"verification_code_482913.txt", "verification_code_[REDACTED:one_time_password].txt"},
		{"verification-code-482913.txt", "verification-code-[REDACTED:one_time_password].txt"},
		{"verification_code_482_913_archive.txt", "verification_code_[REDACTED:one_time_password]_archive.txt"},
		{"sk-test-example-fixture.txt", "[REDACTED:api_key].txt"},
		{"Notes for ann@example.com (signed).pdf", "Notes for [REDACTED:private_email] (signed).pdf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := fileDisplayName(test.name); got != test.want {
				t.Fatalf("fileDisplayName(%q) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestFilesReaderKeepsDocumentNamesAndProtectsSecretsThroughDelivery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	const token = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	const key = "sk-test-example-fixture"
	cases := []struct {
		name string
		want string // An empty name means collection must exclude the file.
	}{
		{"2410.14224v1.pdf", "2410.14224v1.pdf"},
		{"2510.14224v1.pdf", "2510.14224v1.pdf"},
		{"Report 2026-10-17.pdf", "Report 2026-10-17.pdf"},
		{"verification_report_2026.txt", "verification_report_2026.txt"},
		{token + ".txt", "[REDACTED:api_key].txt"},
		{"notes_" + token + ".txt", "notes_[REDACTED:api_key].txt"},
		{key + ".txt", "[REDACTED:api_key].txt"},
		{"notes_" + key + ".txt", "notes_[REDACTED:api_key].txt"},
		{"verification code 482913.txt", ""},
		{"verification_code_482913.txt", ""},
		{"verification-code-482913.txt", ""},
		{"verification__code__482_913_archive.txt", ""},
		{"api_key_1234567890abcdefghijklmnop.txt", ""},
	}
	for _, test := range cases {
		tr.spot.items[docs] = append(tr.spot.items[docs], filesTestItem(docs+"/"+test.name, now.Add(-time.Minute), time.Time{}, time.Time{}))
	}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := tr.store.ListFileActivity(0, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	stored := map[string]string{}
	for _, test := range cases {
		id := recentfiles.PathHash(docs + "/" + test.name)
		if test.want != "" {
			expected[id] = test.want
		}
	}
	for _, row := range rows {
		stored[row.ID] = row.Name
	}
	if !reflect.DeepEqual(stored, expected) {
		t.Fatalf("stored file names = %v, want %v", stored, expected)
	}
	brief := buildMobileBriefV1(mobileBriefInput{
		AsOf: now, Location: time.Local, Slot: mobileBriefSlotOnRequest,
		Doing: mobileBriefDoingCandidates(rows, nil, nil),
	})
	state, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if err := state.SetUIState(context.Background(), permissionUIStateKey(filesPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	app := &App{store: state, activity: &activityMonitorState{store: tr.store}}
	delivered := app.mobileBriefForDelivery(brief)
	for _, stage := range []struct {
		name  string
		brief MobileBriefV1
	}{
		{"brief", brief},
		{"phone delivery", delivered},
	} {
		t.Run(stage.name, func(t *testing.T) {
			want := map[string]string{}
			for id, title := range expected {
				if stage.name == "phone delivery" && strings.Contains(title, "[REDACTED:api_key]") {
					title = "an API key.txt"
				}
				want[id] = title
			}
			titles := map[string]string{}
			labels := map[string]string{}
			evidence := map[string]MobileBriefRef{}
			for _, ref := range stage.brief.Evidence {
				evidence[ref.Ref] = ref
				if ref.SourceKind == mobileBriefFileSource {
					labels[strings.TrimPrefix(ref.SourceID, "file:")] = ref.Label
				}
			}
			for _, item := range stage.brief.Sections.Doing {
				if len(item.Refs) != 1 {
					t.Fatalf("file card lost its source: %+v", item)
				}
				titles[strings.TrimPrefix(evidence[item.Refs[0]].SourceID, "file:")] = item.Title
			}
			if !reflect.DeepEqual(titles, want) || !reflect.DeepEqual(labels, want) {
				t.Fatalf("file titles = %v, evidence = %v, want %v", titles, labels, want)
			}
			payload, err := json.Marshal(stage.brief)
			if err != nil {
				t.Fatal(err)
			}
			if stage.name == "phone delivery" && strings.Contains(string(payload), "[REDACTED") {
				t.Fatalf("phone copy contains raw masks: %s", payload)
			}
			for _, secret := range []string{"482913", "482_913", token, key} {
				if strings.Contains(string(payload), secret) {
					t.Fatalf("%s contains a raw secret", stage.name)
				}
			}
		})
	}
	if err := state.SetUIState(context.Background(), permissionUIStateKey(filesPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	withdrawn := app.mobileBriefForDelivery(brief)
	if len(withdrawn.Sections.Doing) != 0 || len(withdrawn.Evidence) != 0 {
		t.Fatal("file titles remained visible after permission was withdrawn")
	}
	if err := state.SetUIState(context.Background(), permissionUIStateKey(filesPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	private, err := json.Marshal([]string{docs})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetUIState(context.Background(), filesPrivateFoldersKey, string(private)); err != nil {
		t.Fatal(err)
	}
	withdrawn = app.mobileBriefForDelivery(brief)
	if len(withdrawn.Sections.Doing) != 0 || len(withdrawn.Evidence) != 0 {
		t.Fatal("file titles remained visible after the folder became private")
	}
}

func TestFilesReaderStoresDocumentsByRule(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Q3 deck.key", now.Add(-time.Hour), time.Time{}, now.Add(-24*time.Hour)),
		filesTestItem(docs+"/Plan.docx", time.Time{}, now.Add(-30*time.Minute), now.Add(-5*time.Hour)),
		filesTestItem(docs+"/.secret-notes.txt", now.Add(-time.Hour), time.Time{}, time.Time{}),
		filesTestItem(docs+"/web/node_modules/pkg/readme.md", now.Add(-time.Hour), time.Time{}, time.Time{}),
		filesTestItem(docs+"/passwords.csv", now.Add(-time.Hour), time.Time{}, time.Time{}),
		filesTestItem(docs+"/Contract for ann@example.com.pdf", now.Add(-time.Hour), time.Time{}, time.Time{}),
		filesTestItem(docs+"/Before reading.txt", now.Add(-72*time.Hour), time.Time{}, time.Time{}),
		filesTestItem("/Users/sam/Pictures/elsewhere.png", now.Add(-time.Hour), time.Time{}, time.Time{}),
	}
	added, err := tr.readOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if added != 3 || len(rows) != 3 {
		t.Fatalf("added %d, stored %v", added, rows)
	}
	deck := rows["Q3 deck.key"]
	if deck.Action != recentfiles.ActionOpened || deck.Folder != recentfiles.RootDocuments || deck.LastUsedAt != now.Add(-time.Hour).UnixMilli() {
		t.Fatalf("deck = %+v", deck)
	}
	if deck.ID != recentfiles.PathHash(docs+"/Q3 deck.key") || strings.Contains(deck.DirHashes, "Documents") {
		t.Fatalf("a document is kept by hash only: %+v", deck)
	}
	if plan := rows["Plan.docx"]; plan.Action != recentfiles.ActionEdited || plan.ModifiedAt != now.Add(-30*time.Minute).UnixMilli() {
		t.Fatalf("plan = %+v", plan)
	}
	for name := range rows {
		if strings.Contains(name, "ann@example.com") {
			t.Fatalf("a name keeps its personal details redacted: %q", name)
		}
	}
	if _, ok := rows["Before reading.txt"]; ok {
		t.Fatal("work from before reading was allowed must never be read")
	}
}

func TestFilesReaderFirstRunNeverReachesBack(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	*tr.marker = 0
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Yesterday.txt", now.Add(-time.Hour), time.Time{}, time.Time{}),
	}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("first run stored %v", rows)
	}
	if *tr.marker != now.UnixMilli() {
		t.Fatalf("first run starts reading now, marker %d", *tr.marker)
	}
}

func TestFilesReaderLeavesOutPausesAndPausedTime(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/During pause.txt", now.Add(-90*time.Minute), time.Time{}, time.Time{}),
		filesTestItem(docs+"/After pause.txt", now.Add(-10*time.Minute), time.Time{}, time.Time{}),
	}
	*tr.pauseLog = []observationPause{{Start: now.Add(-2 * time.Hour).UnixMilli(), End: now.Add(-time.Hour).UnixMilli()}}
	*tr.paused = true
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("a paused read stored %v", rows)
	}
	*tr.paused = false
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if _, ok := rows["During pause.txt"]; ok || len(rows) != 1 {
		t.Fatalf("work inside a pause must never be stored: %v", rows)
	}
}

func TestFilesReaderPauseLogUnreadableStoresNothing(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{filesTestItem(docs+"/a.txt", now.Add(-time.Minute), time.Time{}, time.Time{})}
	tr.pauses = func() ([]observationPause, error) { return nil, errors.New("broken") }
	if _, err := tr.readOnce(context.Background()); err == nil {
		t.Fatal("an unreadable pause log must fail the read")
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("stored %v", rows)
	}
}

func TestFilesReaderSwitchOffRetainsSavedActivity(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{filesTestItem(docs+"/a.txt", now.Add(-time.Minute), time.Time{}, time.Time{})}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	*tr.enabled = false
	tr.spot.queries = nil
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 1 {
		t.Fatalf("switching off must keep saved activity: %v", rows)
	}
	if len(tr.spot.queries) != 0 {
		t.Fatal("a switched-off read must not ask Spotlight")
	}
	if *tr.marker != now.UnixMilli() {
		t.Fatal("reading must start again from the moment it is switched back on")
	}
}

func TestFilesReaderPrivateFolders(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Taxes/2026/return.pdf", now.Add(-time.Hour), time.Time{}, time.Time{}),
		filesTestItem(docs+"/Work/plan.txt", now.Add(-time.Hour), time.Time{}, time.Time{}),
	}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 2 {
		t.Fatalf("stored %v", rows)
	}
	// Marked private later: the stored document goes and is never read again.
	*tr.private = []string{docs + "/taxes"}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if _, ok := rows["return.pdf"]; ok || len(rows) != 1 {
		t.Fatalf("a private folder's documents must go: %v", rows)
	}
	if n, err := tr.store.DeleteFileActivityUnder(recentfiles.FolderHash(docs + "/Work")); err != nil || n != 1 {
		t.Fatalf("delete under a folder = %d, %v", n, err)
	}
}

func saveFilesTestWindow(t *testing.T, store *ActivityStore, id string, at time.Time, triage string, evt activitymon.ActivityEvent) {
	t.Helper()
	evt.Timestamp = at.UnixMilli()
	evt.Kind = activitymon.EventAppSwitch
	session := ActivitySessionRow{
		ID: id, StartTime: at.Add(-time.Minute).UnixMilli(), EndTime: at.Add(time.Minute).UnixMilli(),
		TriggerKind: "test", TriageStatus: triage, CreatedAt: at.UnixMilli(), EventCount: 1,
	}
	if err := store.SaveSession(session, []activitymon.ActivityEvent{evt}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFilesReaderNamesTheAppAndDropsProtectedUse(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	used := now.Add(-time.Hour)
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Q3 deck.key", used, time.Time{}, time.Time{}),
		filesTestItem(docs+"/Vault export.txt", used.Add(-20*time.Minute), time.Time{}, time.Time{}),
		filesTestItem(docs+"/Bank statement.pdf", used.Add(-40*time.Minute), time.Time{}, time.Time{}),
		filesTestItem(docs+"/Quiet reading.pdf", used.Add(-60*time.Minute), time.Time{}, time.Time{}),
	}
	saveFilesTestWindow(t, tr.store, "s1", used.Add(time.Minute), "pending", activitymon.ActivityEvent{
		AppBundleID: "com.apple.iWork.Keynote", AppName: "Keynote", WindowTitle: "Q3 deck.key",
	})
	saveFilesTestWindow(t, tr.store, "s2", used.Add(-20*time.Minute), "pending", activitymon.ActivityEvent{
		AppBundleID: "com.agilebits.onepassword7", AppName: "1Password", WindowTitle: "Vault export",
	})
	saveFilesTestWindow(t, tr.store, "s3", used.Add(-40*time.Minute), "protected", activitymon.ActivityEvent{
		AppBundleID: "com.apple.Preview", AppName: "Preview", WindowTitle: "Bank statement.pdf",
	})
	saveFilesTestWindow(t, tr.store, "s4", used.Add(-60*time.Minute), "pending", activitymon.ActivityEvent{
		AppBundleID: "com.google.Chrome", AppName: "Chrome", WindowTitle: "Quiet reading.pdf - Google Chrome (Incognito)",
	})
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if len(rows) != 1 {
		t.Fatalf("protected, sensitive and private-window use must be left out: %v", rows)
	}
	if deck := rows["Q3 deck.key"]; deck.AppBundleID != "com.apple.iWork.Keynote" || deck.AppName != "Keynote" {
		t.Fatalf("deck = %+v", deck)
	}
	// The app is protected later: its documents go at the next read.
	tr.sensitive = func(bundleID, name string) bool {
		return bundleID == "com.apple.iWork.Keynote" || bundleID == "com.agilebits.onepassword7"
	}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("documents of a newly protected app must go: %v", rows)
	}
}

func TestFilesReaderDropsWorkInProtectedSessionsWithoutAWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	used := now.Add(-time.Hour)
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Unnamed in any window.pdf", used, time.Time{}, time.Time{}),
		filesTestItem(docs+"/Later work.txt", now.Add(-10*time.Minute), time.Time{}, time.Time{}),
	}
	// The protected session names nothing about the file.
	saveFilesTestWindow(t, tr.store, "p1", used, "protected", activitymon.ActivityEvent{
		AppBundleID: "com.example.vault", AppName: "Vault", WindowTitle: "Unlock",
	})
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if _, ok := rows["Unnamed in any window.pdf"]; ok || len(rows) != 1 {
		t.Fatalf("work during a protected session must never be kept: %v", rows)
	}
	// A protected session saved only after the document was stored still
	// removes it at the next read.
	saveFilesTestWindow(t, tr.store, "p2", now.Add(-10*time.Minute), "protected", activitymon.ActivityEvent{
		AppBundleID: "com.example.vault", AppName: "Vault", WindowTitle: "Unlock",
	})
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("a protected session saved later must sweep the document: %v", rows)
	}
}

func TestFilesReaderTreatsICloudDocumentsAsDocuments(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	tr.exists = func(string) bool { return true }
	docs := filesTestHome + "/Documents"
	cloud := filesTestHome + "/Library/Mobile Documents/com~apple~CloudDocs"
	item := filesTestItem(cloud+"/Documents/Taxes/return.pdf", now.Add(-time.Hour), time.Time{}, time.Time{})
	// Spotlight reports the same file under both scopes.
	tr.spot.items[docs] = []recentfiles.Item{item, filesTestItem(cloud+"/Documents/Plan.txt", now.Add(-time.Hour), time.Time{}, time.Time{})}
	tr.spot.items[cloud] = []recentfiles.Item{item}
	*tr.private = []string{docs + "/Taxes"}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := listAllFiles(t, tr.store)
	if len(rows) != 1 || rows["Plan.txt"].Folder != recentfiles.RootDocuments {
		t.Fatalf("a private folder must hide its iCloud path too, and Documents stays Documents: %v", rows)
	}
}

func TestFilesReaderEditCountsWhenTheReopenWasPaused(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	edited := now.Add(-50 * time.Minute)
	reopened := now.Add(-20 * time.Minute)
	tr.spot.items[docs] = []recentfiles.Item{
		filesTestItem(docs+"/Plan.docx", reopened, edited, now.Add(-24*time.Hour)),
	}
	*tr.pauseLog = []observationPause{{Start: now.Add(-25 * time.Minute).UnixMilli(), End: now.Add(-15 * time.Minute).UnixMilli()}}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	plan := listAllFiles(t, tr.store)["Plan.docx"]
	if plan.Action != recentfiles.ActionEdited || plan.ActiveAt != edited.UnixMilli() || plan.LastUsedAt != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestFilesSwitchOffRetainsSavedActivity(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{filesTestItem(docs+"/a.txt", now.Add(-time.Minute), time.Time{}, time.Time{})}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := &App{filesActivity: tr.filesActivityReader}
	*tr.enabled = false
	a.filesSwitchChanged(false)
	if rows := listAllFiles(t, tr.store); len(rows) != 1 {
		t.Fatalf("switching off must keep saved activity: %v", rows)
	}
}

func TestFilesReaderFolderAvailability(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	tr.spot.hidden[filesTestHome+"/Desktop"] = true
	docs := filesTestHome + "/Documents"
	tr.spot.items[docs] = []recentfiles.Item{filesTestItem(docs+"/a.txt", now.Add(-time.Minute), time.Time{}, time.Time{})}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, folders := tr.status()
	got := map[string]filesFolderStatus{}
	for _, f := range folders {
		got[f.Key] = f
	}
	if !got[recentfiles.RootDocuments].Available || got[recentfiles.RootDesktop].Available {
		t.Fatalf("folders = %+v", folders)
	}
	if !got[recentfiles.RootDownloads].Available {
		t.Fatal("a folder with nothing recent but something in it is available")
	}
	if got[recentfiles.RootICloud].Exists {
		t.Fatal("iCloud Drive is not on this Mac")
	}
	rows, _ := tr.store.ListFileActivity(0, now.Add(time.Hour).UnixMilli())
	views := filesFolderViews(tr.roots, folders, rows)
	if len(views) != 3 || views[0].Key != recentfiles.RootDocuments || views[0].Recent != 1 || views[1].Available {
		t.Fatalf("views = %+v", views)
	}
}

func TestFilesReaderPrunesAfterRetention(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tr := newTestFilesReader(t, now)
	old := FileActivityRow{ID: "old", Name: "old.txt", Folder: "documents", ActiveAt: now.Add(-15 * 24 * time.Hour).UnixMilli(), Action: "opened"}
	if _, err := tr.store.UpsertFileActivity([]FileActivityRow{old}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.readOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := listAllFiles(t, tr.store); len(rows) != 0 {
		t.Fatalf("documents older than two weeks must go: %v", rows)
	}
}

func TestUpsertFileActivityKeepsTheLaterWork(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UnixMilli()
	first := FileActivityRow{ID: "f", Name: "a.txt", Folder: "documents", ActiveAt: 2000, LastUsedAt: 2000, Action: "opened", AppBundleID: "com.a", AppName: "A"}
	if n, err := store.UpsertFileActivity([]FileActivityRow{first}, now); err != nil || n != 1 {
		t.Fatalf("insert = %d, %v", n, err)
	}
	older := FileActivityRow{ID: "f", Name: "a.txt", Folder: "documents", ActiveAt: 1000, ModifiedAt: 1000, Action: "edited"}
	if n, err := store.UpsertFileActivity([]FileActivityRow{older}, now); err != nil || n != 0 {
		t.Fatalf("an older read must change nothing: %d, %v", n, err)
	}
	later := FileActivityRow{ID: "f", Name: "a.txt", Folder: "documents", ActiveAt: 3000, ModifiedAt: 3000, Action: "edited"}
	if n, err := store.UpsertFileActivity([]FileActivityRow{later}, now); err != nil || n != 1 {
		t.Fatalf("later work = %d, %v", n, err)
	}
	rows, _ := store.ListFileActivity(0, 10_000)
	got := rows[0]
	if got.ActiveAt != 3000 || got.Action != "edited" || got.LastUsedAt != 2000 || got.ModifiedAt != 3000 || got.AppBundleID != "com.a" {
		t.Fatalf("row = %+v", got)
	}
}

func TestFileTimes(t *testing.T) {
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	start := base.Add(-3 * time.Hour)
	pause := []observationPause{{Start: base.Add(-time.Hour).UnixMilli(), End: base.Add(-30 * time.Minute).UnixMilli()}}
	cases := []struct {
		name   string
		item   recentfiles.Item
		at     time.Time
		action string
		ok     bool
	}{
		{"edit counts", recentfiles.Item{Created: start.Add(-time.Hour), Modified: base}, base, recentfiles.ActionEdited, true},
		{"edit in a pause falls back to the open", recentfiles.Item{Created: start.Add(-time.Hour), Modified: base.Add(-45 * time.Minute), LastUsed: base.Add(-2 * time.Hour)}, base.Add(-2 * time.Hour), recentfiles.ActionOpened, true},
		{"edited, then opened later: an edit all the same", recentfiles.Item{Created: start.Add(-time.Hour), Modified: base.Add(-2 * time.Hour), LastUsed: base}, base, recentfiles.ActionEdited, true},
		{"both before reading started", recentfiles.Item{Created: start.Add(-48 * time.Hour), Modified: start.Add(-time.Hour), LastUsed: start.Add(-time.Minute)}, time.Time{}, "", false},
		{"open in a pause only", recentfiles.Item{LastUsed: base.Add(-40 * time.Minute)}, time.Time{}, "", false},
	}
	for _, c := range cases {
		_, _, at, action, ok := fileTimes(c.item, recentfiles.RootDocuments, start, pause)
		if ok != c.ok || action != c.action || !at.Equal(c.at) {
			t.Errorf("%s: %v %q %v", c.name, at, action, ok)
		}
	}
}

func TestFilesSwitchStartsOffUntilTurnedOn(t *testing.T) {
	app := mainChatTestApp(t)
	if app.isPermissionEnabled(filesPermissionKey) {
		t.Fatal("recent files must start off until a real Mac confirms Spotlight's folder rules")
	}
	if !app.isPermissionEnabled(mailPermissionKey) || !app.isPermissionEnabled(clipboardPermissionKey) {
		t.Fatal("the other switches keep starting on")
	}
	if (&App{}).isPermissionEnabled(filesPermissionKey) {
		t.Fatal("with no store the default is off too")
	}
	if err := app.store.SetUIState(context.Background(), permissionUIStateKey(filesPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	if !app.isPermissionEnabled(filesPermissionKey) {
		t.Fatal("the user's own choice is kept")
	}
}
