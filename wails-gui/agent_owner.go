package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AgentOwnerSnapshot has no data until the first server response is cached.
// Data retains additive server fields, including fields from newer servers.
type AgentOwnerSnapshot struct {
	Available bool            `json:"available"`
	Data      json.RawMessage `json:"data"`
}

// OpenAgentOwnerPayoutOnboarding records the displayed terms acceptance and
// opens the returned hosted setup page in the system browser.
func (a *App) OpenAgentOwnerPayoutOnboarding(termsVersion string) error {
	if a.ctx == nil {
		return errors.New("browser unavailable")
	}
	env, err := a.agentOwnerEnv()
	if err != nil {
		return err
	}
	link, err := agentOwnerPayoutOnboarding(env, termsVersion)
	if err != nil {
		return err
	}
	runtime.BrowserOpenURL(a.ctx, link)
	return nil
}

func agentOwnerPayoutOnboarding(env agentOwnerEnv, termsVersion string) (string, error) {
	if strings.TrimSpace(termsVersion) == "" {
		return "", errors.New("missing payout terms version")
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return "", err
	}
	payload := struct {
		AcceptTerms  bool   `json:"acceptTerms"`
		TermsVersion string `json:"termsVersion"`
	}{true, termsVersion}
	data, err := env.client.do(context.Background(), http.MethodPost, "/v1/owner/payouts/onboard", payload, state.Hub, state.Token)
	if err != nil {
		return "", err
	}
	var response struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &response) != nil {
		return "", errors.New("invalid payout onboarding response")
	}
	link, err := url.Parse(response.URL)
	if err != nil || link.Scheme != "https" || link.Hostname() == "" || link.User != nil {
		return "", errors.New("invalid payout onboarding URL")
	}
	agentOwnerInvalidate(env, state.Hub, "payouts", "status")
	return link.String(), nil
}

type agentOwnerCache struct {
	Identity string                     `json:"identity"`
	Entries  map[string]json.RawMessage `json:"entries"`
}

var agentOwnerCacheMu sync.Mutex
var agentOwnerRefreshing = map[string]bool{}
var agentOwnerEpoch = map[string]uint64{}

func agentOwnerIdentity(base, hub string) string {
	h := sha256.Sum256([]byte(base + "\x00" + hub))
	return hex.EncodeToString(h[:])
}

func agentOwnerReadCache(path, identity string) agentOwnerCache {
	cache := agentOwnerCache{Identity: identity, Entries: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	var saved agentOwnerCache
	if json.Unmarshal(data, &saved) == nil && saved.Identity == identity && saved.Entries != nil {
		return saved
	}
	return cache
}

func agentOwnerWriteCache(path string, cache agentOwnerCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-owner-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func agentOwnerPath(kind, id string) (string, error) {
	switch kind {
	case "status":
		return "/v1/owner", nil
	case "speeds":
		return "/v1/speeds", nil
	case "profile":
		return "/v1/owner/profile", nil
	case "jobs":
		return "/v1/owner/chats", nil
	case "balance":
		return "/v1/owner/balance", nil
	case "payouts":
		return "/v1/owner/payouts", nil
	case "blocked":
		return "/v1/owner/clients/blocked", nil
	case "job":
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
			return "", errors.New("invalid job id")
		}
		return "/v1/owner/chats/" + url.PathEscape(id), nil
	default:
		return "", errors.New("unknown owner snapshot")
	}
}

type agentOwnerEnv struct {
	reservation yourAgentEnv
	client      *agentServerClient
	cachePath   string
	emit        func()
	ctx         context.Context
}

func (a *App) agentOwnerEnv() (agentOwnerEnv, error) {
	reservation, err := a.yourAgentEnvFor()
	if err != nil {
		return agentOwnerEnv{}, err
	}
	client, err := newAgentServerClient(os.Getenv("ISOFORM_SERVER_URL"))
	if err != nil {
		return agentOwnerEnv{}, err
	}
	return agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(filepath.Dir(reservation.stateFile), "agent-owner-cache.json"), ctx: a.ctx, emit: func() { a.emitBrowserEvent("agent-owner:changed") }}, nil
}

func agentOwnerGet(env agentOwnerEnv, kind, id string) (AgentOwnerSnapshot, error) {
	path, err := agentOwnerPath(kind, id)
	if err != nil {
		return AgentOwnerSnapshot{}, err
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return AgentOwnerSnapshot{}, err
	}
	identity := agentOwnerIdentity(env.client.base, state.Hub)
	agentOwnerCacheMu.Lock()
	cache := agentOwnerReadCache(env.cachePath, identity)
	value, ok := cache.Entries[path]
	ownerKey := env.cachePath + "\x00" + identity
	epoch := agentOwnerEpoch[ownerKey]
	key := fmt.Sprintf("%s\x00%s\x00%d", ownerKey, path, epoch)
	if kind == "jobs" && ok {
		agentOwnerWatchJobs(env, identity, value)
	}
	if !agentOwnerRefreshing[key] {
		agentOwnerRefreshing[key] = true
		go agentOwnerRefresh(env, path, identity, ownerKey, key, epoch)
	}
	agentOwnerCacheMu.Unlock()
	return AgentOwnerSnapshot{Available: ok, Data: value}, nil
}

func agentOwnerRefresh(env agentOwnerEnv, path, identity, ownerKey, key string, epoch uint64) {
	defer func() {
		agentOwnerCacheMu.Lock()
		delete(agentOwnerRefreshing, key)
		agentOwnerCacheMu.Unlock()
	}()
	state, err := loadYourAgentState(env.reservation)
	if err != nil || agentOwnerIdentity(env.client.base, state.Hub) != identity {
		return
	}
	data, err := env.client.do(context.Background(), http.MethodGet, path, nil, state.Hub, state.Token)
	if err != nil {
		return
	}
	// The current identity is checked again before saving an asynchronous read.
	state, err = loadYourAgentState(env.reservation)
	if err != nil || agentOwnerIdentity(env.client.base, state.Hub) != identity {
		return
	}
	agentOwnerCacheMu.Lock()
	if agentOwnerEpoch[ownerKey] != epoch {
		agentOwnerCacheMu.Unlock()
		return
	}
	cache := agentOwnerReadCache(env.cachePath, identity)
	changed := string(cache.Entries[path]) != string(data)
	cache.Entries[path] = data
	if changed {
		err = agentOwnerWriteCache(env.cachePath, cache)
	}
	if path == "/v1/owner/chats" && err == nil {
		agentOwnerWatchJobs(env, identity, data)
	}
	agentOwnerCacheMu.Unlock()
	if changed && err == nil && env.emit != nil {
		env.emit()
	}
}

type agentOwnerWatchSet struct {
	identity string
	jobs     map[string]context.CancelFunc
}

var agentOwnerWatchesMu sync.Mutex
var agentOwnerWatches = map[string]*agentOwnerWatchSet{}

// Keep one stream for each open chat, bound to the application's lifetime.
func agentOwnerWatchJobs(env agentOwnerEnv, identity string, data json.RawMessage) {
	if env.ctx == nil {
		return
	}
	var jobs []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if json.Unmarshal(data, &jobs) != nil {
		return
	}
	open := map[string]bool{}
	for _, job := range jobs {
		if _, err := agentOwnerPath("job", job.ID); err == nil && job.State != "closed" && job.State != "blocked" {
			open[job.ID] = true
		}
	}
	agentOwnerWatchesMu.Lock()
	set := agentOwnerWatches[env.cachePath]
	if set == nil || set.identity != identity {
		if set != nil {
			for _, cancel := range set.jobs {
				cancel()
			}
		}
		set = &agentOwnerWatchSet{identity: identity, jobs: map[string]context.CancelFunc{}}
		agentOwnerWatches[env.cachePath] = set
		go func(watched *agentOwnerWatchSet) {
			<-env.ctx.Done()
			agentOwnerWatchesMu.Lock()
			if agentOwnerWatches[env.cachePath] == watched {
				for _, cancel := range watched.jobs {
					cancel()
				}
				delete(agentOwnerWatches, env.cachePath)
			}
			agentOwnerWatchesMu.Unlock()
		}(set)
	}
	for id, cancel := range set.jobs {
		if !open[id] {
			cancel()
			delete(set.jobs, id)
		}
	}
	for id := range open {
		if _, ok := set.jobs[id]; !ok {
			ctx, cancel := context.WithCancel(env.ctx)
			set.jobs[id] = cancel
			go agentOwnerWatchJob(ctx, env, identity, id)
		}
	}
	agentOwnerWatchesMu.Unlock()
}

func agentOwnerWatchJob(ctx context.Context, env agentOwnerEnv, identity, id string) {
	lastID := ""
	for ctx.Err() == nil {
		state, err := loadYourAgentState(env.reservation)
		if err != nil || agentOwnerIdentity(env.client.base, state.Hub) != identity {
			return
		}
		lastID = agentOwnerStream(ctx, env, identity, id, lastID)
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func agentOwnerStream(ctx context.Context, env agentOwnerEnv, identity, id, lastID string) string {
	state, err := loadYourAgentState(env.reservation)
	if err != nil || agentOwnerIdentity(env.client.base, state.Hub) != identity {
		return lastID
	}
	jobPath, err := agentOwnerPath("job", id)
	if err != nil {
		return lastID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.client.base+jobPath+"/events", nil)
	if err != nil {
		return lastID
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Isoform-Hub", state.Hub)
	req.Header.Set("Authorization", "Bearer "+state.Token)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = agentServerTimeout
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return lastID
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return lastID
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	eventID, kind := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		switch true {
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			kind = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case line == "":
			if eventID != "" {
				lastID = eventID
			}
			if kind == "state" || kind == "message" || kind == "progress" {
				current, e := loadYourAgentState(env.reservation)
				if e != nil || agentOwnerIdentity(env.client.base, current.Hub) != identity {
					return lastID
				}
				agentOwnerInvalidate(env, state.Hub, "status", "jobs", "balance")
				_, _ = agentOwnerGet(env, "job", id)
			}
			eventID, kind = "", ""
		}
	}
	return lastID
}

// agentOwnerInvalidate ensures that reads started before a successful write
// cannot replace snapshots obtained afterwards.
func agentOwnerInvalidate(env agentOwnerEnv, hub string, kinds ...string) {
	identity := agentOwnerIdentity(env.client.base, hub)
	agentOwnerCacheMu.Lock()
	agentOwnerEpoch[env.cachePath+"\x00"+identity]++
	agentOwnerCacheMu.Unlock()
	for _, kind := range kinds {
		_, _ = agentOwnerGet(env, kind, "")
	}
}

// GetAgentOwnerSnapshot returns the last snapshot and refreshes it in the background.
func (a *App) GetAgentOwnerSnapshot(kind, id string) (AgentOwnerSnapshot, error) {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return AgentOwnerSnapshot{}, err
	}
	return agentOwnerGet(env, kind, id)
}

// agentOwnerProfileUploader is only invoked by the local approval operation.
type agentOwnerProfileUploader struct{ env agentOwnerEnv }

func (u agentOwnerProfileUploader) UploadApprovedProfile(ctx context.Context, payload WorkProfileUpload) (WorkProfileUploadResult, error) {
	state, err := loadYourAgentState(u.env.reservation)
	if err != nil {
		return WorkProfileUploadResult{}, err
	}
	data, err := u.env.client.do(ctx, http.MethodPut, "/v1/owner/profile", payload, state.Hub, state.Token)
	if err != nil {
		return WorkProfileUploadResult{}, err
	}
	var result WorkProfileUploadResult
	if err := json.Unmarshal(data, &result); err != nil {
		return WorkProfileUploadResult{}, errors.New("invalid profile response")
	}
	agentOwnerInvalidate(u.env, state.Hub, "status", "profile")
	return result, nil
}

// AgentOwnerSettings is the owner-controlled server configuration.
type AgentOwnerSettings struct {
	RateCentsPerMinute    int64  `json:"rateCentsPerMinute"`
	Speed                 string `json:"speed"`
	Effort                string `json:"effort"`
	ApproveJobs           bool   `json:"approveJobs"`
	ReviewResults         bool   `json:"reviewResults"`
	DeliveredNotification *bool  `json:"deliveredNotification,omitempty"`
}

// AgentOwnerAction only exposes the documented owner mutations. A successful
// action is never retried automatically because the server may have applied it.
func (a *App) AgentOwnerAction(action, id, note string, settings *AgentOwnerSettings) (json.RawMessage, error) {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return nil, err
	}
	return agentOwnerAction(env, action, id, note, settings)
}

func agentOwnerAction(env agentOwnerEnv, action, id, note string, settings *AgentOwnerSettings) (json.RawMessage, error) {
	method := http.MethodPost
	path := ""
	var payload any
	switch action {
	case "join":
		path = "/v1/owner/join"
	case "cancel_join":
		path = "/v1/owner/join/cancel"
	case "pause":
		path = "/v1/owner/" + action
	case "agree":
		if strings.TrimSpace(note) == "" {
			return nil, errors.New("missing payment terms version")
		}
		path = "/v1/owner/payment-agreement"
		payload = struct {
			AcceptTerms  bool   `json:"acceptTerms"`
			TermsVersion string `json:"termsVersion"`
		}{true, note}
	case "on":
		path = "/v1/owner/on"
	case "settings":
		if settings == nil {
			return nil, errors.New("missing settings")
		}
		method = http.MethodPut
		path = "/v1/owner/settings"
		payload = settings
	case "accept", "decline", "send", "redo":
		// The legacy endpoint chooses a pass on the server. It cannot safely
		// act on a card the owner saw without a server-side revision check.
		return nil, errors.New("owner decision requires a pass and state token")
	case "block", "unblock":
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
			return nil, errors.New("invalid client id")
		}
		path = "/v1/owner/clients/" + url.PathEscape(id) + "/" + action
	default:
		return nil, errors.New("unknown owner action")
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return nil, err
	}
	if action == "settings" && settings.DeliveredNotification != nil {
		status, err := env.client.do(context.Background(), http.MethodGet, "/v1/owner", nil, state.Hub, state.Token)
		if err != nil {
			return nil, err
		}
		var owner struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		if json.Unmarshal(status, &owner) != nil {
			return nil, errors.New("invalid owner settings response")
		}
		if _, supported := owner.Settings["deliveredNotification"]; !supported {
			return nil, errors.New("delivered notification setting unavailable")
		}
	}
	if action == "on" {
		status, err := env.client.do(context.Background(), http.MethodGet, "/v1/owner", nil, state.Hub, state.Token)
		if err != nil {
			return nil, err
		}
		var owner struct {
			PaymentAgreement *struct {
				TermsVersion string `json:"termsVersion"`
				AcceptedAt   string `json:"acceptedAt"`
			} `json:"paymentAgreement"`
		}
		if json.Unmarshal(status, &owner) != nil || owner.PaymentAgreement == nil || owner.PaymentAgreement.TermsVersion == "" || owner.PaymentAgreement.AcceptedAt == "" {
			return nil, errors.New("payment agreement required")
		}
	}
	data, err := env.client.do(context.Background(), method, path, payload, state.Hub, state.Token)
	if err != nil {
		return nil, err
	}
	agentOwnerInvalidate(env, state.Hub, "status", "jobs", "balance", "payouts", "blocked")
	if action == "accept" || action == "decline" || action == "send" || action == "redo" {
		_, _ = agentOwnerGet(env, "job", id)
	}
	return data, nil
}

type AgentOwnerDecisionInput struct {
	Action          string `json:"action"`
	JobID           string `json:"jobId"`
	PassID          string `json:"passId"`
	DecisionVersion *int64 `json:"decisionVersion"`
	Note            string `json:"note,omitempty"`
}

type AgentOwnerDecisionCurrent struct {
	PassID          string `json:"passId"`
	State           string `json:"state"`
	DecisionVersion int64  `json:"decisionVersion"`
}

type AgentOwnerDecisionResult struct {
	Accepted bool                       `json:"accepted"`
	Data     json.RawMessage            `json:"data,omitempty"`
	Error    string                     `json:"error,omitempty"`
	Current  *AgentOwnerDecisionCurrent `json:"current"`
}

func (a *App) AgentOwnerDecision(input AgentOwnerDecisionInput) (AgentOwnerDecisionResult, error) {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return AgentOwnerDecisionResult{}, err
	}
	return agentOwnerDecision(env, input)
}

func agentOwnerDecision(env agentOwnerEnv, input AgentOwnerDecisionInput) (AgentOwnerDecisionResult, error) {
	switch input.Action {
	case "accept", "decline", "send", "redo":
	default:
		return AgentOwnerDecisionResult{}, errors.New("invalid decision")
	}
	jobPath, err := agentOwnerPath("job", input.JobID)
	if err != nil || input.PassID == "" || len(input.PassID) > 128 || input.DecisionVersion == nil || *input.DecisionVersion < 0 {
		return AgentOwnerDecisionResult{}, errors.New("invalid decision card")
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return AgentOwnerDecisionResult{}, err
	}
	// Older servers ignore unknown request fields. Require a fresh response that
	// advertises decision versions before sending any decision to that server.
	data, err := env.client.do(context.Background(), http.MethodGet, jobPath, nil, state.Hub, state.Token)
	if err != nil {
		return AgentOwnerDecisionResult{}, err
	}
	var detail struct {
		Passes []struct {
			DecisionVersion *int64 `json:"decisionVersion"`
		} `json:"passes"`
		Review *struct {
			DecisionVersion *int64 `json:"decisionVersion"`
		} `json:"review"`
	}
	if json.Unmarshal(data, &detail) != nil {
		return AgentOwnerDecisionResult{}, errors.New("invalid decision card")
	}
	if detail.Passes != nil && len(detail.Passes) == 0 && detail.Review == nil {
		agentOwnerInvalidate(env, state.Hub, "jobs")
		_, _ = agentOwnerGet(env, "job", input.JobID)
		return AgentOwnerDecisionResult{Error: "stale_decision"}, nil
	}
	supported := detail.Review != nil && detail.Review.DecisionVersion != nil
	for _, pass := range detail.Passes {
		if pass.DecisionVersion != nil {
			supported = true
		}
	}
	if !supported {
		return AgentOwnerDecisionResult{}, errors.New("server does not support safe decisions")
	}
	payload := struct {
		PassID          string `json:"passId"`
		DecisionVersion *int64 `json:"decisionVersion"`
		Note            string `json:"note,omitempty"`
	}{input.PassID, input.DecisionVersion, input.Note}
	data, err = env.client.do(context.Background(), http.MethodPost, jobPath+"/"+input.Action, payload, state.Hub, state.Token)
	if err != nil {
		var refusal *agentServerError
		if errors.As(err, &refusal) && refusal.Status == http.StatusConflict && refusal.Code == "stale_decision" {
			var current *AgentOwnerDecisionCurrent
			if len(refusal.Current) > 0 && json.Unmarshal(refusal.Current, &current) != nil {
				return AgentOwnerDecisionResult{}, errors.New("invalid stale decision response")
			}
			agentOwnerInvalidate(env, state.Hub, "jobs")
			_, _ = agentOwnerGet(env, "job", input.JobID)
			return AgentOwnerDecisionResult{Error: refusal.Code, Current: current}, nil
		}
		return AgentOwnerDecisionResult{}, err
	}
	agentOwnerInvalidate(env, state.Hub, "status", "jobs", "balance", "payouts")
	_, _ = agentOwnerGet(env, "job", input.JobID)
	return AgentOwnerDecisionResult{Accepted: true, Data: data}, nil
}

// AgentOwnerNeed identifies an owner item without supplying display copy.
type AgentOwnerNeed struct {
	Kind         string `json:"kind"`
	ID           string `json:"id,omitempty"`
	AmountCents  *int64 `json:"amountCents,omitempty"`
	NewCount     int    `json:"newCount,omitempty"`
	ChangedCount int    `json:"changedCount,omitempty"`
}

func agentOwnerNeeds(jobs, payouts, status json.RawMessage) []AgentOwnerNeed {
	needs := []AgentOwnerNeed{}
	var list []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if json.Unmarshal(jobs, &list) == nil {
		for _, job := range list {
			switch job.State {
			case "waiting_owner":
				needs = append(needs, AgentOwnerNeed{Kind: "approval", ID: job.ID})
			case "review":
				needs = append(needs, AgentOwnerNeed{Kind: "review", ID: job.ID})
			}
		}
	}
	var payout struct {
		Status string `json:"status"`
	}
	var owner struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(status, &owner)
	if json.Unmarshal(payouts, &payout) == nil && payout.Status == "not_started" && (owner.State == "setting_up" || owner.State == "on" || owner.State == "paused") {
		needs = append(needs, AgentOwnerNeed{Kind: "payouts"})
	}
	return needs
}

// GetAgentOwnerNeeds returns cached owner items and schedules their refresh.
// The local weekly-profile draft is owned by the separate work-profile service.
func (a *App) GetAgentOwnerNeeds() ([]AgentOwnerNeed, error) {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return nil, err
	}
	jobs, err := agentOwnerGet(env, "jobs", "")
	if err != nil {
		return nil, err
	}
	payouts, err := agentOwnerGet(env, "payouts", "")
	if err != nil {
		return nil, err
	}
	status, err := agentOwnerGet(env, "status", "")
	if err != nil {
		return nil, err
	}
	balance, err := agentOwnerGet(env, "balance", "")
	if err != nil {
		return nil, err
	}
	needs := agentOwnerNeedsWithPayout(env, jobs.Data, payouts.Data, status.Data, balance.Data, time.Now())
	path, err := workProfilePath()
	if err != nil {
		return nil, err
	}
	workProfileStateMu.Lock()
	profile, err := workProfileReadState(path)
	workProfileStateMu.Unlock()
	if err != nil {
		return nil, err
	}
	return agentOwnerAddWorkProfileNeed(needs, profile), nil
}

// GetAgentOwnerNeedsWithDraft includes the local draft identified by the
// work-profile owner. An empty ID means there is no draft ready for review.
func (a *App) GetAgentOwnerNeedsWithDraft(draftID string) ([]AgentOwnerNeed, error) {
	if len(draftID) > 128 {
		return nil, errors.New("invalid draft id")
	}
	needs, err := a.GetAgentOwnerNeeds()
	if err != nil {
		return nil, err
	}
	for _, need := range needs {
		if need.Kind == "draft" {
			return needs, nil
		}
	}
	return agentOwnerAddDraftNeed(needs, draftID), nil
}

func agentOwnerAddWorkProfileNeed(needs []AgentOwnerNeed, state workProfileState) []AgentOwnerNeed {
	if state.ApprovedVersion == 0 || state.Pending == nil {
		return needs
	}
	need := AgentOwnerNeed{Kind: "draft", ID: state.Pending.ID}
	for _, line := range state.Pending.Lines {
		if line.Removed {
			continue
		}
		switch line.Status {
		case "new":
			need.NewCount++
		case "changed":
			need.ChangedCount++
		}
	}
	return append(needs, need)
}

func agentOwnerAddDraftNeed(needs []AgentOwnerNeed, draftID string) []AgentOwnerNeed {
	if strings.TrimSpace(draftID) != "" {
		return append(needs, AgentOwnerNeed{Kind: "draft", ID: draftID})
	}
	return needs
}

const agentOwnerPayoutShownKey = "local:payout-need-shown"

func agentOwnerNeedsWithPayout(env agentOwnerEnv, jobs, payouts, status, balance json.RawMessage, now time.Time) []AgentOwnerNeed {
	needs := agentOwnerNeeds(jobs, payouts, status)
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return needs
	}
	agentOwnerCacheMu.Lock()
	cache := agentOwnerReadCache(env.cachePath, agentOwnerIdentity(env.client.base, state.Hub))
	var shown time.Time
	_ = json.Unmarshal(cache.Entries[agentOwnerPayoutShownKey], &shown)
	agentOwnerCacheMu.Unlock()
	var funds struct {
		Available *int64 `json:"availableCents"`
		Balance   *int64 `json:"balanceCents"`
	}
	_ = json.Unmarshal(balance, &funds)
	amount := funds.Available
	if amount == nil {
		amount = funds.Balance
	}
	filtered := make([]AgentOwnerNeed, 0, len(needs))
	for _, need := range needs {
		if need.Kind == "payouts" {
			if !shown.IsZero() && now.Before(shown.Add(30*24*time.Hour)) {
				continue
			}
			need.AmountCents = amount
		}
		filtered = append(filtered, need)
	}
	return filtered
}

// MarkAgentOwnerPayoutNeedShown is called after Today actually displays the row.
func (a *App) MarkAgentOwnerPayoutNeedShown() error {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return err
	}
	return agentOwnerMarkPayoutNeedShown(env, time.Now())
}

func agentOwnerMarkPayoutNeedShown(env agentOwnerEnv, now time.Time) error {
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return err
	}
	value, err := json.Marshal(now.UTC())
	if err != nil {
		return err
	}
	agentOwnerCacheMu.Lock()
	defer agentOwnerCacheMu.Unlock()
	cache := agentOwnerReadCache(env.cachePath, agentOwnerIdentity(env.client.base, state.Hub))
	cache.Entries[agentOwnerPayoutShownKey] = value
	return agentOwnerWriteCache(env.cachePath, cache)
}

// DownloadAgentOwnerReviewFile saves a checked review file to a location the
// owner chooses. Cancelling the system dialog returns false without a request.
func (a *App) DownloadAgentOwnerReviewFile(jobID, name string) (bool, error) {
	if a.ctx == nil {
		return false, errors.New("save dialog unavailable")
	}
	if _, err := agentOwnerReviewFilePath(jobID, name); err != nil {
		return false, err
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{DefaultFilename: filepath.Base(name)})
	if err != nil || dest == "" {
		return false, err
	}
	env, err := a.agentOwnerEnv()
	if err != nil {
		return false, err
	}
	data, err := agentOwnerReviewFile(env, jobID, name)
	if err != nil {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".agent-review-*")
	if err != nil {
		return false, err
	}
	temp := file.Name()
	defer os.Remove(temp)
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = os.Rename(temp, dest); err != nil {
		return false, err
	}
	return true, nil
}

func agentOwnerReviewFilePath(jobID, name string) (string, error) {
	jobPath, err := agentOwnerPath("job", jobID)
	if err != nil {
		return "", err
	}
	if name == "" || len(name) > 1024 || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") {
		return "", errors.New("invalid review file path")
	}
	return jobPath + "/review/files/" + url.PathEscape(name), nil
}

func agentOwnerReviewFile(env agentOwnerEnv, jobID, name string) ([]byte, error) {
	filePath, err := agentOwnerReviewFilePath(jobID, name)
	if err != nil {
		return nil, err
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return nil, err
	}
	return env.client.request(context.Background(), http.MethodGet, filePath, nil, state.Hub, state.Token, 64<<20)
}

// serveAgentOwnerRead exposes owner snapshots only through the authorized
// mobile bridge. Profile text and credentials are never included in these reads.
func (bridge *mobileBridge) serveAgentOwnerRead(w http.ResponseWriter, r *http.Request, kind string) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	env, err := bridge.app.agentOwnerEnv()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	serveAgentOwnerReadEnv(w, env, kind, r.URL.Query().Get("id"))
}

func serveAgentOwnerReadEnv(w http.ResponseWriter, env agentOwnerEnv, kind, id string) {
	if _, err := agentOwnerPath(kind, id); err != nil {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid owner request")
		return
	}
	view, err := agentOwnerGet(env, kind, id)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func (bridge *mobileBridge) serveAgentOwnerWrite(w http.ResponseWriter, r *http.Request, decision bool) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	env, err := bridge.app.agentOwnerEnv()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	serveAgentOwnerWriteEnv(w, r, env, decision)
}

func serveAgentOwnerWriteEnv(w http.ResponseWriter, r *http.Request, env agentOwnerEnv, decision bool) {
	if decision {
		var input AgentOwnerDecisionInput
		if !mobileDecode(w, r, &input) {
			return
		}
		_, pathErr := agentOwnerPath("job", input.JobID)
		if pathErr != nil || input.PassID == "" || len(input.PassID) > 128 || input.DecisionVersion == nil || *input.DecisionVersion < 0 || (input.Action != "accept" && input.Action != "decline" && input.Action != "send" && input.Action != "redo") {
			mobileJSON(w, http.StatusBadRequest, nil, "invalid decision card")
			return
		}
		result, err := agentOwnerDecision(env, input)
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "owner decision unavailable")
			return
		}
		if !result.Accepted {
			mobileJSON(w, http.StatusConflict, result, result.Error)
			return
		}
		mobileJSON(w, http.StatusOK, result, "")
		return
	}
	var input struct {
		Action       string              `json:"action"`
		TermsVersion string              `json:"termsVersion,omitempty"`
		Settings     *AgentOwnerSettings `json:"settings,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	switch input.Action {
	case "pause", "on", "agree", "settings":
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "invalid owner action")
		return
	}
	data, err := agentOwnerAction(env, input.Action, "", input.TermsVersion, input.Settings)
	if err != nil {
		var refusal *agentServerError
		if errors.As(err, &refusal) && refusal.Status >= 400 && refusal.Status < 500 {
			mobileJSON(w, refusal.Status, nil, refusal.Code)
			return
		}
		mobileJSON(w, http.StatusServiceUnavailable, nil, "owner action unavailable")
		return
	}
	mobileJSON(w, http.StatusOK, data, "")
}
