package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/slack-go/slack"

	"wails-gui/internal/destinations"
	"wails-gui/internal/sop"
	"wails-gui/storage"
)

func TestLeavesPresenceAndUnknownCounts(t *testing.T) {
	off := leavesPaths{}
	empty := off.summary(nil, nil)
	if empty.WebSearches != nil || empty.Email != nil || empty.Texts != nil || empty.ChatApps != nil || empty.AgentsAsk != nil || empty.UsageCounts.On {
		t.Fatalf("off: %+v", empty)
	}
	on := leavesPaths{search: true, email: true, texts: true, names: true, agents: true, apps: []string{"telegram"}, usage: true}
	for _, err := range []error{nil, errors.New("read failed")} {
		out := on.summary(nil, err)
		if !out.Email.On || !out.Texts.NamesOn || !out.Texts.On || !out.UsageCounts.On || out.ChatApps.Apps[0] != "telegram" {
			t.Fatalf("on: %+v", out)
		}
		for _, n := range []*int{out.WebSearches.ThisWeek, out.Email.ThisWeek, out.Texts.ThisWeek, out.ChatApps.ThisWeek, out.AgentsAsk.ThisWeek} {
			if (err != nil) != (n == nil) || (n != nil && *n != 0) {
				t.Fatalf("unexpected count %v for error %v", n, err)
			}
		}
	}
	rows := []storage.DestinationCount{
		{ID: destinations.AssistantSearch, Detail: "grok", Calls: 3}, {ID: destinations.AssistantSearch, Detail: "gemini", Calls: 1}, {ID: destinations.Email, Calls: 1}, {ID: destinations.TextRelay, Calls: 2}, {ID: destinations.TextMessages, Calls: 3}, {ID: destinations.AgentsMCPRead, Detail: "codex", Calls: 2}, {ID: destinations.ChatBridges, Calls: 4},
	}
	out := off.summary(rows, nil)
	if out.WebSearches.Service != "gemini" || *out.WebSearches.ThisWeek != 4 || out.Email.On || *out.Email.ThisWeek != 1 || out.Texts.On || *out.Texts.ThisWeek != 5 || out.ChatApps != nil || *out.AgentsAsk.ThisWeek != 2 || *(*out.AgentsAsk.ByAgent)[0].ThisWeek != 2 {
		t.Fatalf("recent sends: %+v", out)
	}
}

type observedDestinationStore struct {
	*storage.Store
	calls chan destinations.Call
}

func (s observedDestinationStore) RecordDestination(ctx context.Context, c destinations.Call) error {
	if err := s.Store.RecordDestination(ctx, c); err != nil {
		return err
	}
	s.calls <- c
	return nil
}

func observeDestinationCalls(t *testing.T) chan destinations.Call {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "counts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ch := make(chan destinations.Call, 16)
	r, err := destinations.StartRecorder(observedDestinationStore{store, ch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return ch
}

func expectDestination(t *testing.T, ch chan destinations.Call, id destinations.ID, detail string) {
	t.Helper()
	select {
	case c := <-ch:
		if c.ID != id || c.Detail != detail {
			t.Fatalf("got %s/%q, want %s/%q", c.ID, c.Detail, id, detail)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing call to %s", id)
	}
}

func TestLeavesChannelSends(t *testing.T) {
	calls := observeDestinationCalls(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"data":{"state":"active"}}`)
	}))
	defer srv.Close()
	relay := mobileRelayClient{origin: srv.URL}
	ctx := t.Context()
	if _, err := relay.smsCall(ctx, "GET", "/v1/hub/sms/subscription", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.emailCall(ctx, "POST", "/v1/hub/email/send", map[string]string{"body": "private"}); err != nil {
		t.Fatal(err)
	}
	expectDestination(t, calls, destinations.Email, "")
	if _, err := relay.smsCall(ctx, "POST", "/v1/hub/sms/send", map[string]string{"body": "private"}); err != nil {
		t.Fatal(err)
	}
	expectDestination(t, calls, destinations.TextRelay, "")
	bot := telegramBotClient{apiURL: srv.URL, http: srv.Client()}
	if err := bot.sendMessage(ctx, 1, 0, "private"); err != nil {
		t.Fatal(err)
	}
	expectDestination(t, calls, destinations.ChatBridges, "")
	wechat := newWeChatAPIClient(srv.URL, "local")
	if err := wechat.SendMessage(&wechatSendMessageRequest{}); err != nil {
		t.Fatal(err)
	}
	expectDestination(t, calls, destinations.ChatBridges, "")
	feishu := feishuLocalPoster{baseURL: srv.URL, token: "local", expire: time.Now().Add(time.Hour)}
	if err := feishu.sendDirectToUser(ctx, "local", "private"); err != nil {
		t.Fatal(err)
	}
	expectDestination(t, calls, destinations.ChatBridges, "")
	slackBot := slackLocalReceiver{api: slack.New("local", slack.OptionAPIURL(srv.URL+"/"))}
	slackBot.notice(ctx, "local", "private")
	expectDestination(t, calls, destinations.ChatBridges, "")
	old := discordgo.EndpointChannelMessages
	t.Cleanup(func() { discordgo.EndpointChannelMessages = old })
	discordgo.EndpointChannelMessages = func(string) string { return srv.URL + "/messages" }
	session, err := discordgo.New("Bot local")
	if err != nil {
		t.Fatal(err)
	}
	discord := discordLocalReceiver{session: session}
	discord.notice("local", "private")
	expectDestination(t, calls, destinations.ChatBridges, "")
}

func TestLeavesReadsSavedPaths(t *testing.T) {
	app := newMCPReadTestApp(t)
	dir := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", dir)
	t.Setenv(topicsStateDirEnvVar, dir)
	t.Setenv(featureSwitchesEnv, "playbooks")
	if err := os.WriteFile(filepath.Join(dir, topicsStateFileName), []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	job := storage.CronJobRow{ID: "news", Name: todayNewsJobName, Enabled: true, SurfaceKind: todayNewsSurfaceKind}
	if err := app.store.SaveCronJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := newEmailChannel(dir).save(emailChannelFile{State: emailStateActive, Address: "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := saveTextChannelConfig(dir, TextChannelConfig{Enabled: true, IncludeNames: true, VerifiedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := saveSlackLocalCredentials(&slackLocalCredentials{BotToken: "xoxb-local", AppToken: "xapp-local"}); err != nil {
		t.Fatal(err)
	}
	mcpReadIssueTestToken(t, "codex")
	read := func() YourDataLeaves {
		t.Helper()
		got, err := app.yourDataLeaves()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	out := read()
	if out.Email != nil || out.WebSearches == nil || out.Texts == nil || !out.Texts.NamesOn || out.AgentsAsk == nil || out.ChatApps == nil || !slices.Equal(out.ChatApps.Apps, []string{"slack"}) {
		t.Fatalf("saved paths: %+v", out)
	}
	if _, err := app.playbookPlans.Update("send", func(p *sop.Plan) { p.Channels = []string{"email"}; p.Schedule = &sop.Schedule{Enabled: true} }); err != nil {
		t.Fatal(err)
	}
	if row := read().Email; row == nil || !row.On || row.AddressMasked != "b***@example.com" {
		t.Fatalf("email: %+v", row)
	}
	t.Setenv(featureSwitchesEnv, "")
	job.Enabled = false
	if err := app.store.SaveCronJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	out = read()
	if out.WebSearches != nil || out.Email != nil {
		t.Fatalf("disabled paths: %+v", out)
	}
}

func TestLeavesFailedCounterReadOnConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", dir)
	t.Setenv(topicsStateDirEnvVar, dir)
	path := filepath.Join(dir, "sessions.db")
	st, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP TABLE destination_calls"); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{store: st}).yourDataLeaves()
	if err != nil || out.WebSearches == nil || out.WebSearches.ThisWeek != nil {
		t.Fatalf("summary after failed read: %+v, %v", out, err)
	}
}

func TestLeavesIncompleteAppliesToDestinationAndAgents(t *testing.T) {
	unreadable := (leavesPaths{}).summary([]storage.DestinationCount{{ID: destinations.Email, Incomplete: true}}, errors.New("store unavailable"))
	if unreadable.Email == nil || !unreadable.Email.Incomplete || unreadable.Email.ThisWeek != nil {
		t.Fatalf("known loss on failed read: %+v", unreadable.Email)
	}

	out := (leavesPaths{apps: []string{"telegram"}}).summary([]storage.DestinationCount{
		{ID: destinations.TextRelay, Incomplete: true},
		{ID: destinations.TextMessages, Calls: 3},
		{ID: destinations.Email, Incomplete: true},
		{ID: destinations.AssistantSearch, Detail: "gemini", Calls: 2},
		{ID: destinations.AssistantSearch, Incomplete: true},
		{ID: destinations.AgentsMCPRead, Detail: "codex", Calls: 1},
		{ID: destinations.AgentsMCPRead, Incomplete: true},
		{ID: destinations.ChatBridges, Calls: 5},
	}, nil)
	if !out.Email.Incomplete || *out.Email.ThisWeek != 0 || !out.Texts.Incomplete || *out.Texts.ThisWeek != 3 || out.ChatApps.Incomplete {
		t.Fatalf("wrong row flags: %+v", out)
	}
	if !out.WebSearches.Incomplete || out.WebSearches.Service != "gemini" || !out.AgentsAsk.Incomplete || len(*out.AgentsAsk.ByAgent) != 1 || !(*out.AgentsAsk.ByAgent)[0].Incomplete {
		t.Fatalf("wrong detail flags: %+v", out)
	}
}

func TestComposioLeavesSavedStateAndHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", dir)
	t.Setenv(topicsStateDirEnvVar, dir)
	app := newBindingsTestApp(t)
	read := func() *ComposioLeaves {
		t.Helper()
		out, err := app.yourDataLeaves()
		if err != nil {
			t.Fatal(err)
		}
		return out.Composio
	}
	if row := read(); row != nil {
		t.Fatalf("unexpected row before configuration: %+v", row)
	}
	if err := mutateComposioByok(func(st *composioByokState) {
		st.APIKey = "local"
		st.Connections["notion"] = composioByokConnection{Status: composioStatusConnected}
		st.Connections["github"] = composioByokConnection{Status: composioStatusConnecting}
	}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row == nil || !row.Connected || !slices.Equal(row.Apps, []string{"notion"}) || row.ThisWeek == nil || *row.ThisWeek != 0 {
		t.Fatalf("saved connection: %+v", row)
	}
	if out, err := (&App{}).yourDataLeaves(); err != nil || out.Composio == nil || out.Composio.ThisWeek != nil {
		t.Fatalf("unavailable counters: %+v, %v", out.Composio, err)
	}
	if err := app.ClearComposioApiKey(); err != nil {
		t.Fatal(err)
	}
	if err := app.store.RecordDestination(t.Context(), destinations.Call{ID: destinations.Composio, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row == nil || row.Connected || row.Apps == nil || len(row.Apps) != 0 || row.ThisWeek == nil || *row.ThisWeek != 1 {
		t.Fatalf("disconnected with recent send: %+v", row)
	}
	path, err := composioByokPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.yourDataLeaves(); err == nil {
		t.Fatal("unreadable Composio state must fail the summary")
	}
}
