package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/pii"
	"wails-gui/storage"
	"yansu-agent/agent"
)

func mobileTopicTestBridge(t *testing.T) (*mobileBridge, string, string) {
	t.Helper()
	app, tag, _ := registryChatTestApp(t)
	if _, err := collectAndReadTopicViewsForTest(app); err != nil {
		t.Fatal(err)
	}
	app.eventBroadcaster = NewEventBroadcaster()
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	return bridge, tag, topicProjectID("obj:atlas")
}

func mobileTopicTestList(t *testing.T, bridge *mobileBridge) []mobileTopicView {
	t.Helper()
	response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead), http.MethodGet, "/topics", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	mobileTopicTestFrame(t, response.Body.String())
	var data struct {
		Topics []mobileTopicView `json:"topics"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Topics == nil {
		t.Fatal("topics must be an array")
	}
	return data.Topics
}

func TestMobileTopicsPendingBootstrapPublishes(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	a.eventBroadcaster = NewEventBroadcaster()
	events := a.eventBroadcaster.AddClient("phone-bootstrap")
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id LIKE 'session-work-%'`); err != nil {
		t.Fatal(err)
	}
	unblock := holdTodayTopicsBuild(t, a)
	bridge := &mobileBridge{app: a, ctx: t.Context()}
	read := func() struct {
		Topics     []mobileTopicView `json:"topics"`
		Refreshing bool              `json:"refreshing"`
		HasTopics  bool              `json:"hasTopics"`
	} {
		t.Helper()
		response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead), http.MethodGet, "/topics", "")
		if response.Code != http.StatusOK {
			t.Fatalf("phone list: %d %s", response.Code, response.Body.String())
		}
		var data struct {
			Topics     []mobileTopicView `json:"topics"`
			Refreshing bool              `json:"refreshing"`
			HasTopics  bool              `json:"hasTopics"`
		}
		if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &data); err != nil {
			t.Fatal(err)
		}
		mobileTopicTestFrame(t, response.Body.String())
		return data
	}
	var first struct {
		Topics     []mobileTopicView `json:"topics"`
		Refreshing bool              `json:"refreshing"`
		HasTopics  bool              `json:"hasTopics"`
	}
	elapsed := coldReadBestOfThree(t, func() {
		first = read()
		if len(first.Topics) != 0 || !first.Refreshing || !first.HasTopics {
			t.Fatalf("withheld phone bootstrap: %+v", first)
		}
	})
	if elapsed >= 3*time.Duration(topicColdProofBudgetMillis)*time.Millisecond {
		t.Fatalf("withheld phone bootstrap exceeded budget: best=%s", elapsed)
	}
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='done' WHERE id LIKE 'session-work-%'`); err != nil {
		t.Fatal(err)
	}
	unblock()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-events:
			var event BrowserEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Event != "topics:updated" {
				continue
			}
			full := read()
			if full.Refreshing {
				continue
			}
			if len(full.Topics) != topicRegistryLimit || !full.HasTopics {
				t.Fatalf("phone publication: %+v", full)
			}
			a.closedEyes.enabled.Store(true)
			paused := read()
			if len(paused.Topics) != 0 || paused.Refreshing || paused.HasTopics {
				t.Fatalf("Closed Eyes phone metadata: %+v", paused)
			}
			return
		case <-deadline.C:
			t.Fatal("phone never received completed publication")
		}
	}
}

func mobileTopicTestFrame(t *testing.T, body string) {
	t.Helper()
	plaintext, err := json.Marshal(mobileResponsePlaintext{ID: uuid.NewString(), Status: http.StatusOK, Body: strings.TrimSuffix(body, "\n")})
	if err != nil || len(plaintext) > mobileMaxMessagePlaintext {
		t.Fatalf("frame = %d bytes, %v", len(plaintext), err)
	}
	env, err := sealMobileEnvelope(make([]byte, 32), uuid.NewString(), uuid.NewString(), mobileDirH2D, 1, mobileKindResponse, plaintext)
	if err != nil {
		t.Fatalf("seal page: %v", err)
	}
	opened, err := openMobileEnvelope(make([]byte, 32), env, env.Hub, env.Dev, mobileDirH2D)
	if err != nil || string(opened) != string(plaintext) {
		t.Fatalf("open page: %v", err)
	}
}

func TestMobileTopicScopesAndRouteFamilies(t *testing.T) {
	bridge := &mobileBridge{}
	for _, test := range []struct{ method, path, family, wrongScope string }{
		{http.MethodGet, "/topics", "", mobileScopeChatRead},
		{http.MethodGet, "/topics/project_abc", "GET /topics/*", mobileScopeBriefRead},
		{http.MethodPost, "/topics/project_abc/messages", "POST /topics/*/messages", mobileScopeBriefRead},
	} {
		if got := mobileRouteFamily(test.method, test.path); got != test.family {
			t.Fatalf("family %s = %s", test.path, got)
		}
		for _, scopes := range [][]string{nil, {test.wrongScope}} {
			response := mobileTestChatCall(bridge, mobileTestDevice(scopes...), test.method, test.path, `{}`)
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s scopes %v = %d", test.path, scopes, response.Code)
			}
		}
	}
	for _, path := range []string{"/topics/a/other", "/topics/a/messages/extra"} {
		if response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead, mobileScopeChatRead, mobileScopeChatWrite), http.MethodGet, path, ""); response.Code != http.StatusNotFound {
			t.Fatalf("extra route accepted: %s", path)
		}
	}
}

func TestMobileTopicListMatchesTodayAndStatusCapability(t *testing.T) {
	bridge, _, _ := mobileTopicTestBridge(t)
	interest := testInterest("following", "Wearable computers", "Current research")
	interest.Evidence[0].At = bridge.app.interestsSvc().now().Add(-2 * time.Hour).UnixMilli()
	storeTestInterests(t, bridge.app, interest)
	expected, err := collectAndReadTopicViewsForTest(bridge.app)
	if err != nil {
		t.Fatal(err)
	}
	topics := mobileTopicTestList(t, bridge)
	if len(topics) != len(expected) || len(topics) != 2 {
		t.Fatalf("list=%+v Today=%+v", topics, expected)
	}
	for i, topic := range topics {
		want := expected[i]
		if topic.ID != want.ID || topic.Name != want.Name || topic.Kind != want.Kind || topic.Status != want.Status || topic.NewStories != want.NewStories {
			t.Fatalf("topic=%+v want=%+v", topic, want)
		}
	}
	response := mobileTestChatCall(bridge, mobileTestDevice(), http.MethodGet, "/status", "")
	var status mobileStatusView
	if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &status); err != nil || response.Code != http.StatusOK || !status.Topics || status.ProtocolVersion != 1 {
		t.Fatalf("status=%+v %v", status, err)
	}
	// Older phones decode their existing fields and ignore the new capability.
	var legacy struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &legacy); err != nil || legacy.ProtocolVersion != 1 {
		t.Fatalf("legacy status=%+v %v", legacy, err)
	}
	raw, _ := json.Marshal(mobileStatusView{})
	if strings.Contains(string(raw), `"topics"`) {
		t.Fatal("false capability must be omitted")
	}
}

func TestMobileTopicPrivacyWithdrawalAndUnknownIDs(t *testing.T) {
	for _, change := range []string{"closed_eyes", "sensitive", "private", "protected", "forgotten"} {
		t.Run(change, func(t *testing.T) {
			bridge, tag, id := mobileTopicTestBridge(t)
			if len(mobileTopicTestList(t, bridge)) != 1 {
				t.Fatal("missing safe control")
			}
			seedTopicMessage(t, bridge.app, tag, "retained", id, time.Now().UnixMilli())
			switch change {
			case "closed_eyes":
				storeTestInterests(t, bridge.app, testInterest("public-following", "Public research", "Current research"))
				bridge.app.closedEyes.enabled.Store(true)
			case "sensitive":
				pii.SetUserSensitiveApps(map[string]string{"Code": "Code"})
			case "private":
				if _, err := bridge.app.activity.store.db.Exec(`UPDATE activity_events SET data='{"windowTitle":"Atlas - Incognito"}' WHERE session_id='session-atlas'`); err != nil {
					t.Fatal(err)
				}
			case "protected":
				if _, err := bridge.app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-atlas'`); err != nil {
					t.Fatal(err)
				}
			case "forgotten":
				if err := bridge.app.activity.store.DeleteSession("session-atlas"); err != nil {
					t.Fatal(err)
				}
			}
			if change == "private" || change == "protected" {
				if topics := mobileTopicTestList(t, bridge); len(topics) != 1 {
					t.Fatalf("%s out-of-band change altered the published snapshot: %+v", change, topics)
				}
				rebuilt, err := collectAndReadTopicViewsForTest(bridge.app)
				if err != nil || len(rebuilt) != 0 {
					t.Fatalf("%s source survived explicit rebuild: %+v %v", change, rebuilt, err)
				}
			}
			if topics := mobileTopicTestList(t, bridge); len(topics) != 0 {
				t.Fatalf("%s leaked %+v", change, topics)
			}
			want := http.StatusNotFound
			if change == "closed_eyes" {
				want = http.StatusConflict
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				path, body := "/topics/"+id, ""
				if method == http.MethodPost {
					path += "/messages"
					body = `{"text":"Review","idempotencyKey":"refused"}`
				}
				response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead, mobileScopeChatRead, mobileScopeChatWrite), method, path, body)
				if response.Code != want || strings.Contains(response.Body.String(), "retained") {
					t.Fatalf("%s %s: %d %s", change, method, response.Code, response.Body.String())
				}
				if change == "closed_eyes" && mobileTestDecodeReply(t, response).Error != errTodayChatObservationPaused.Error() {
					t.Fatal("desktop refusal changed")
				}
			}
		})
	}
	bridge, _, _ := mobileTopicTestBridge(t)
	for _, id := range []string{"unknown", strings.Repeat("x", 129)} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path, body := "/topics/"+id, ""
			if method == http.MethodPost {
				path += "/messages"
				body = `{"text":"Review","idempotencyKey":"unknown"}`
			}
			response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead, mobileScopeChatRead, mobileScopeChatWrite), method, path, body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("unknown %s: %d %s", method, response.Code, response.Body.String())
			}
		}
	}
}

func TestMobileTopicPagingFrameBudgetAndPhoneProjection(t *testing.T) {
	bridge, tag, id := mobileTopicTestBridge(t)
	seedTopicEvent(t, bridge.app, "phone-event", "Code", "done", time.Now().Add(-time.Hour).UnixMilli())
	// Quote-heavy messages exercise both JSON escape layers and force the
	// route to shrink its default 50-row desktop page.
	for i := 0; i < 32; i++ {
		if _, err := bridge.app.store.AppendUserStreamItemAtomic(t.Context(), tag, storage.StreamItemRow{ID: fmt.Sprintf("large-%02d", i), Kind: "user", TopicID: id, Content: strings.Repeat(`"`, mobileChatTextMax*2), Images: "desktop-only-attachment", AutomationContext: "desktop-only-context"}); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	pages, events := 0, 0
	for {
		response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/topics/"+id+"?limit=50&cursor="+url.QueryEscape(cursor), "")
		if response.Code != http.StatusOK {
			t.Fatalf("page: %d %s", response.Code, response.Body.String())
		}
		mobileTopicTestFrame(t, response.Body.String())
		if strings.Contains(response.Body.String(), "desktop-only") {
			t.Fatal("desktop metadata leaked")
		}
		var page mobileTopicPage
		if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &page); err != nil {
			t.Fatal(err)
		}
		if page.SessionID != tag || page.TopicID != id {
			t.Fatalf("wrong conversation: %+v", page)
		}
		for i, item := range page.Items {
			if i > 0 && (page.Items[i-1].At > item.At || (page.Items[i-1].At == item.At && page.Items[i-1].ID > item.ID)) {
				t.Fatal("page not newest last")
			}
			if seen[item.ID] {
				t.Fatalf("repeated item %s", item.ID)
			}
			seen[item.ID] = true
			if item.Kind == "event" {
				events++
			}
			if item.Message != nil && len(item.Message.Text) > mobileChatTextMax {
				t.Fatal("unbounded message")
			}
		}
		pages++
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor || pages > 40 {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if pages < 2 || len(seen) != 33 || events != 1 {
		t.Fatalf("pages=%d items=%d events=%d", pages, len(seen), events)
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=bad", "cursor=bad", "cursor=" + url.QueryEscape(strings.Repeat("x", 6001))} {
		response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/topics/"+id+"?"+query, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", query, response.Code, response.Body.String())
		}
	}
}

func TestMobileTopicSendReceiptsAndPhoneAuthority(t *testing.T) {
	bridge, tag, id := mobileTopicTestBridge(t)
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 2)}
	bridge.app.runner = runner
	device := mobileTestDevice(mobileScopeChatWrite) // No additional read scope.
	path := "/topics/" + id + "/messages"
	body := `{"text":"Review this release","idempotencyKey":"send-topic"}`
	first := mobileTestChatCall(bridge, device, http.MethodPost, path, body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", first.Code, first.Body.String())
	}
	var accepted struct {
		TurnID string `json:"turnId"`
		Seq    int64  `json:"seq"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, first).Data, &accepted); err != nil || !mobileValidUUID(accepted.TurnID) || accepted.Seq < 1 {
		t.Fatalf("acceptance %+v %v", accepted, err)
	}
	select {
	case task := <-runner.inputs:
		prompt := task.Prompt
		if task.PromptFunc != nil {
			prompt, _ = task.PromptFunc()
		}
		if !strings.Contains(prompt, "can be changed only on the computer") || !strings.Contains(prompt, "Atlas") || !strings.HasSuffix(prompt, "Review this release") {
			t.Fatal("topic context or phone origin missing")
		}
		// A phone turn gets GoalCreate and the read-only SearchWeb (#925), nothing
		// that changes the computer.
		for _, tool := range task.ExtraTools {
			if tool.Name() != goalToolCreate && tool.Name() != chatWebSearchToolName {
				t.Fatalf("computer tool exposed: %s", tool.Name())
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner not called")
	}
	waitForTagIdle(t, bridge.app, tag)
	// Receipts survive bridge restart and replay even after source withdrawal.
	replacement, err := newMobileBridge(bridge.app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replacement.Stop)
	bridge.app.closedEyes.enabled.Store(true)
	second := mobileTestChatCall(replacement, device, http.MethodPost, path, body)
	if second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("replay %d %s", second.Code, second.Body.String())
	}
	conflict := mobileTestChatCall(replacement, device, http.MethodPost, "/topics/other/messages", body)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("cross-route key: %d %s", conflict.Code, conflict.Body.String())
	}
	rows, err := bridge.app.store.LoadStreamItems(t.Context(), tag)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, row := range rows {
		if row.Kind == "user" {
			users++
			if row.Content != "Review this release" {
				t.Fatalf("context in bubble: %s", row.Content)
			}
		}
	}
	if users != 1 || len(runner.inputs) != 0 {
		t.Fatalf("duplicate turn: %d users, %d tasks", users, len(runner.inputs))
	}
}

func TestMobileTopicReadFailsClosedDuringWithdrawal(t *testing.T) {
	bridge, _, id := mobileTopicTestBridge(t)
	bridge.app.topicPageBeforePublish = func() { bridge.app.closedEyes.enabled.Store(true) }
	response := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/topics/"+id, "")
	if response.Code < 400 || strings.Contains(response.Body.String(), "Atlas release ready") {
		t.Fatalf("withdrawal published: %d %s", response.Code, response.Body.String())
	}
}

func TestMobileTopicListBoundsTextAndCount(t *testing.T) {
	bridge, _, _ := mobileTopicTestBridge(t)
	for i := 0; i < 14; i++ {
		seedThreadMoment(t, bridge.app, threadFixtureMoment{id: fmt.Sprintf("list-%d", i), title: fmt.Sprintf("Project%02d", i), app: "Code", start: time.Now().Add(-time.Hour), end: time.Now().Add(-time.Minute), entities: []Entity{{Type: entityProject, Name: fmt.Sprintf("Project%02d", i)}}})
	}
	if _, err := collectAndReadTopicViewsForTest(bridge.app); err != nil {
		t.Fatal(err)
	}
	if got := mobileTopicTestList(t, bridge); len(got) != topicRegistryLimit {
		t.Fatalf("topic count=%d", len(got))
	}
	long := strings.Repeat("\x00\"中", 1024)
	view := mobileTopicProjection(TopicView{ID: "stable-id", Kind: TopicKindProject, Name: long, Status: TopicStatusToday, Latest: &TopicMoment{Text: long}, Next: &TopicMoment{Text: long}, PeopleLine: &TopicPeopleLine{Names: []string{long, long, long, long}, Count: 4}, Why: &TopicWhy{InterestWhy: long}})
	if view.ID != "stable-id" || len(view.Name) > 160 || len(view.Latest.Text) > 384 || len(view.Next.Text) > 384 || len(view.Why.InterestWhy) > 384 || len(view.PeopleLine.Names) != 3 || view.PeopleLine.Count != 4 {
		t.Fatalf("unbounded topic: %+v", view)
	}
	for _, name := range view.PeopleLine.Names {
		if len(name) > 96 {
			t.Fatal("unbounded person name")
		}
	}
	if !mobileTopicResponseFits(map[string]any{"topics": []mobileTopicView{view}}) {
		t.Fatal("bounded topic does not fit")
	}
}

func TestMobileTopicSendValidationAndBusyHaveNoWrites(t *testing.T) {
	bridge, tag, id := mobileTopicTestBridge(t)
	device := mobileTestDevice(mobileScopeChatWrite)
	path := "/topics/" + id + "/messages"
	for _, body := range []string{`{}`, `{"text":"Hi"}`, `{"text":" ","idempotencyKey":"key"}`, `{"text":"Hi","idempotencyKey":"key","topicId":"other"}`, `{"text":"Hi","idempotencyKey":"key","ownerVerified":true}`} {
		response := mobileTestChatCall(bridge, device, http.MethodPost, path, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d %s", response.Code, response.Body.String())
		}
	}
	bridge.app.runner = &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	bridge.app.activeTags[tag] = true
	response := mobileTestChatCall(bridge, device, http.MethodPost, path, `{"text":"Hi","idempotencyKey":"busy"}`)
	if response.Code != http.StatusConflict || mobileTestDecodeReply(t, response).Error != "busy" {
		t.Fatalf("busy: %d %s", response.Code, response.Body.String())
	}
	rows, err := bridge.app.store.LoadStreamItems(t.Context(), tag)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Kind == "user" {
			t.Fatal("rejected send persisted user message")
		}
	}
}

func TestMobileTopicDetailRequiresChatRead(t *testing.T) {
	bridge, tag, id := mobileTopicTestBridge(t)
	seedTopicMessage(t, bridge.app, tag, "scope-transcript", id, time.Now().UnixMilli())
	briefReader := mobileTestDevice(mobileScopeBriefRead)
	list := mobileTestChatCall(bridge, briefReader, http.MethodGet, "/topics", "")
	if list.Code != http.StatusOK {
		t.Fatalf("brief reader list: %d %s", list.Code, list.Body.String())
	}
	detail := mobileTestChatCall(bridge, briefReader, http.MethodGet, "/topics/"+id, "")
	if detail.Code != http.StatusForbidden {
		t.Errorf("brief-only phone received transcript: %d %s", detail.Code, detail.Body.String())
	}
	detail = mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/topics/"+id, "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "scope-transcript") {
		t.Errorf("chat reader detail: %d %s", detail.Code, detail.Body.String())
	}
}

type mobileTopicAdmissionRunner struct {
	upcomingCaptureRunner
	release <-chan struct{}
}

func (runner *mobileTopicAdmissionRunner) ExecuteTask(ctx context.Context, input *agent.TaskInput, _ agent.StreamCallback) (string, error) {
	runner.inputs <- input
	select {
	case <-runner.release:
		return "topic-admission-run", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestMobileTopicReceiptRetryDuringAdmission(t *testing.T) {
	bridge, tag, id := mobileTopicTestBridge(t)
	admissionStarted, releaseAdmission := make(chan struct{}), make(chan struct{})
	retryWaiting, releaseRetry := make(chan struct{}), make(chan struct{})
	releaseRunner := make(chan struct{})
	var admissionOnce, releaseAdmissionOnce, releaseRetryOnce sync.Once
	defer func() {
		releaseAdmissionOnce.Do(func() { close(releaseAdmission) })
		releaseRetryOnce.Do(func() { close(releaseRetry) })
		close(releaseRunner)
		waitForTagIdle(t, bridge.app, tag)
	}()
	runner := &mobileTopicAdmissionRunner{upcomingCaptureRunner: upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 2)}, release: releaseRunner}
	bridge.app.runner = runner
	bridge.app.todayChatCollectionObserver = func() {
		bridge.app.mu.Lock()
		active := bridge.app.activeTags[tag]
		bridge.app.mu.Unlock()
		if active {
			admissionOnce.Do(func() { close(admissionStarted); <-releaseAdmission })
		}
	}
	var attempts atomic.Int32
	mobileTopicSendBeforeLock = func() {
		if attempts.Add(1) == 2 {
			close(retryWaiting)
			<-releaseRetry
		}
	}
	defer func() { mobileTopicSendBeforeLock = nil }()
	device := mobileTestDevice(mobileScopeChatWrite)
	path, body := "/topics/"+id+"/messages", `{"text":"Review this release","idempotencyKey":"admission-retry"}`
	first, retry := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- mobileTestChatCall(bridge, device, http.MethodPost, path, body) }()
	select {
	case <-admissionStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first send did not enter admission")
	}
	go func() { retry <- mobileTestChatCall(bridge, device, http.MethodPost, path, body) }()
	select {
	case <-retryWaiting:
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not reach send serialization during admission")
	}
	// The retry has reached the lock boundary while the first admission is
	// blocked. Let the first finish saving its receipt before the retry proceeds.
	releaseAdmissionOnce.Do(func() { close(releaseAdmission) })
	var accepted *httptest.ResponseRecorder
	select {
	case accepted = <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("first admission did not finish")
	}
	releaseRetryOnce.Do(func() { close(releaseRetry) })
	var replay *httptest.ResponseRecorder
	select {
	case replay = <-retry:
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not finish")
	}
	if accepted.Code != http.StatusAccepted || replay.Code != accepted.Code || replay.Body.String() != accepted.Body.String() {
		t.Fatalf("admission retry differs: %d %s / %d %s", accepted.Code, accepted.Body.String(), replay.Code, replay.Body.String())
	}
	select {
	case <-runner.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted turn did not reach the runner")
	}
	rows, err := bridge.app.store.LoadStreamItems(t.Context(), tag)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, row := range rows {
		if row.Kind == "user" {
			users++
		}
	}
	if users != 1 || len(runner.inputs) != 0 {
		t.Fatalf("retry duplicated turn: %d messages, %d tasks", users, len(runner.inputs))
	}
}
