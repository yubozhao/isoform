package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/models"
)

func TestAgentProbeCacheInstantReadAndBackgroundRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.json")
	old := AgentProbe{ProbedAt: "old", Agents: []SetupAgent{{ID: "claude", Installed: true, Version: "old"}, {ID: "codex"}, {ID: "ollama"}}}
	if err := saveSetupJSON(path, old); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	events := make(chan AgentProbe, 3)
	var c agentProbeCache
	c.init(path, func(ctx context.Context, id string) SetupAgent {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return SetupAgent{ID: id, Installed: true, SignedIn: true, Version: "new"}
	}, func(p AgentProbe) { events <- p }, nil)
	var done <-chan struct{}
	var cached AgentProbe
	finishesWhileHeld(t, "a cache read during a probe", func() {
		done = c.refresh(true)
		cached = c.snapshot()
	})
	if cached.Agents[0].Version != "old" {
		t.Fatal("cache read waited for probe")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh stalled")
	}
	if c.snapshot().ProbedAt == "old" || !c.snapshot().Agents[0].SignedIn {
		t.Fatal("did not refresh")
	}
	if len(events) != 3 {
		t.Fatal("missing partial update events")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		t.Fatal("cache not persisted")
	}
}
func TestAgentProbeFreshInstallUnknownRows(t *testing.T) {
	var c agentProbeCache
	c.init(filepath.Join(t.TempDir(), "missing.json"), nil, nil, nil)
	p := c.snapshot()
	if p.ProbedAt != "" || len(p.Agents) != 3 {
		t.Fatal(p)
	}
	for _, row := range p.Agents {
		if !row.NotFoundYet {
			t.Fatal("unknown treated as absent")
		}
	}
}
func TestPermissionSequenceOrderDenialAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	var order []string
	var events []SetupPermissions
	err := sequencePermissions(path, func(key string) bool { order = append(order, key); return key != remindersPermissionKey }, func(p SetupPermissions) {
		copy := p
		copy.Answers = append([]SetupPermissionAnswer{}, p.Answers...)
		events = append(events, copy)
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, setupPermissionOrder) {
		t.Fatalf("order %+v", order)
	}
	if len(events) != len(setupPermissionOrder)+1 {
		t.Fatalf("events %d", len(events))
	}
	p := loadSetupPermissions(path)
	if p.ResumeStep != "sees" || p.Running || p.Next != len(setupPermissionOrder) {
		t.Fatal(p)
	}
	if p.Answers[1].State != "denied" || p.Answers[len(p.Answers)-1].State != "onAfterReopen" {
		t.Fatal(p)
	}
	// After reopen native permission checks make the screen available; no OS
	// prompt is replayed merely by loading the persisted state.
	count := len(order)
	_ = loadSetupPermissions(path)
	if len(order) != count {
		t.Fatal("resume prompted")
	}
	err = sequencePermissions(path, func(string) bool { return true }, func(SetupPermissions) {}, true)
	if err != nil {
		t.Fatal(err)
	}
	if loadSetupPermissions(path).Answers[6].State != "on" {
		t.Fatal("reopened screen stayed pending")
	}
}
func TestMissingModelsNeverDownloadFromFeatureConsumers(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureLocalModel(dir, nil); err == nil {
		t.Fatal("missing memory model ready")
	}
	if err := models.Require(dir, "not-in-manifest"); err == nil {
		t.Fatal("unlisted model allowed")
	}
	if models.Present(dir, "sensevoice") {
		t.Fatal("empty install downloaded")
	}
}

func TestAgentProbeFreshLaunchDeadline(t *testing.T) {
	a := &App{}
	release := make(chan struct{})
	dir := t.TempDir()
	a.setupAgents.init(filepath.Join(dir, "probe.json"), func(ctx context.Context, id string) SetupAgent {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return SetupAgent{ID: id}
	}, nil, nil)
	t.Cleanup(func() {
		close(release)
		a.setupAgents.waitIdle()
	})
	start := time.Now()
	a.prepareAgentProbe()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fresh Welcome delayed %v", elapsed)
	}
	for _, row := range a.setupAgents.snapshot().Agents {
		if !row.NotFoundYet {
			t.Fatal("slow probe claimed absence")
		}
	}
}

func TestAgentProbePollsOnlyWhileWelcomeVisible(t *testing.T) {
	a := &App{}
	var calls atomic.Int32
	dir := t.TempDir()
	a.setupAgents.init(filepath.Join(dir, "probe.json"), func(ctx context.Context, id string) SetupAgent { calls.Add(1); return SetupAgent{ID: id} }, nil, nil)
	t.Cleanup(func() {
		a.SetSetupWelcomeVisible(false)
		a.setupAgents.waitIdle()
	})
	a.SetSetupWelcomeVisible(true)
	deadline := time.After(time.Second)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("Welcome did not refresh")
		case <-time.After(time.Millisecond):
		}
	}
	a.SetSetupWelcomeVisible(false)
	a.setupAgents.waitIdle()
	before := calls.Load()
	time.Sleep(3100 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("probe polled after Welcome unmounted")
	}
}

func TestPermissionStateReflectsRevokedGrants(t *testing.T) {
	if got := setupPermissionCurrentState("on", false, false); got != "off" {
		t.Fatalf("revoked grant remained %s", got)
	}
	if got := setupPermissionCurrentState("onAfterReopen", true, false); got != "on" {
		t.Fatalf("reopened grant remained %s", got)
	}
	if got := setupPermissionCurrentState("off", true, true); got != "onAfterReopen" {
		t.Fatalf("new screen grant became %s", got)
	}
}

func TestMissingOllamaModelStaysNotReadyWithoutPull(t *testing.T) {
	var pulls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			pulls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "")
	provider := resolveEmbeddingProvider(t.TempDir(), MemoryStoreConfig{Provider: "ollama", BaseURL: server.URL}, nil)
	if provider != nil {
		t.Fatal("missing Ollama weights reported a ready provider")
	}
	if pulls.Load() != 0 {
		t.Fatal("memory attempted an implicit Ollama pull")
	}
}

func TestPermissionDisplayDoesNotQueryMessagesAutomation(t *testing.T) {
	rig := newTextChannelRig(t)
	if err := rig.ch.mutateState(func(s *textChannelState) bool { s.MessagesAllowed = true; return true }); err != nil {
		t.Fatal(err)
	}
	rig.ch.automation = func(bool) textAutomationState {
		t.Fatal("a permission display queried Apple Events")
		return textAutomationUnknown
	}
	if !rig.ch.cachedAutomationAllowed() {
		t.Fatal("lost the recorded display grant")
	}
	status, err := rig.ch.Status()
	if err != nil || !status.PermissionGranted {
		t.Fatalf("cached status: %+v, %v", status, err)
	}
}

func TestPermissionSequenceAsksTheDayFirstAndScreenLast(t *testing.T) {
	want := []string{calendarPermissionKey, remindersPermissionKey, "contacts", mailPermissionKey, "microphone", "system_audio", "accessibility", "screen_recording"}
	if !reflect.DeepEqual(setupPermissionOrder, want) {
		t.Fatalf("order %v", setupPermissionOrder)
	}
	if slices.Contains(setupPermissionOrder, "notifications") {
		t.Fatal("notifications are asked on screen 5, not by Turn on")
	}
}

func TestPermissionSequenceRecordsMailUnavailableWithoutAsking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	var asked []string
	err := sequencePermissionsWith(path,
		func(key string) bool { asked = append(asked, key); return true },
		func(key string) bool { return key != mailPermissionKey },
		func(SetupPermissions) {}, true)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(asked, mailPermissionKey) {
		t.Fatal("Mail was asked while it was not running")
	}
	p := loadSetupPermissions(path)
	for _, answer := range p.Answers {
		want := "on"
		if answer.Key == mailPermissionKey {
			want = setupPermissionUnavailable
		}
		if answer.State != want {
			t.Fatalf("%s = %s, want %s", answer.Key, answer.State, want)
		}
	}
}
