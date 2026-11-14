package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// streamPageTestApp prepares a chat with one item and keeps background work
// out of the way: the refresh waits an hour, so a test runs it explicitly.
func streamPageTestApp(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	a, session, _ := registryChatTestApp(t)
	a.streamPages.mu.Lock()
	a.streamPages.settings()
	a.streamPages.launchDelay = time.Hour
	a.streamPages.eventDelay = time.Hour
	a.streamPages.mu.Unlock()
	appendStreamPageItem(t, a, session, "one", "first words")
	// The memory folder counts as settled once it has been still for a moment;
	// a copy's stamp holds only then, as it does in a running app.
	waitForMemorySettle(t, a)
	return a, session
}

func waitForMemorySettle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot, err := a.memorySnapshot(); err == nil && snapshot.settled {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the memory folder did not settle")
}

func appendStreamPageItem(t *testing.T, a *App, session, id, content string) {
	t.Helper()
	if _, err := a.store.AppendUserStreamItemAtomic(t.Context(), session, storage.StreamItemRow{ID: id, Kind: "user", Content: content}); err != nil {
		t.Fatal(err)
	}
}

func latestPage(t *testing.T, a *App, session string) StreamItemPageData {
	t.Helper()
	page, err := a.LoadStreamItemsPage(session, -1, 50)
	if err != nil {
		t.Fatal(err)
	}
	// The background full check records the copy, as it does in a running app,
	// before the copy can be served.
	a.verifyStreamPageCopy(streamPageKey{session: session, limit: 50})
	return page
}

func pageHas(page StreamItemPageData, id string) bool {
	for _, item := range page.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func setStreamPageRecomputeWait(a *App, versionWait, recomputeWait time.Duration) {
	a.streamPages.mu.Lock()
	a.streamPages.versionWait = versionWait
	a.streamPages.recomputeWait = recomputeWait
	a.streamPages.mu.Unlock()
}

// While the stream version holds, the first page is served from its saved copy
// with no computation and no permission retry on the read path.
func TestLatestStreamPageIsServedFromItsCopyWhileTheVersionHolds(t *testing.T) {
	a, session := streamPageTestApp(t)
	if page := latestPage(t, a, session); !pageHas(page, "one") {
		t.Fatal("the cold read did not show the stored item")
	}
	computes, retries := a.streamPages.computes.Load(), a.streamPages.retries.Load()
	for i := 0; i < 5; i++ {
		if page := latestPage(t, a, session); !pageHas(page, "one") {
			t.Fatalf("read %d lost the stored item", i)
		}
	}
	if n := a.streamPages.computes.Load(); n != computes {
		t.Fatalf("reads with the version unchanged recomputed %d times", n-computes)
	}
	if n := a.streamPages.retries.Load(); n != retries {
		t.Fatal("a read ran the permission retries; they belong to the background refresh")
	}
}

// A newer item is never hidden by the copy: a changed version recomputes.
func TestLatestStreamPageShowsANewerItemAtOnce(t *testing.T) {
	a, session := streamPageTestApp(t)
	latestPage(t, a, session)
	appendStreamPageItem(t, a, session, "two", "second words")
	if page := latestPage(t, a, session); !pageHas(page, "two") {
		t.Fatal("a newer item was hidden behind the saved copy")
	}
}

// If the version cannot be read in time, the copy is served and the missing
// item arrives with the background refresh.
func TestLatestStreamPageServesTheCopyWhenTheVersionTimesOut(t *testing.T) {
	a, session := streamPageTestApp(t)
	latestPage(t, a, session)
	appendStreamPageItem(t, a, session, "two", "second words")
	setStreamPageRecomputeWait(a, 0, time.Second)
	if page := latestPage(t, a, session); pageHas(page, "two") {
		t.Fatal("the timeout path should serve the copy")
	}
	setStreamPageRecomputeWait(a, streamPageVersionDeadline, streamPageRecomputeWait)
	a.refreshStreamPages()
	if page := latestPage(t, a, session); !pageHas(page, "two") {
		t.Fatal("the refresh did not add the newer item")
	}
}

// A privacy change drops the copy: the next read computes and saves a new one.
func TestLatestStreamPageIsDroppedWhenThePrivacyStampChanges(t *testing.T) {
	a, session := streamPageTestApp(t)
	latestPage(t, a, session)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	before := a.streamPages.computes.Load()
	pii.SetUserSensitiveApps(map[string]string{"com.example.private": "work"})
	latestPage(t, a, session)
	if n := a.streamPages.computes.Load(); n != before+1 {
		t.Fatalf("a privacy change did not recompute the page: %d -> %d", before, n)
	}
}

// Every change to a stored item reaches the next read: an edit, a withdrawal
// (an in-place tool-input change) and a removal.
func TestLatestStreamPageShowsEditsWithdrawalsAndRemovals(t *testing.T) {
	a, session := streamPageTestApp(t)
	appendStreamPageItem(t, a, session, "two", "second words")
	latestPage(t, a, session)
	ctx := t.Context()
	if err := a.store.UpdateStreamItemContent(ctx, "two", "edited words"); err != nil {
		t.Fatal(err)
	}
	if page := latestPage(t, a, session); !streamPageContent(page, "two", "edited words") {
		t.Fatal("an edit did not reach the next read")
	}
	if err := a.store.UpdateStreamItemToolInput(ctx, "two", `{"withdrawn":true}`); err != nil {
		t.Fatal(err)
	}
	if page := latestPage(t, a, session); !streamPageToolInput(page, "two", `{"withdrawn":true}`) {
		t.Fatal("a withdrawal did not reach the next read")
	}
	if err := a.store.DeleteStreamItemByID(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if page := latestPage(t, a, session); pageHas(page, "two") {
		t.Fatal("a removed item is still shown")
	}
}

func streamPageContent(page StreamItemPageData, id, content string) bool {
	for _, item := range page.Items {
		if item.ID == id {
			return item.Content == content
		}
	}
	return false
}

func streamPageToolInput(page StreamItemPageData, id, toolInput string) bool {
	for _, item := range page.Items {
		if item.ID == id {
			return item.ToolInput == toolInput
		}
	}
	return false
}

func streamPageHasContent(page StreamItemPageData, content string) bool {
	for _, item := range page.Items {
		if item.Content == content {
			return true
		}
	}
	return false
}

// A saved copy that holds a brief lead is dropped by any withdrawal before the
// next read: the withheld words do not appear, and the copy is not served. The
// copy is planted, so the test proves the drop, not the lead's projection.
func TestAWithdrawalDropsASavedCopyThatHoldsABriefLead(t *testing.T) {
	const lead = "Withdrawn brief words"
	withdrawals := map[string]func(){
		"session withdrawal": func() {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "gone"})
		},
		"brief privacy epoch": func() { briefPrivacyChanged() },
		"sensitive app": func() {
			pii.SetUserSensitiveApps(map[string]string{"com.example.secret": "work"})
		},
	}
	for name, withdraw := range withdrawals {
		t.Run(name, func(t *testing.T) {
			previous := pii.GetUserSensitiveApps()
			t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
			a, session := streamPageTestApp(t)
			latestPage(t, a, session)
			// The read that saved the copy queued its full check. Plant only once
			// that check has finished, or it rewrites the copy over the planted one.
			waitStreamPageVerified(t, a)
			key := streamPageKey{session: session, limit: 50}
			stamp := a.streamPageStamp(t.Context())
			snap, reason := readStreamPageSnapshot(key, stamp)
			if reason != "" {
				t.Fatalf("the first read did not save a copy: %s", reason)
			}
			snap.Page.Items[0].Content = lead
			if err := writeStreamPageSnapshot(key, snap); err != nil {
				t.Fatal(err)
			}
			if page := latestPage(t, a, session); !streamPageHasContent(page, lead) {
				t.Fatal("the planted copy was not served before the withdrawal")
			}
			computes := a.streamPages.computes.Load()
			withdraw()
			page := latestPage(t, a, session)
			if streamPageHasContent(page, lead) {
				t.Fatal("withheld brief words were served after a withdrawal")
			}
			if a.streamPages.computes.Load() == computes {
				t.Fatal("the copy was served after a withdrawal; it should have been dropped")
			}
		})
	}
}

// A saved copy survives a restart: the durable privacy epoch is read back from
// the briefs folder, so the first read after a launch serves the copy with no
// computation. The in-memory counters reset on launch; the copy must not depend
// on them.
func TestASavedFirstPageSurvivesARestart(t *testing.T) {
	a, session := streamPageTestApp(t)
	adoptTestBriefPrivacyEpoch(a) // load the briefs folder's epoch, so changes are saved
	briefPrivacyChanged()         // a durable privacy change, saved before the copy
	latestPage(t, a, session)
	computes := a.streamPages.computes.Load()
	restart := func() {
		mobileBriefPrivacyEpoch.Store(0)
		mobileBriefReadEpoch.Store(0)
		mobileBriefPrivacyEpochMu.Lock()
		mobileBriefPrivacyEpochSavedDir = ""
		mobileBriefPrivacyEpochMu.Unlock()
		// A launch reads the saved epoch back before the first read.
		loadSavedCopyEpoch()
	}
	restart()
	if page := latestPage(t, a, session); !pageHas(page, "one") {
		t.Fatal("the first read after a restart lost the stored item")
	}
	if n := a.streamPages.computes.Load(); n != computes {
		t.Fatalf("a restart dropped the saved first page: %d computations", n-computes)
	}
}

// adoptTestBriefPrivacyEpoch makes the app load the briefs folder's saved
// privacy epoch, which writes later changes to that folder.
func adoptTestBriefPrivacyEpoch(a *App) {
	dir, err := mobileBriefStateDir()
	if err != nil {
		panic(err)
	}
	// The epoch file is written beside the briefs, which exist once a brief is stored.
	if err := os.MkdirAll(filepath.Join(dir, mobileBriefDirName), 0o700); err != nil {
		panic(err)
	}
	loadSavedCopyEpoch()
}

// holdFirstStreamRun makes the first computation after this call wait until
// release. started closes once the run is inside the computation.
func holdFirstStreamRun(a *App) (started <-chan struct{}, release func()) {
	s := make(chan struct{})
	r := make(chan struct{})
	var once sync.Once
	a.streamPages.mu.Lock()
	a.streamPages.computeGate = func() { once.Do(func() { close(s); <-r }) }
	a.streamPages.mu.Unlock()
	return s, func() { close(r) }
}

// A read that arrives while a run is in progress is served by the next run, so
// an item written before it arrived is never missing from its page.
func TestAReadArrivingDuringARunSeesANewerItem(t *testing.T) {
	a, session := streamPageTestApp(t)
	started, release := holdFirstStreamRun(a)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = a.LoadStreamItemsPage(session, -1, 50)
	}()
	<-started
	appendStreamPageItem(t, a, session, "late", "written during the run")
	second := make(chan StreamItemPageData, 1)
	go func() {
		page, _ := a.LoadStreamItemsPage(session, -1, 50)
		second <- page
	}()
	time.Sleep(50 * time.Millisecond) // let the second read join the queue
	release()
	<-firstDone
	select {
	case page := <-second:
		if !pageHas(page, "late") {
			t.Fatal("a read that arrived after an item was written did not see it")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the queued read did not finish")
	}
}

// A burst of reads while one run is open costs at most two runs: the open one
// and one shared run for everyone who arrived during it.
func TestABurstOfReadsSharesOneFollowingRun(t *testing.T) {
	a, session := streamPageTestApp(t)
	started, release := holdFirstStreamRun(a)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = a.LoadStreamItemsPage(session, -1, 50)
	}()
	<-started
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if page, err := a.LoadStreamItemsPage(session, -1, 50); err != nil || !pageHas(page, "one") {
				t.Error("a read in the burst did not return the stored item")
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	release()
	wg.Wait()
	if n := a.streamPages.computes.Load(); n > 2 {
		t.Fatalf("a burst of 11 reads made %d computations, want at most 2", n)
	}
}

// A copy that a background refresh saves is verified in the background as well,
// so the next read serves it. Before this, the refresh left the copy unverified
// and that read recomputed the page.
func TestBackgroundRefreshVerifiesItsCopy(t *testing.T) {
	a, session := streamPageTestApp(t)
	key := streamPageKey{session: session, limit: 50}
	latestPage(t, a, session)
	a.refreshStreamPages()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap, reason := readRawStreamPageSnapshot(key); reason == "" && snap.Deep != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the copy saved by the background refresh was never verified")
		}
		time.Sleep(10 * time.Millisecond)
	}
	computes := a.streamPages.computes.Load()
	page, err := a.LoadStreamItemsPage(session, -1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n := a.streamPages.computes.Load(); n != computes {
		t.Fatalf("the read recomputed the page (%d computes) instead of serving its verified copy", n-computes)
	}
	if !pageHas(page, "one") {
		t.Fatal("the served copy lost its item")
	}
}

// waitStreamPageVerified waits until every queued full check has finished.
func waitStreamPageVerified(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.streamPages.verifyDone.Load() != a.streamPages.verifyQueued.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the copy's full check did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// appendStreamPageCard adds a proactive card to the chat, as a card row is stored.
func appendStreamPageCard(t *testing.T, a *App, session, id string) {
	t.Helper()
	card := mainChatProactive{MomentID: id, Kind: "decision_due", State: "pending", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if _, err := a.store.AppendStreamItem(t.Context(), session, storage.StreamItemRow{ID: id, Kind: "system", ToolName: mainChatProactiveTool, ToolInput: mainChatViewPayload(t, card), CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

// waitStreamPageCardChecks waits until every background card check has finished.
func waitStreamPageCardChecks(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for a.streamPages.cardChecksDone.Load() != a.streamPages.cardChecksQueued.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the background card check did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func captureStreamPageReloads(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	previous := streamPageReloadHook
	streamPageReloadHook = func(string, string) { n.Add(1) }
	t.Cleanup(func() { streamPageReloadHook = previous })
	return &n
}

// A copy that holds a card is served at once, with no computation; the live
// gates confirm its card in the background and the copy stays.
func TestProactiveCardCopyIsServedAtOnceAndConfirmedLater(t *testing.T) {
	a, session := streamPageTestApp(t)
	reloads := captureStreamPageReloads(t)
	appendStreamPageCard(t, a, session, "card-served")
	latestPage(t, a, session)
	waitStreamPageVerified(t, a)
	waitStreamPageCardChecks(t, a)
	computes := a.streamPages.computes.Load()
	page, err := a.LoadStreamItemsPage(session, -1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n := a.streamPages.computes.Load(); n != computes {
		t.Fatalf("a card copy recomputed the page (%d computes)", n-computes)
	}
	if !pageHas(page, "card-served") {
		t.Fatal("the card is missing from the served page")
	}
	waitStreamPageCardChecks(t, a)
	if _, reason := readRawStreamPageSnapshot(streamPageKey{session: session, limit: 50}); reason != "" {
		t.Fatalf("a confirmed card copy was dropped: %s", reason)
	}
	if reloads.Load() != 0 {
		t.Fatal("a confirmed card asked the page to reload")
	}
}

// A background check that only runs out of time keeps the copy and asks for no
// reload: a timeout withdraws nothing.
func TestProactiveCardCopyStaysWhenItsCheckTimesOut(t *testing.T) {
	a, session := streamPageTestApp(t)
	appendStreamPageCard(t, a, session, "card-late")
	latestPage(t, a, session)
	waitStreamPageVerified(t, a)
	waitStreamPageCardChecks(t, a)
	reloads := captureStreamPageReloads(t)
	key := streamPageKey{session: session, limit: 50}
	a.streamPages.mu.Lock()
	a.streamPages.cardBudget = 0
	a.streamPages.mu.Unlock()
	if _, err := a.LoadStreamItemsPage(session, -1, 50); err != nil {
		t.Fatal(err)
	}
	waitStreamPageCardChecks(t, a)
	if _, reason := readRawStreamPageSnapshot(key); reason != "" {
		t.Fatalf("a timed-out card check dropped the copy (%q)", reason)
	}
	if reloads.Load() != 0 {
		t.Fatal("a timed-out card check asked the page to reload")
	}
	computes := a.streamPages.computes.Load()
	if _, err := a.LoadStreamItemsPage(session, -1, 50); err != nil {
		t.Fatal(err)
	}
	if n := a.streamPages.computes.Load(); n != computes {
		t.Fatalf("the read after a timed-out check recomputed (%d computes)", n-computes)
	}
}

// When the live gates do not reproduce a copy's card, the copy is deleted and
// the page is told to reload; the next read recomputes the live page, so the
// copy's card is never served again.
func TestProactiveCardCopyIsDroppedWhenACardChanged(t *testing.T) {
	a, session := streamPageTestApp(t)
	appendStreamPageCard(t, a, session, "card-unconfirmed")
	latestPage(t, a, session)
	waitStreamPageVerified(t, a)
	waitStreamPageCardChecks(t, a)
	key := streamPageKey{session: session, limit: 50}
	snap, reason := readRawStreamPageSnapshot(key)
	if reason != "" {
		t.Fatalf("no copy: %s", reason)
	}
	for i := range snap.Page.Items {
		if snap.Page.Items[i].ID == "card-unconfirmed" {
			snap.Page.Items[i].ToolInput = strings.Replace(snap.Page.Items[i].ToolInput, "{", `{"planted":"words",`, 1)
		}
	}
	if err := writeStreamPageSnapshot(key, snap); err != nil {
		t.Fatal(err)
	}
	reloads := captureStreamPageReloads(t)
	if _, err := a.LoadStreamItemsPage(session, -1, 50); err != nil {
		t.Fatal(err)
	}
	waitStreamPageCardChecks(t, a)
	if _, reason := readRawStreamPageSnapshot(key); reason != "no-copy" {
		t.Fatalf("the copy with a changed card stayed on disk (%q)", reason)
	}
	if reloads.Load() == 0 {
		t.Fatal("the page was not told to reload after a card changed")
	}
	computes := a.streamPages.computes.Load()
	page, err := a.LoadStreamItemsPage(session, -1, 50)
	if err != nil {
		t.Fatal(err)
	}
	planted := false
	for _, item := range page.Items {
		planted = planted || strings.Contains(item.ToolInput, "planted")
	}
	if n := a.streamPages.computes.Load(); n != computes+1 || planted {
		t.Fatalf("the read after a changed card did not recompute the live page (%d computes)", n-computes)
	}
}

// A save keeps the full check of the copy it replaces, so the read after a
// background refresh serves the copy instead of recomputing it as unverified.
func TestASavedCopyKeepsItsFullCheckAcrossSaves(t *testing.T) {
	a, session := streamPageTestApp(t)
	key := streamPageKey{session: session, limit: 50}
	latestPage(t, a, session)
	waitStreamPageVerified(t, a)
	appendStreamPageItem(t, a, session, "two", "second words")
	if _, saved, err := a.computeLatestStreamPage(key, a.streamPageStamp(t.Context())); err != nil || !saved {
		t.Fatalf("the refresh did not save: %v", err)
	}
	if snap, reason := readRawStreamPageSnapshot(key); reason != "" || snap.Deep == "" {
		t.Fatalf("the new copy lost its full check (%q)", reason)
	}
	computes := a.streamPages.computes.Load()
	page, err := a.LoadStreamItemsPage(session, -1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n := a.streamPages.computes.Load(); n != computes || !pageHas(page, "two") {
		t.Fatalf("the read after a save recomputed (%d) or lost the new item", n-computes)
	}
}

// With a writer appending to the chat all the time and a slow projection, the
// first page still paints from its copy in under 100 ms, and the page is told
// to reload once the live page is saved.
func TestLatestStreamPagePaintsFastWithABackgroundWriter(t *testing.T) {
	a, session := streamPageTestApp(t)
	for i := 0; i < 30; i++ {
		appendStreamPageCard(t, a, session, "card-"+string(rune('a'+i)))
	}
	latestPage(t, a, session)
	waitStreamPageVerified(t, a)
	waitStreamPageCardChecks(t, a)
	reloads := captureStreamPageReloads(t)
	// The projection of Bo's page took 1 to 16 s; stand in for it.
	a.streamPages.mu.Lock()
	a.streamPages.computeGate = func() { time.Sleep(1500 * time.Millisecond) }
	a.streamPages.mu.Unlock()

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := a.store.AppendUserStreamItemAtomic(t.Context(), session, storage.StreamItemRow{ID: "w" + time.Now().Format("150405.000000000"), Kind: "user", Content: "writer words"}); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var worst time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		page, err := a.LoadStreamItemsPage(session, -1, 50)
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatal("an empty first page was painted")
		}
		worst = max(worst, took)
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	writer.Wait()
	t.Logf("worst first-page read with a background writer: %s", worst)
	if worst >= 100*time.Millisecond {
		t.Fatalf("a first-page read took %s with a background writer; want under 100 ms", worst)
	}
	deadline := time.Now().Add(10 * time.Second)
	for reloads.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the page was never told to reload after the live page was saved")
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.streamPages.mu.Lock()
	a.streamPages.computeGate = nil
	a.streamPages.mu.Unlock()
	waitStreamPageCardChecks(t, a)
	// Let the detached recomputes finish before the store closes.
	time.Sleep(2 * time.Second)
}

// Deleting a chat withdraws it: its saved first page leaves the disk at once,
// though that chat is never read again. Before, a copy was dropped only when
// its own page was next read, so a deleted chat's words stayed on disk.
func TestDeletingAChatRemovesItsSavedFirstPage(t *testing.T) {
	a, session := streamPageTestApp(t)
	adoptTestBriefPrivacyEpoch(a)
	if page := latestPage(t, a, session); !pageHas(page, "one") {
		t.Fatal("the fixture page lacks its item")
	}
	path, err := streamPageFile(streamPageKey{session: session, limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no saved copy to delete: %v", err)
	}
	if err := a.DeleteSessionData(session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the deleted chat's saved first page is still on disk: %v", err)
	}
}

// A copy left under an older epoch (the app stopped before its chat was read
// again) is removed when the saved epoch is loaded at launch; a copy made under
// the current epoch is kept.
func TestALaunchRemovesSavedPagesFromAnOlderEpoch(t *testing.T) {
	a, session := streamPageTestApp(t)
	adoptTestBriefPrivacyEpoch(a)
	latestPage(t, a, session)
	key := streamPageKey{session: session, limit: 50}
	path, err := streamPageFile(key)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(path), "stale.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte(`{"format":2,"readStamp":"read|999999|false|0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dropStaleStreamPageCopies()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a copy from another epoch stayed on disk")
	}
	if kept, err := os.ReadFile(path); err != nil || string(kept) != string(raw) {
		t.Fatalf("the current copy was removed: %v", err)
	}
}
