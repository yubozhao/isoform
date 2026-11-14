package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"yansu-agent/cronsecret"
	"yansu-agent/runtoken"
)

// cronAPIServer is a lightweight REST API for cron job management.
// It listens on 127.0.0.1:0 and exposes endpoints for the yansu CLI.
type cronAPIServer struct {
	app         *App
	listener    net.Listener
	server      *http.Server
	secret      string
	publication *cronAPIPublication
	runTokens   *runtoken.Registry
}

// newCronAPIServer creates and starts the cron API server.
func newCronAPIServer(app *App) (*cronAPIServer, error) {
	// Generate a per-process bearer token.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	secret := hex.EncodeToString(secretBytes)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &cronAPIServer{
		app:       app,
		listener:  listener,
		secret:    secret,
		runTokens: &app.runTokens,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cron/jobs", s.auth(s.handleJobs))
	mux.HandleFunc("/cron/jobs/", s.auth(s.handleJobByID))
	mux.HandleFunc("/activity/sessions", s.auth(s.handleActivitySessions))
	mux.HandleFunc("/activity/sessions/", s.auth(s.handleActivitySessionByID))
	mux.HandleFunc("/activity/summary", s.auth(s.handleActivitySummary))
	mux.HandleFunc("/activity/search", s.auth(s.handleActivitySearch))
	mux.HandleFunc("/memory/list", s.auth(s.handleMemoryList))
	mux.HandleFunc("/memory/search", s.auth(s.handleMemorySearch))
	mux.HandleFunc("/memory/show/", s.auth(s.handleMemoryShow))
	mux.HandleFunc("/skills", s.auth(s.handleSkills))
	mux.HandleFunc("/skills/sources", s.auth(s.handleSkillSources))
	mux.HandleFunc("/skills/search", s.auth(s.handleSkillSearch))
	mux.HandleFunc("/skills/install", s.auth(s.handleSkillInstall))
	mux.HandleFunc("/skills/by-id/", s.auth(s.handleSkillByID))
	mux.HandleFunc("/mcp/sources", s.auth(s.handleMcpSources))
	mux.HandleFunc("/mcp/discover", s.auth(s.handleMcpDiscover))
	mux.HandleFunc("/mcp/install", s.auth(s.handleMcpInstall))
	mux.HandleFunc("/mcp/registry", s.auth(s.handleMcpRegistry))
	mux.HandleFunc("/mcp", s.auth(s.handleMcpList))
	mux.HandleFunc("/mcp/", s.auth(s.handleMcpByID))

	s.server = &http.Server{Handler: mux}
	s.publication = registerCronAPIPublication(app.ctx, s.Port(), secret)

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[cron-api] server error: %v", err)
		}
	}()

	log.Printf("[cron-api] listening on %s", listener.Addr().String())
	return s, nil
}

// Port returns the port the server is listening on.
func (s *cronAPIServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Secret returns the bearer token for authentication.
func (s *cronAPIServer) Secret() string {
	return s.secret
}

// Stop shuts down the server.
func (s *cronAPIServer) Stop() {
	if s.runTokens != nil {
		s.runTokens.RevokeAll()
	}
	if s.publication != nil {
		s.publication.stop()
	}
	if s.server != nil {
		s.server.Close()
	}
}

// auth wraps a handler with bearer token authentication.
func (s *cronAPIServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if s.secret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.secret)) == 1 {
			next(w, r)
			return
		}
		if s.runTokens == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		scope, err := s.runTokens.Verify(token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !cronScopePermits(scope, r) {
			http.Error(w, "outside run scope", http.StatusForbidden)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), cronRunScopeKey{}, scope))
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type skillSourceInfo struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Searchable           bool   `json:"searchable"`
	Installable          bool   `json:"installable"`
	RequiresConfirmation bool   `json:"requires_confirmation"`
	ConfirmationReason   string `json:"confirmation_reason,omitempty"`
}

type skillInstallRequest struct {
	Source             string `json:"source"`
	Ref                string `json:"ref"`
	ConfirmUnmoderated bool   `json:"confirm_unmoderated"`
}

type skillPatchRequest struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Domains map[string]bool `json:"domains,omitempty"`
}

// handleJobs handles POST /cron/jobs (create) and GET /cron/jobs (list).
func (s *cronAPIServer) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jobs := s.app.ListCronJobs()
		if scope, scoped := cronRunScope(r); scoped {
			owned := make([]CronJobState, 0)
			for _, job := range jobs {
				if cronScopeOwnsJob(scope, job) {
					owned = append(owned, job)
				}
			}
			jobs = owned
		}
		writeJSON(w, http.StatusOK, jobs)

	case http.MethodPost:
		var input CronJobInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if scope, scoped := cronRunScope(r); scoped {
			if err := constrainScopedCronInput(scope, &input); err != nil {
				writeError(w, http.StatusForbidden, err.Error())
				return
			}
			// Reading the body may have outlived the run. Recheck immediately
			// before creating work, including expiry of its authority grants.
			if _, err := s.runTokens.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !cronScopePermits(scope, r) {
				http.Error(w, "outside run scope", http.StatusForbidden)
				return
			}
		}
		// Auto-associate with the active chat session when the CLI
		// doesn't provide a sourceSessionId. The active-session file
		// is written by executePrompt and removed when the prompt
		// finishes, so this is deterministic and doesn't depend on
		// the AI passing --source-session.
		if input.SourceSessionID == "" {
			if path := activeSessionFile(); path != "" {
				if data, err := os.ReadFile(path); err == nil {
					input.SourceSessionID = strings.TrimSpace(string(data))
				}
			}
		}
		input.SurfaceKind = cronAPISurfaceKind(input.SurfaceKind)
		job, err := s.app.CreateCronJob(input)
		if err != nil {
			if _, scoped := cronRunScope(r); scoped {
				// Global dedup errors may name another session's job. Preserve
				// status semantics without returning that job's name or ID.
				if errors.Is(err, ErrDuplicateAutomation) {
					writeError(w, http.StatusConflict, "duplicate automation")
					return
				}
				writeError(w, http.StatusInternalServerError, "could not create schedule")
				return
			}
			if errors.Is(err, ErrDuplicateAutomation) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.app.postScheduleAPIChange(job.ID, job.Name, "created")
		writeJSON(w, http.StatusCreated, job)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// cronAPISurfaceKind keeps a schedule created through the API on Upcoming.
// Agent CLIs create the user's own schedules; the app's own jobs and the
// jobs behind handoffs are made inside the app, so a caller here cannot file
// a schedule under a surface that Upcoming hides.
func cronAPISurfaceKind(surfaceKind string) string {
	switch strings.ToLower(strings.TrimSpace(surfaceKind)) {
	case "scheduled":
		return "scheduled"
	default:
		return ""
	}
}

// cronAPIErrorStatus maps a job change error to its HTTP status. An Agency
// proposal nobody approved is the user's to decide, so changing it from the
// API is forbidden rather than a server failure; a schedule the app cannot
// look up right now is refused until it can.
func cronAPIErrorStatus(err error) int {
	if errors.Is(err, errAgencyScheduleAwaitsUser) || errors.Is(err, errSystemScheduleLocked) || errors.Is(err, errPlaybookScheduleUserOnly) {
		return http.StatusForbidden
	}
	if errors.Is(err, errSystemScheduleUnknown) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// handleJobByID handles GET/PUT/DELETE /cron/jobs/:id and POST /cron/jobs/:id/run.
// PUT and DELETE go through the API variants of the bindings, which refuse to
// turn on or delete an Agency proposal: only the user decides those.
func (s *cronAPIServer) handleJobByID(w http.ResponseWriter, r *http.Request) {
	// Parse ID from path: /cron/jobs/{id} or /cron/jobs/{id}/run
	path := strings.TrimPrefix(r.URL.Path, "/cron/jobs/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID is required")
		return
	}

	// Check for /run suffix.
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.app.refuseSystemScheduleFromAPI(id, "run"); err != nil {
			writeError(w, cronAPIErrorStatus(err), err.Error())
			return
		}
		if err := s.app.RunCronJobNow(id); err != nil {
			writeError(w, cronAPIErrorStatus(err), err.Error())
			return
		}
		s.app.postScheduleAPIChange(id, s.app.cronJobName(id), "started")
		writeJSON(w, http.StatusOK, map[string]string{"status": "triggered"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		job, err := s.app.GetCronJob(id)
		if scope, scoped := cronRunScope(r); scoped && (err != nil || !cronScopeOwnsJob(scope, job)) {
			http.Error(w, "outside run scope", http.StatusForbidden)
			return
		}
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, job)

	case http.MethodPut:
		var patch CronJobPatch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		job, err := s.app.updateCronJobFromAPI(id, patch)
		if err != nil {
			writeError(w, cronAPIErrorStatus(err), err.Error())
			return
		}
		s.app.postScheduleAPIChange(id, job.Name, apiUpdateStatus(patch))
		writeJSON(w, http.StatusOK, job)

	case http.MethodDelete:
		name := s.app.cronJobName(id)
		if err := s.app.deleteCronJobFromAPI(id); err != nil {
			writeError(w, cronAPIErrorStatus(err), err.Error())
			return
		}
		s.app.postScheduleAPIChange(id, name, "removed")
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *cronAPIServer) handleSkillSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, []skillSourceInfo{
		{ID: "clawhub", Name: "ClawHub", Searchable: true, Installable: true},
		{ID: "installed", Name: "Installed skills", Searchable: true, Installable: false},
		{
			ID:                   skillHubSourceAgentDirs,
			Name:                 "Installed agent apps",
			Searchable:           true,
			Installable:          true,
			RequiresConfirmation: true,
			ConfirmationReason:   "Skills discovered from other agent apps are local, unmoderated files",
		},
		{
			ID:                   "github",
			Name:                 "GitHub",
			Searchable:           false,
			Installable:          true,
			RequiresConfirmation: true,
			ConfirmationReason:   "GitHub skills are not moderated by ClawHub",
		},
		{
			ID:                   "local",
			Name:                 "Local folder",
			Searchable:           false,
			Installable:          true,
			RequiresConfirmation: true,
			ConfirmationReason:   "Local skills are installed from arbitrary files on this computer",
		},
	})
}

func (s *cronAPIServer) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	skills, err := s.app.ListSkillHubInstalledSkills()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if skills == nil {
		skills = []SkillHubInstalledSkill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *cronAPIServer) handleSkillSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}

	switch source {
	case "clawhub":
		results, err := s.app.SearchClawHubSkills(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if results == nil {
			results = []SkillHubCatalogSkill{}
		}
		writeJSON(w, http.StatusOK, results)
	case "installed":
		results, err := s.searchInstalledSkills(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	case skillHubSourceAgentDirs:
		results, err := s.app.SearchAgentSkills(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if results == nil {
			results = []SkillHubDiscoveredSkill{}
		}
		writeJSON(w, http.StatusOK, results)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported skill search source %q", source))
	}
}

func (s *cronAPIServer) searchInstalledSkills(query string) ([]SkillHubInstalledSkill, error) {
	skills, err := s.app.ListSkillHubInstalledSkills()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return skills, nil
	}
	results := make([]SkillHubInstalledSkill, 0, len(skills))
	for _, skill := range skills {
		haystack := strings.ToLower(strings.Join([]string{
			skill.ID,
			skill.Slug,
			skill.Name,
			skill.Description,
			skill.Source,
			skill.SourceURL,
		}, "\n"))
		if strings.Contains(haystack, query) {
			results = append(results, skill)
		}
	}
	return results, nil
}

func (s *cronAPIServer) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input skillInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	source := strings.TrimSpace(input.Source)
	ref := strings.TrimSpace(input.Ref)
	if source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	if ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required")
		return
	}

	var (
		skill *SkillHubInstalledSkill
		err   error
	)
	switch source {
	case "clawhub":
		skill, err = s.app.InstallClawHubSkill(ref)
	case "github":
		if !input.ConfirmUnmoderated {
			writeError(w, http.StatusBadRequest, "GitHub skills are not moderated; retry with confirm_unmoderated=true after user confirmation")
			return
		}
		skill, err = s.app.InstallSkillFromGitHub(ref)
	case "local":
		if !input.ConfirmUnmoderated {
			writeError(w, http.StatusBadRequest, "local skills are not moderated; retry with confirm_unmoderated=true after user confirmation")
			return
		}
		skill, err = s.app.InstallSkillFromLocal(ref)
	case skillHubSourceAgentDirs:
		if !input.ConfirmUnmoderated {
			writeError(w, http.StatusBadRequest, "skills discovered from other agent apps are not moderated; retry with confirm_unmoderated=true after user confirmation")
			return
		}
		skill, err = s.app.InstallSkillFromAgentDir(ref)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported skill install source %q", source))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, skill)
}

func (s *cronAPIServer) handleSkillByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/skills/by-id/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "skill ID is required")
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var patch skillPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if patch.Enabled == nil && patch.Domains == nil {
			writeError(w, http.StatusBadRequest, "no skill fields to update")
			return
		}
		skill, err := s.app.PatchSkillHubSkill(id, patch.Enabled, patch.Domains)
		if err != nil {
			if errors.Is(err, ErrSkillNotInstalled) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, skill)
	case http.MethodDelete:
		if err := s.app.UninstallSkillHubSkill(id); err != nil {
			if errors.Is(err, ErrSkillNotInstalled) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ──────────────────────────────────────────────
// Activity endpoints
// ──────────────────────────────────────────────

// handleActivitySessions handles GET /activity/sessions?start=<ms>&end=<ms>&limit=<n>&keyword=<kw>
func (s *cronAPIServer) handleActivitySessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	limit := 20
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	var startMs, endMs int64
	if v := q.Get("start"); v != "" {
		startMs, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("end"); v != "" {
		endMs, _ = strconv.ParseInt(v, 10, 64)
	}
	// Convenience: --date YYYY-MM-DD sets start/end to that day.
	if dateStr := q.Get("date"); dateStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local); err == nil {
			startMs = t.UnixMilli()
			endMs = t.AddDate(0, 0, 1).UnixMilli()
		}
	}

	keyword := q.Get("keyword")

	sessions, err := s.app.activitySessionsForExternalRead(limit, offset, startMs, endMs, keyword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []ActivitySessionSummary{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handleActivitySessionByID handles GET /activity/sessions/:id
func (s *cronAPIServer) handleActivitySessionByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/activity/sessions/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}

	detail, err := s.app.activitySessionForExternalRead(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleActivitySummary handles GET /activity/summary?date=YYYY-MM-DD
// Returns the AI-generated narrative journal for the given date. If the
// narrative hasn't been generated yet, triggers generation synchronously
// so the caller always gets a meaningful response (not raw stats JSON).
func (s *cronAPIServer) handleActivitySummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}

	data, err := s.app.dailySummaryWithModelContext(dateStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If no AI narrative exists yet, kick off generation in the background
	// and return status="generating" so the client can poll. Blocking the
	// HTTP response on the LLM call routinely exceeded the CLI's 60s timeout
	// when the rich-model call took longer than usual; the goroutine here is
	// safe because GenerateDailySummary is already guarded by a singleflight
	// keyed on dateStr (app.generateDailySummarySF) — concurrent polls from
	// the same or different clients coalesce into a single LLM call.
	if data.AISummaryStatus != "done" && data.SessionCount > 0 {
		go func() {
			if err := s.app.GenerateDailySummary(dateStr); err != nil {
				log.Printf("[cron-api] background daily summary for %s failed: %v", dateStr, err)
			}
		}()
		data.AISummaryStatus = "generating"
	}

	// Return a compact journal response: narrative + essential context.
	type journalResponse struct {
		Date      string         `json:"date"`
		Journal   string         `json:"journal"`
		Status    string         `json:"status"` // "done", "none", "error"
		IsPartial bool           `json:"isPartial"`
		ActiveMs  int64          `json:"activeMs"`
		Sessions  int            `json:"sessions"`
		TopApps   []AppUsageStat `json:"topApps,omitempty"`
	}

	resp := journalResponse{
		Date:      data.Date,
		Journal:   redactActivityModelText(data.AISummary),
		Status:    data.AISummaryStatus,
		IsPartial: data.IsPartial,
		ActiveMs:  data.TotalActiveMs,
		Sessions:  data.SessionCount,
		TopApps:   data.AppBreakdown,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleActivitySearch handles GET /activity/search?q=<query>&limit=<n>
func (s *cronAPIServer) handleActivitySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	results, err := s.app.SemanticSearchActivitySessions(query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []ActivitySemanticResult{}
	}
	writeJSON(w, http.StatusOK, results)
}

// ──────────────────────────────────────────────
// Memory endpoints
// ──────────────────────────────────────────────

// memoryEntrySummary trims MemoryEntry for list output (drops full Content to
// keep responses small; callers fetch the body via /memory/show/:id).
type memoryEntrySummary struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Dimension string   `json:"dimension"`
	Source    string   `json:"source"`
	UpdatedAt int64    `json:"updatedAt"`
	ModTime   int64    `json:"modTime"`
	SizeBytes int64    `json:"sizeBytes"`
	Apps      []string `json:"apps,omitempty"`
}

func summarizeMemoryEntry(e MemoryEntry) memoryEntrySummary {
	return memoryEntrySummary{
		ID:        e.ID,
		Title:     e.Title,
		Dimension: e.Dimension,
		Source:    e.Source,
		UpdatedAt: e.UpdatedAt,
		ModTime:   e.ModTime,
		SizeBytes: e.SizeBytes,
		Apps:      e.Apps,
	}
}

// handleMemoryList handles GET /memory/list?dimension=X&limit=N
func (s *cronAPIServer) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	dimension := q.Get("dimension")
	limit := 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	useEpoch := s.app.contextUseEpoch()
	entries, err := s.app.memoryEntriesForExternalRead()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]memoryEntrySummary, 0, len(entries))
	for _, e := range entries {
		if dimension != "" && e.Dimension != dimension {
			continue
		}
		out = append(out, summarizeMemoryEntry(e))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
	var usedEntries []contextUseEntry
	for _, entry := range out {
		usedEntries = append(usedEntries, contextUseEntry{"memory", entry.ID, useEpoch})
	}
	s.app.recordContextEntries(usedEntries, "assistant_tool", "")
}

// handleMemorySearch handles GET /memory/search?q=<query>&limit=<n>&dimension=<d>
func (s *cronAPIServer) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	dimension := r.URL.Query().Get("dimension")

	// Over-fetch when filtering by dimension so we can still return `limit`
	// final hits after the post-filter. `Search` already caps at its own
	// candidate limit, so this is cheap.
	searchLimit := limit
	if dimension != "" {
		searchLimit = limit * 4
	}

	useEpoch := s.app.contextUseEpoch()
	results, err := s.app.SearchMemory(query, searchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	filtered := make([]MemorySearchResult, 0, len(results))
	for _, r := range results {
		if dimension != "" {
			if MemorySource(r.Source) != MemorySourceMemory {
				continue
			}
			if ReadMemoryFileDimension(r.Path) != dimension {
				continue
			}
		}
		filtered = append(filtered, r)
		if len(filtered) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, filtered)
	s.app.recordContextEntries(memoryResultUseEntries(filtered, useEpoch), "assistant_tool", "")
}

// handleMemoryShow handles GET /memory/show/:id
func (s *cronAPIServer) handleMemoryShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/memory/show/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory ID is required")
		return
	}

	useEpoch := s.app.contextUseEpoch()
	entry, err := s.app.memoryEntryForExternalRead(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "memory entry not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
	s.app.recordContextEntries([]contextUseEntry{{"memory", entry.ID, useEpoch}}, "assistant_tool", "")
}

// ──────────────────────────────────────────────
// MCP hub endpoints
// ──────────────────────────────────────────────

// mcpInstallRequest is the union body shape for POST /mcp/install. The
// `source` field discriminates between the two install flows: "manual"
// uses the embedded ManualServerInput, "local" picks an entry from the
// discovery report by source_id + name.
type mcpInstallRequest struct {
	Source string `json:"source"`

	// Manual install fields (mirror ManualServerInput).
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Transport   string            `json:"transport,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	SecretEnv   []string          `json:"secret_env,omitempty"`
	URL         string            `json:"url,omitempty"`
	AutoStart   bool              `json:"auto_start,omitempty"`

	// Local install fields. SourceID + DiscoveredName picks the row
	// returned by /mcp/discover; AutoStart is reused.
	SourceID       string `json:"source_id,omitempty"`
	DiscoveredName string `json:"discovered_name,omitempty"`

	// Registry install fields. RegistrySlug picks the row from
	// /mcp/registry.
	RegistrySlug string `json:"registry_slug,omitempty"`
}

// handleMcpList handles GET /mcp.
func (s *cronAPIServer) handleMcpList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	servers, err := s.app.ListMcpServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

// handleMcpSources handles GET /mcp/sources — returns just the per-source
// summary so the CLI can show "we look here" without dumping every entry.
func (s *cronAPIServer) handleMcpSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report, err := s.app.DiscoverLocalMcpServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report.Sources)
}

// handleMcpDiscover handles GET /mcp/discover — full discovery report.
func (s *cronAPIServer) handleMcpDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report, err := s.app.DiscoverLocalMcpServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleMcpRegistry handles:
//
//	GET  /mcp/registry?q=<query>   -> search the catalog
//	POST /mcp/registry             -> {"action":"refresh"} re-fetches now
func (s *cronAPIServer) handleMcpRegistry(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		query := r.URL.Query().Get("q")
		entries, err := s.app.SearchMcpRegistry(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, entries)
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.ToLower(strings.TrimSpace(body.Action)) != "refresh" {
			writeError(w, http.StatusBadRequest, "expected {\"action\":\"refresh\"}")
			return
		}
		entries, err := s.app.RefreshMcpRegistry()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, entries)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMcpInstall handles POST /mcp/install.
func (s *cronAPIServer) handleMcpInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mcpInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Source)) {
	case "", "manual":
		// A bearer token does not authorize an arbitrary local command.
		// Manual stdio servers must be added through the Settings binding.
		switch strings.ToLower(strings.TrimSpace(req.Transport)) {
		case "", mcpTransportStdio:
			writeError(w, http.StatusForbidden, "add servers in Settings")
			return
		}
		view, err := s.app.AddMcpServerManual(ManualServerInput{
			ID:          req.ID,
			Name:        req.Name,
			Description: req.Description,
			Transport:   req.Transport,
			Command:     req.Command,
			Args:        req.Args,
			WorkDir:     req.WorkDir,
			Env:         req.Env,
			SecretEnv:   req.SecretEnv,
			URL:         req.URL,
			AutoStart:   req.AutoStart,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view)
	case "local":
		view, err := s.app.ImportLocalMcpServer(req.SourceID, req.DiscoveredName, req.AutoStart)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view)
	case "registry":
		// req.AutoStart is set by the caller (true for the typical
		// "user just clicked Install" path; false when the CLI passes
		// --no-auto-start). Pass through verbatim.
		view, err := s.app.InstallMcpFromRegistry(req.RegistrySlug, req.AutoStart)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported source: %q (expected manual, local, or registry)", req.Source))
	}
}

// handleMcpByID dispatches all per-server actions:
//
//	GET    /mcp/{id}            -> get
//	GET    /mcp/{id}/tools      -> list tools
//	GET    /mcp/{id}/logs?n=N   -> tail stderr
//	POST   /mcp/{id}/enable     -> enable + spawn
//	POST   /mcp/{id}/disable    -> disable + stop
//	POST   /mcp/{id}/restart    -> restart
//	POST   /mcp/{id}/stop       -> stop without disabling
//	DELETE /mcp/{id}            -> uninstall
func (s *cronAPIServer) handleMcpByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/mcp/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "server ID is required")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			view, err := s.app.GetMcpServer(id)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, view)
		case http.MethodDelete:
			if err := s.app.UninstallMcpServer(id); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "uninstalled"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "tools":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tools, err := s.app.ListMcpServerTools(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tools)
	case "logs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		n := 50
		if v := r.URL.Query().Get("n"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				n = parsed
			}
		}
		lines, err := s.app.TailMcpServerLog(id, n)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
	case "enable":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := s.app.SetMcpServerEnabled(id, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	case "disable":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := s.app.SetMcpServerEnabled(id, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	case "restart":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := s.app.RestartMcpServer(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	case "stop":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.app.StopMcpServer(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown action: %q", action))
	}
}

// writeCronAPIFiles publishes the port only after the credential is stored.
// On macOS the CLI reads the secret from Keychain; other platforms use a
// private file. Temporary Keychain failures retry in the background so startup
// retains the running API. A failure never falls back to a plaintext file.
func writeCronAPIFiles(port int, secret string, opts ...cronsecret.Option) error {
	dir, err := flavorDataDir()
	if err != nil {
		return fmt.Errorf("resolve home for API credentials: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create API directory: %w", err)
	}
	return publishCronAPIFiles(dir, port, secret, cronsecret.Store, waitCronAPIRetry, opts...)
}
