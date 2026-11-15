package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wails-gui/storage"
	agent "yansu-agent/agent"
	"yansu-agent/agent/authority"
)

// writeGateRunner replays a fixed list of stream events and records the task
// input it was given.
type writeGateRunner struct {
	input  *agent.TaskInput
	events []agent.StreamEvent
}

func (r *writeGateRunner) Init(context.Context) error { return nil }
func (r *writeGateRunner) IsReady() bool              { return true }
func (r *writeGateRunner) ExecuteTask(_ context.Context, input *agent.TaskInput, emit agent.StreamCallback) (string, error) {
	r.input = input
	for i := range r.events {
		event := r.events[i]
		if err := emit(&event); err != nil {
			return "", err
		}
	}
	return "write-gate-run", nil
}
func (r *writeGateRunner) CancelRun(string) error                   { return nil }
func (r *writeGateRunner) RespondToPermission(string, string) error { return nil }
func (r *writeGateRunner) CancelPermission(string)                  {}
func (r *writeGateRunner) Shutdown() error                          { return nil }

// A write the policy denied never touched the file, so it must not make that
// file a run-written deliverable: a denied Write to ~/.ssh/id_ed25519 followed
// by a deliverables block naming it would otherwise upload the key.
func TestDeniedWriteIsNeverUploadable(t *testing.T) {
	home := setTestHome(t)
	app, _, _ := newBgShellTestApp(t)
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	report := filepath.Join(workDir, "report.md")
	if err := os.WriteFile(report, []byte("# Report"), 0o644); err != nil {
		t.Fatal(err)
	}
	unfinished := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(unfinished, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &writeGateRunner{events: []agent.StreamEvent{
		{Type: "tool_use", ToolName: "Write", ToolUseID: "w1", ToolInput: map[string]any{"path": key, "content": "x"}},
		{Type: "tool_result", ToolName: "Write", ToolUseID: "w1", Content: "Blocked by authority policy: unattended local write has no matching scoped authority grant (tool.write)."},
		{Type: "tool_use", ToolName: "Write", ToolUseID: "w2", ToolInput: map[string]any{"path": report, "content": "# Report"}},
		{Type: "tool_result", ToolName: "Write", ToolUseID: "w2", Content: "File created", ToolSucceeded: true},
		// A call whose result never arrives does not count either.
		{Type: "tool_use", ToolName: "Edit", ToolUseID: "w3", ToolInput: map[string]any{"path": unfinished}},
		{Type: "assistant", Content: "Report written."},
	}}
	app.runner = runner

	_, _, written, err := app.executePromptSync(context.Background(), "chat-1", "", workDir, workDir,
		"write the report", "m", "", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	canonicalReport, _ := filepath.EvalSymlinks(report)
	if len(written) != 1 || written[0] != canonicalReport {
		t.Fatalf("written = %v, want only %s", written, canonicalReport)
	}
	if runner.input == nil || !runner.input.DenyExternalWrites || !runner.input.AutoApproveTools {
		t.Fatalf("task input = %+v, want an unattended run with the deny list on", runner.input)
	}

	writtenSet := map[string]struct{}{}
	for _, p := range written {
		writtenSet[p] = struct{}{}
	}
	files, skipped := readDeliverableFiles([]string{key, unfinished, report}, workDir, "", writtenSet)
	if len(files) != 1 || files[0].Filename != "report.md" {
		t.Fatalf("uploaded %+v, want only report.md", files)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want the key and the unfinished edit", skipped)
	}
}

func TestCronProjectDirMustNotCoverTheHomeFolder(t *testing.T) {
	app := newAuthorityTestApp(t, newTestStore(t))
	home := os.Getenv("HOME")
	for _, dir := range []string{"/", home, filepath.Dir(home), home + "/", "relative/dir",
		filepath.Join(home, ".isoform-agent"), filepath.Join(home, ".yansu-agent", "memory"),
		filepath.Join(home, configDirName), filepath.Join(home, ".ssh"), filepath.Join(home, ".config", "gh"),
		filepath.Join(home, "Library"), filepath.Join(home, "Library", "Keychains"),
		filepath.Join(home, "Library", "Mobile Documents", "project"), filepath.Join(home, ".aws")} {
		if err := validateCronProjectDir(dir); err == nil {
			t.Errorf("validateCronProjectDir(%q) = nil, want an error", dir)
		}
	}
	for _, dir := range []string{"", filepath.Join(home, "project"), t.TempDir()} {
		if err := validateCronProjectDir(dir); err != nil {
			t.Errorf("validateCronProjectDir(%q) = %v, want nil", dir, err)
		}
	}

	config, _ := json.Marshal(map[string]string{"prompt": "x", "project_dir": home})
	if _, err := app.cronScheduler.AddJob(context.Background(), CronJobInput{
		Name: "home", TargetKind: "agent", TargetConfig: config,
	}); err == nil {
		t.Fatal("AddJob accepted the home folder as a project folder")
	}
	raw := json.RawMessage(config)
	if _, err := app.cronScheduler.UpdateJob(context.Background(), "any", CronJobPatch{TargetConfig: &raw}); err == nil ||
		!strings.Contains(err.Error(), "home folder") {
		t.Fatalf("UpdateJob error = %v, want the home-folder refusal", err)
	}
}

func TestCronRunUsesOnlyItsOwnLogSession(t *testing.T) {
	app, _, _ := newBgShellTestApp(t)
	ctx := context.Background()
	jobID := "job-1234567890"
	if got := app.ownCronSessionTag(ctx, jobID, "chat-1"); got != "" {
		t.Fatalf("a chat's tag was accepted as the job's log session: %q", got)
	}
	for tag, label := range map[string]string{
		"cron-own":   cronSessionLabel(jobID),
		"cron-other": cronSessionLabel("other-987654321"),
	} {
		if err := app.store.SaveSession(ctx, storage.SessionRow{LocalID: tag, ProjectID: "p1", Label: label, Hidden: true}); err != nil {
			t.Fatal(err)
		}
	}
	if got := app.ownCronSessionTag(ctx, jobID, "cron-own"); got != "cron-own" {
		t.Fatalf("own log session = %q, want cron-own", got)
	}
	if got := app.ownCronSessionTag(ctx, jobID, "cron-other"); got != "" {
		t.Fatalf("another job's log session was accepted: %q", got)
	}
	if cronScratchTag(jobID) == "chat-1" || !strings.Contains(cronScratchTag(jobID), jobID) {
		t.Fatalf("scratch tag %q is not derived from the job id", cronScratchTag(jobID))
	}
}

// approvedFilesystemInitiative approves a qualified initiative whose manifest
// grants one scoped filesystem write.
func approvedFilesystemInitiative(t *testing.T) (*App, *ActivityStore, Initiative) {
	t.Helper()
	store := newTestStore(t)
	action := authority.Action{
		Capability: "filesystem.write",
		ToolName:   "Write",
		Effect:     authority.EffectLocalWrite,
		Resource:   "/tmp/yansu-qualified/",
		Reversible: true,
	}
	// A proposal and the grantable manifest the removed shadow qualification
	// left on file.
	proposal := newInitiativeFixture("legacy-filesystem-proposal", "session-filesystem", InitiativeStatusProposed)
	if err := store.SaveInitiative(proposal); err != nil {
		t.Fatal(err)
	}
	insertLegacyRow(t, store, "shadow_capability_qualifications", map[string]any{
		"id": "legacy-qualification", "created_at": 3_900_000,
	})
	if _, _, err := store.SaveCapabilityManifest(CapabilityManifest{
		ID: "legacy-filesystem-manifest", InitiativeID: proposal.ID, QualificationID: "legacy-qualification",
		PolicyVersion: "legacy", PlanHash: "legacy-plan", RiskTier: "local_write", Grantable: true, GrantMode: "scoped_expiring",
		BlockReasons: []string{}, CreatedAt: 3_900_000,
		Effects: []CapabilityManifestEffect{{
			Capability: action.Capability, Effect: action.Effect, Resource: action.Resource, Reversible: action.Reversible,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	app := newAuthorityTestApp(t, store)
	approved, err := app.ApproveInitiative(proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ExecutionRef == "" {
		t.Fatalf("approved initiative has no job: %+v", approved)
	}
	return app, store, approved
}

// A finished initiative's grants are no longer active, but they come back if
// it runs again, so a prompt rewrite must revoke them whatever the status.
func TestCronAPIRewriteRevokesGrantsWhateverTheInitiativeStatus(t *testing.T) {
	app, store, approved := approvedFilesystemInitiative(t)
	if _, err := store.db.Exec(`UPDATE initiatives SET status = 'succeeded' WHERE id = ?`, approved.ID); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"prompt":"a different prompt"}`)
	if _, err := app.updateCronJobFromAPI(approved.ExecutionRef, CronJobPatch{TargetConfig: &raw}); err != nil {
		t.Fatal(err)
	}
	grants, err := store.ListAuthorityGrants(approved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) == 0 {
		t.Fatal("no grants on file")
	}
	for _, g := range grants {
		if g.Status != "revoked" {
			t.Fatalf("grant %s is %q after the rewrite, want revoked", g.ID, g.Status)
		}
	}
	if _, err := store.db.Exec(`UPDATE initiatives SET status = 'approved' WHERE id = ?`, approved.ID); err != nil {
		t.Fatal(err)
	}
	active, err := store.ListActiveAuthorityGrants(approved.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("grants came back after the initiative ran again: %+v", active)
	}
}

func TestCronAPIRewriteIsRefusedWhenGrantsCannotBeRevoked(t *testing.T) {
	app, store, approved := approvedFilesystemInitiative(t)
	before, err := app.cronScheduler.GetJob(approved.ExecutionRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"prompt":"a different prompt"}`)
	if _, err := app.updateCronJobFromAPI(approved.ExecutionRef, CronJobPatch{TargetConfig: &raw}); err == nil {
		t.Fatal("the rewrite was applied although the grants could not be revoked")
	}
	after, err := app.cronScheduler.GetJob(approved.ExecutionRef)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := json.Marshal(before.TargetConfig)
	afterConfig, _ := json.Marshal(after.TargetConfig)
	if string(beforeConfig) != string(afterConfig) {
		t.Fatalf("target config changed from %s to %s", beforeConfig, afterConfig)
	}
}

// A shell grant already on file is never honoured: one would cover every
// command the shell can run.
func TestShellCapabilityNeverBecomesAGrant(t *testing.T) {
	_, fsStore, approved := approvedFilesystemInitiative(t)
	if _, err := fsStore.db.Exec(`INSERT INTO authority_grants (
			id, initiative_id, manifest_id, capability, resource_prefix, effect, expires_at, created_at)
		SELECT 'legacy-shell', initiative_id, manifest_id, 'local.shell.execute', '', 'local_write', expires_at, created_at
		FROM authority_grants WHERE initiative_id = ? LIMIT 1`, approved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fsStore.db.Exec(`INSERT INTO authority_grant_events (id, grant_id, event_type, reason, created_at)
		VALUES ('legacy-shell-issued', 'legacy-shell', 'issued', 'test', ?)`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	active, err := fsStore.ListActiveAuthorityGrants(approved.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range active {
		if g.Capability == "local.shell.execute" {
			t.Fatalf("a legacy shell grant is still honoured: %+v", active)
		}
	}
	if len(active) != 1 {
		t.Fatalf("active grants = %+v, want the filesystem grant only", active)
	}
}

// fakeTelegramAPI answers every Bot API call with ok and records the texts
// the bot sends.
func fakeTelegramAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	sent := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(server.Close)
	return server, &sent
}

func TestTelegramBotAnswersOnlyItsPairedOwner(t *testing.T) {
	setTestHome(t)
	connected := time.Now().Add(-time.Minute)
	creds := telegramLocalCredentials{BotToken: "123:abc", BotID: 123, BotUsername: "mybot",
		ConnectedAt: connected.Format(time.RFC3339)}
	if _, err := ensureTelegramPairing(&creds); err != nil {
		t.Fatal(err)
	}
	if creds.PairingCode == "" {
		t.Fatal("an unpaired bot has no pairing code")
	}
	server, sent := fakeTelegramAPI(t)
	r := newTelegramLocalReceiver(&App{}, creds)
	r.client.apiURL = server.URL
	ctx := context.Background()
	message := func(user int64, chatType, text string) telegramMessage {
		return telegramMessage{Date: time.Now().Unix(), Text: text, From: &telegramUser{ID: user, Username: "u"},
			Chat: telegramChat{ID: user, Type: chatType}}
	}
	if r.senderAllowed(ctx, message(7, "private", "hello")) {
		t.Fatal("the first private sender was accepted without the pairing code")
	}
	if r.senderAllowed(ctx, message(7, "group", creds.PairingCode)) {
		t.Fatal("a group message paired the bot")
	}
	if saved := loadTelegramLocalCredentials(); saved.OwnerUserID != 0 {
		t.Fatalf("owner = %d before pairing", saved.OwnerUserID)
	}
	// The t.me start link sends "/start CODE"; the pairing message itself is
	// not run as a prompt.
	if r.senderAllowed(ctx, message(42, "private", "/start "+strings.ToLower(creds.PairingCode))) {
		t.Fatal("the pairing message was passed on as a prompt")
	}
	saved := loadTelegramLocalCredentials()
	if saved == nil || saved.OwnerUserID != 42 || !saved.OwnerPaired || saved.PairingCode != "" {
		t.Fatalf("saved credentials = %+v, want paired owner 42 and no code", saved)
	}
	if !r.senderAllowed(ctx, message(42, "private", "what is on today")) {
		t.Fatal("the paired owner was refused")
	}
	if !r.senderAllowed(ctx, message(42, "group", "hi")) {
		t.Fatal("the owner's group message was refused")
	}
	if r.senderAllowed(ctx, message(43, "private", creds.PairingCode)) {
		t.Fatal("a stranger with the old code was accepted")
	}
	if len(*sent) < 2 || !strings.Contains(strings.Join(*sent, "\n"), "Paired with Isoform") {
		t.Fatalf("notices sent = %q", *sent)
	}

	// Messages from before the bot was connected are ignored, even the owner's.
	old := message(42, "private", "old")
	old.Date = connected.Add(-time.Hour).Unix()
	before := len(*sent)
	r.handleMessage(ctx, old)
	if len(*sent) != before {
		t.Fatal("a message from before the connect time was answered")
	}

	// An owner saved by an older build (the first sender) must pair again.
	legacy := telegramLocalCredentials{BotToken: "123:abc", BotID: 123, OwnerUserID: 99}
	if changed, err := ensureTelegramPairing(&legacy); err != nil || !changed || legacy.OwnerUserID != 0 || legacy.PairingCode == "" {
		t.Fatalf("legacy owner kept: %+v (changed %v, err %v)", legacy, changed, err)
	}
}
