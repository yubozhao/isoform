package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"wails-gui/internal/identity"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon/appparser"
	"wails-gui/storage"

	"yansu-agent/agent"
)

// personResearchModel is the model used for the dossier synthesis. The run is
// a single non-tool LLM call over local signals + the model's own knowledge,
// so a strong model gives the best one-shot profile.
const personResearchModel = "sonnet"

// personResearchTimeout caps the single synthesis call. The default path does
// NOT do live web search (the browser-driven opencli backends cost ~100s per
// call); it synthesizes from Yansu's local signals plus the model's training
// knowledge, which returns in ~10-20s. The ceiling only catches a hung call.
const personResearchTimeout = 90 * time.Second

// ── DTOs ────────────────────────────────────────────────────────────

// PersonResearchDTO is the JSON shape of a person's dossier sent to the
// frontend. It mirrors storage.PrincipalResearchRow.
type PersonResearchDTO struct {
	PrincipalID  string              `json:"principalId"`
	Status       string              `json:"status"`
	Summary      string              `json:"summary"`
	Markdown     string              `json:"markdown"`
	Sources      []ResearchSourceDTO `json:"sources"`
	Model        string              `json:"model"`
	Error        string              `json:"error"`
	ResearchedAt int64               `json:"researchedAt"`
	UpdatedAt    int64               `json:"updatedAt"`
}

// ResearchSourceDTO mirrors storage.ResearchSource for the frontend.
type ResearchSourceDTO struct {
	N     int    `json:"n"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func personResearchToDTO(r storage.PrincipalResearchRow) PersonResearchDTO {
	sources := make([]ResearchSourceDTO, 0, len(r.Sources))
	for _, s := range r.Sources {
		sources = append(sources, ResearchSourceDTO{N: s.N, Title: s.Title, URL: s.URL})
	}
	return PersonResearchDTO{
		PrincipalID:  r.PrincipalID,
		Status:       r.Status,
		Summary:      r.Summary,
		Markdown:     r.Markdown,
		Sources:      sources,
		Model:        r.Model,
		Error:        r.Error,
		ResearchedAt: r.ResearchedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// ── Bindings ────────────────────────────────────────────────────────

// GetPersonResearch returns the current dossier (or an idle placeholder when
// the person has never been researched) for the given principal.
func (a *App) GetPersonResearch(principalID string) (perfValue PersonResearchDTO, perfErr error) {
	defer logPageBinding("GetPersonResearch", time.Now(), &perfValue)
	store, err := a.requireStore()
	if err != nil {
		return PersonResearchDTO{}, err
	}
	row, err := store.GetPrincipalResearch(context.Background(), principalID)
	if err != nil {
		return PersonResearchDTO{}, err
	}
	return personResearchToDTO(row), nil
}

// StartPersonResearch kicks off (or refreshes) the AI research dossier for a
// principal in the background. It is idempotent: a second call while a run is
// already in flight for the same principal is a no-op. Progress and
// completion are reported via the "people:research" Wails event.
func (a *App) StartPersonResearch(principalID, additionalContext string) error {
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	if _, busy := a.personResearchInFlight.LoadOrStore(principalID, struct{}{}); busy {
		return nil
	}
	p, err := store.GetPrincipal(context.Background(), principalID)
	if err != nil {
		a.personResearchInFlight.Delete(principalID)
		return err
	}

	if err := store.UpsertPrincipalResearchStatus(context.Background(), principalID, storage.PrincipalResearchStatusResearching, ""); err != nil {
		a.personResearchInFlight.Delete(principalID)
		return err
	}
	a.emitPersonResearch(principalID, storage.PrincipalResearchStatusResearching, "")

	go a.runPersonResearch(p, additionalContext)
	return nil
}

// DeletePersonResearch removes the dossier for a principal.
func (a *App) DeletePersonResearch(principalID string) error {
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	if err := store.DeletePrincipalResearch(context.Background(), principalID); err != nil {
		return err
	}
	a.emitPersonResearch(principalID, storage.PrincipalResearchStatusIdle, "")
	return nil
}

// ── Background run ──────────────────────────────────────────────────

func (a *App) runPersonResearch(p storage.PrincipalRow, additionalContext string) {
	defer a.personResearchInFlight.Delete(p.ID)
	if !a.peopleProfileAllowsModelRead(context.Background(), p, nil) {
		a.failPersonResearch(p.ID, "observation is paused")
		return
	}
	// A panic in the synthesis path must not leave the row stuck in
	// 'researching' (disabled button, spins forever) — mark it failed instead.
	defer func() {
		if r := recover(); r != nil {
			a.failPersonResearch(p.ID, fmt.Sprintf("research panicked: %v", r))
		}
	}()

	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		a.failPersonResearch(p.ID, "runner not initialized")
		return
	}

	givenCtx, given, _ := withLearningScope(context.Background(), "about")
	given.add("people", p.ID, 1)
	defer a.finishAssistantCall(given)
	ctx, cancel := context.WithTimeout(givenCtx, personResearchTimeout)
	defer cancel()

	seed := a.buildResearchSeed(ctx, p, additionalContext)
	langInstruction := responseLanguageInstruction(a.currentUILanguageName())

	log.Printf("[people] research starting for %s model=%s budget=%s web=%v", p.ID, personResearchModel, personResearchTimeout, tavilyAPIKey() != "")

	// Trajectory step 1: reviewing what Yansu already knows. Emitted from Go
	// (the synthesis is a single LLM call, so there are no tool events to
	// derive steps from) — gives the user something concrete to watch.
	a.emitPersonResearchStep(p.ID, "thinking", "context", "")

	// Ground the dossier in real web sources (Tavily, ~2-3s) when a key is
	// configured — this is what gives the profile cited, externally-verified
	// facts like Town's People. Falls back to local-only synthesis otherwise.
	web := a.gatherWebFindings(ctx, p, seed)
	if !a.researchSeedAllowsRead(ctx, seed) {
		a.failPersonResearch(p.ID, "research sources changed")
		return
	}
	prompt := personResearchPrompt(seed, langInstruction, web)

	var result strings.Builder
	wroteWriteStep := false
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Prompt:            prompt,
		Priority:          agent.PriorityBackground,
		Model:             personResearchModel,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			// The dossier arrives as the assistant message; accumulate only
			// this type to avoid double-counting streaming deltas.
			result.WriteString(event.Content)
			if strings.TrimSpace(event.Content) != "" && !wroteWriteStep {
				wroteWriteStep = true
				a.emitPersonResearchStep(p.ID, "writing", "write", "")
			}
		case "text":
			if strings.TrimSpace(event.Content) != "" && !wroteWriteStep {
				wroteWriteStep = true
				a.emitPersonResearchStep(p.ID, "writing", "write", "")
			}
		case "error":
			log.Printf("[people] research stream error for %s: %s", p.ID, event.Error)
		}
		return nil
	})
	if err != nil {
		a.failPersonResearch(p.ID, fmt.Sprintf("research run failed: %v", err))
		return
	}

	markdown, summary, _ := parseDossier(result.String())
	if strings.TrimSpace(markdown) == "" {
		a.failPersonResearch(p.ID, "research produced no dossier content")
		return
	}
	// Sources come from the actual Tavily results (not the model) so the [n]
	// citations always resolve to real URLs — no fabricated links.
	sources := webFindingsToSources(web)

	store, err := a.requireStore()
	if err != nil {
		a.failPersonResearch(p.ID, err.Error())
		return
	}
	row := storage.PrincipalResearchRow{
		PrincipalID: p.ID,
		Status:      storage.PrincipalResearchStatusReady,
		Summary:     summary,
		Markdown:    markdown,
		Sources:     sources,
		Model:       personResearchModel,
	}
	if err := store.SavePrincipalResearch(context.Background(), row); err != nil {
		a.failPersonResearch(p.ID, fmt.Sprintf("save dossier failed: %v", err))
		return
	}
	a.emitPersonResearch(p.ID, storage.PrincipalResearchStatusReady, "")
	log.Printf("[people] research ready for %s (%d sources)", p.ID, len(sources))
}

func (a *App) failPersonResearch(principalID, reason string) {
	log.Printf("[people] research failed for %s: %s", principalID, reason)
	if store, err := a.requireStore(); err == nil {
		_ = store.UpsertPrincipalResearchStatus(context.Background(), principalID, storage.PrincipalResearchStatusFailed, reason)
	}
	a.emitPersonResearch(principalID, storage.PrincipalResearchStatusFailed, reason)
}

// emitPersonResearch notifies the Who you know page that a dossier's state changed
// so it can re-fetch (or flip its spinner) without polling.
func (a *App) emitPersonResearch(principalID, status, errMsg string) {
	// The dossier's summary is on the person's page snapshot.
	a.touchDerivedViews(derivedPersonDetail)
	if a.ctx == nil {
		return
	}
	a.emitBrowserEvent("people:research", map[string]string{
		"principalId": principalID,
		"status":      status,
		"error":       errMsg,
	})
}

// emitPersonResearchStep appends a trajectory step (a discrete action the
// agent just took) to the live research view. kind is a stable key the
// frontend localizes; title carries the raw data (a search query or a host)
// that the frontend interpolates as-is — backend emits no UI prose.
func (a *App) emitPersonResearchStep(principalID, phase, kind, title string) {
	if a.ctx == nil {
		return
	}
	a.emitBrowserEvent("people:research", map[string]any{
		"principalId": principalID,
		"status":      storage.PrincipalResearchStatusResearching,
		"phase":       phase,
		"step": map[string]string{
			"kind":  kind,
			"title": title,
		},
	})
}

// ── Seed + prompt ───────────────────────────────────────────────────

// researchSeed is the local, already-known signal about a person that we feed
// to the research agent so it starts from real identifiers rather than a bare
// name.
type researchSeed struct {
	principalID string
	sourceExtra string
	Names       string // display name + aliases
	Handles     string // per-app accounts, one per line
	Recent      string // redacted recent message previews
	Stats       string // message volume + first/last seen per app (drives cadence)
	Extra       string // user-supplied additional context
}

func (a *App) buildResearchSeed(ctx context.Context, p storage.PrincipalRow, additionalContext string) researchSeed {
	if a == nil || !a.peopleProfileAllowsModelRead(ctx, p, nil) {
		return redactResearchSeed(researchSeed{Extra: additionalContext})
	}
	names := []string{p.DisplayName}
	names = append(names, p.Aliases...)

	seed := researchSeed{
		principalID: p.ID,
		sourceExtra: redactActivityModelText(additionalContext),
		Names:       strings.Join(dedupeNonEmpty(names), ", "),
		Extra:       strings.TrimSpace(additionalContext),
	}

	store, err := a.requireStore()
	if err != nil {
		return redactResearchSeed(seed)
	}

	if handles, err := a.peopleHandlesForModel(ctx, store, p); err == nil {
		var lines []string
		for _, h := range handles {
			if personResearchAppProtected(h.App) {
				continue
			}
			label := strings.TrimSpace(h.DisplayHandle)
			if label == "" {
				label = strings.TrimSpace(h.HandleID)
			}
			if label == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", identity.AppDisplayName(h.App), label))
		}
		seed.Handles = strings.Join(lines, "\n")
	}

	if recents, err := store.GetPrincipalRecentMessages(ctx, p.ID, 20); err == nil {
		var lines []string
		for _, m := range recents {
			if personResearchAppProtected(m.App) {
				continue
			}
			preview := strings.TrimSpace(m.Preview)
			if preview == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", identity.AppDisplayName(m.App), preview))
		}
		seed.Recent = strings.Join(lines, "\n")
	}

	if stats, err := store.GetPrincipalStats(ctx, p.ID); err == nil {
		var lines []string
		var total int
		var firstSeen, lastSeen int64
		for _, b := range stats.ByApp {
			if personResearchAppProtected(b.App) {
				continue
			}
			total += b.MessageCount
			if firstSeen == 0 || b.FirstSeenAt < firstSeen {
				firstSeen = b.FirstSeenAt
			}
			if b.LastSeenAt > lastSeen {
				lastSeen = b.LastSeenAt
			}
			lines = append(lines, fmt.Sprintf("- %s: %d messages (%s -> %s)",
				identity.AppDisplayName(b.App), b.MessageCount,
				millisToDate(b.FirstSeenAt), millisToDate(b.LastSeenAt)))
		}
		if total > 0 {
			lines = append([]string{
				fmt.Sprintf("- Total messages observed: %d", total),
				fmt.Sprintf("- First seen %s, last seen %s", millisToDate(firstSeen), millisToDate(lastSeen)),
			}, lines...)
		}
		seed.Stats = strings.Join(lines, "\n")
	}

	return redactResearchSeed(seed)
}

func (a *App) researchSeedAllowsRead(ctx context.Context, seed researchSeed) bool {
	if a == nil || a.IsClosedEyesModeEnabled() || ctx.Err() != nil {
		return false
	}
	if seed.principalID == "" {
		return true
	} // Explicit direct-input helper.
	p, err := a.store.GetPrincipal(ctx, seed.principalID)
	return err == nil && a.peopleProfileAllowsModelRead(ctx, p, nil) && seed == a.buildResearchSeed(ctx, p, seed.sourceExtra)
}

// Person records use canonical app names, while older rows can contain bundle
// IDs. Recheck both forms so changing the sensitive-app list applies to history.
func personResearchAppProtected(app string) bool {
	_, sensitive := pii.IsSensitiveApp(app)
	if sensitive || pii.IsSensitiveAppName(app) || pii.IsSensitiveAppName(identity.AppDisplayName(app)) {
		return true
	}
	canonical := strings.ToLower(strings.TrimSpace(app))
	switch canonical {
	case "imessage":
		canonical = "messages"
	case "zoom_chat":
		canonical = "zoom"
	}
	// Parser metadata retains the bundle IDs lost by canonical person rows.
	// Use it so a localized app display name cannot bypass a bundle rule.
	for _, parser := range appparser.Catalog() {
		if parser.Name != canonical {
			continue
		}
		for _, bundle := range parser.Bundles {
			if _, protected := pii.IsSensitiveApp(bundle); protected {
				return true
			}
		}
	}
	return false
}

func redactResearchSeed(seed researchSeed) researchSeed {
	seed.Names, _ = pii.RedactText(seed.Names)
	seed.Handles, _ = pii.RedactText(seed.Handles)
	seed.Recent, _ = pii.RedactText(seed.Recent)
	seed.Stats, _ = pii.RedactText(seed.Stats)
	seed.Extra, _ = pii.RedactText(seed.Extra)
	return seed
}

// millisToDate formats a unix-millis timestamp as a plain YYYY-MM-DD date for
// the research seed; empty for zero so the prompt doesn't show "1970".
func millisToDate(ms int64) string {
	if ms <= 0 {
		return "unknown"
	}
	return time.UnixMilli(ms).Format("2006-01-02")
}

// ── Web grounding (Tavily) ──────────────────────────────────────────

// webFinding is one numbered, citable web source.
type webFinding struct {
	N       int
	Title   string
	URL     string
	Content string
}

// webResearch bundles the search answer + numbered findings fed to synthesis.
type webResearch struct {
	Query    string
	Answer   string
	Findings []webFinding
}

// gatherWebFindings runs a single fast web search for the person and returns
// numbered, citable results — emitting trajectory steps as it goes. Returns
// nil when no API key is configured or the search fails, so the caller falls
// back to local-only synthesis.
func (a *App) gatherWebFindings(ctx context.Context, p storage.PrincipalRow, seed researchSeed) *webResearch {
	if tavilyAPIKey() == "" {
		log.Printf("[people] research %s: web search off (no TAVILY_API_KEY) — local synthesis only", p.ID)
		return nil
	}
	query := a.generateSearchQuery(ctx, seed)
	if query == "" {
		return nil
	}

	a.emitPersonResearchStep(p.ID, "searching", "search", query)
	log.Printf("[people] research %s: web search bytes=%d", p.ID, len(query))

	sctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	if !a.researchSeedAllowsRead(ctx, seed) {
		return nil
	}
	resp, err := tavilySearch(sctx, query, 6)
	if err != nil {
		log.Printf("[people] research %s: web search failed: %v", p.ID, err)
		return nil
	}

	web := &webResearch{Query: query, Answer: strings.TrimSpace(resp.Answer)}
	reads := 0
	for _, r := range resp.Results {
		u := strings.TrimSpace(r.URL)
		if u == "" {
			continue
		}
		web.Findings = append(web.Findings, webFinding{
			N:       len(web.Findings) + 1,
			Title:   strings.TrimSpace(r.Title),
			URL:     u,
			Content: truncateRunes(strings.TrimSpace(r.Content), 600),
		})
		if reads < 4 {
			a.emitPersonResearchStep(p.ID, "reading", "read", urlHost(u))
			reads++
		}
	}
	log.Printf("[people] research %s: %d web findings", p.ID, len(web.Findings))
	if len(web.Findings) == 0 {
		return nil
	}
	return web
}

// generateSearchQuery uses a fast model to mine the local signals (name +
// handles + message snippets) for the most DISTINGUISHING web-search query —
// a bare nickname like "Bo" returns dictionary/Wikipedia noise, but pairing it
// with a company/project/real name from the messages can pin the real person.
// Falls back to the plain concatenation when the model is unavailable.
func (a *App) generateSearchQuery(ctx context.Context, seed researchSeed) string {
	seed = redactResearchSeed(seed)
	if !a.researchSeedAllowsRead(ctx, seed) {
		return ""
	}
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	fallback := buildSearchQuery(seed)
	if runner == nil {
		return fallback
	}

	prompt := personResearchSearchPrompt(seed)

	qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var out strings.Builder
	_, err := runner.ExecuteTask(qctx, &agent.TaskInput{
		Prompt:            prompt,
		Priority:          agent.PriorityBackground,
		Model:             "haiku",
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(ev *agent.StreamEvent) error {
		if ev.Type == "assistant" {
			out.WriteString(ev.Content)
		}
		return nil
	})
	q := strings.TrimSpace(out.String())
	if i := strings.IndexByte(q, '\n'); i >= 0 {
		q = strings.TrimSpace(q[:i])
	}
	q = strings.Trim(q, "\"'`")
	if err != nil || q == "" || len(q) > 200 {
		return fallback
	}
	q, _ = pii.RedactText(q)
	return q
}

func personResearchSearchPrompt(seed researchSeed) string {
	seed = redactResearchSeed(seed)
	return fmt.Sprintf(`Output the SINGLE best web-search query to find THIS specific real person online.

Rules:
- The query MUST start with the person's name/handle.
- Append ONLY a real IDENTITY term if one is clearly present in the signals: a
  company/employer name, the person's real full name, or a job title.
- Do NOT add product names, project codenames, internal tool names, model names
  (e.g. "deepseek", "yansu"), metrics, or generic tech jargon — those describe
  the work, not the person, and pollute the search.
- If no real identity term exists, output JUST the name. A clean name beats a
  name + jargon.
- Output ONLY the query text — no quotes, no explanation, one line.

Name / aliases: %s
Per-app accounts: %s
Recent message snippets (for identity terms only — ignore project jargon):
%s
User-supplied context: %s`,
		seed.Names, seed.Handles, seed.Recent, seed.Extra)

}

// buildSearchQuery combines the person's names with their strongest extra
// signals (handles, user context) into one broad query.
func buildSearchQuery(seed researchSeed) string {
	seed = redactResearchSeed(seed)
	parts := []string{strings.TrimSpace(seed.Names)}
	// Pull handle values (e.g. "@yetone", "example") off the per-line list.
	for _, line := range strings.Split(seed.Handles, "\n") {
		if i := strings.Index(line, ":"); i >= 0 {
			if v := strings.TrimSpace(line[i+1:]); v != "" {
				parts = append(parts, v)
			}
		}
	}
	if e := strings.TrimSpace(seed.Extra); e != "" {
		parts = append(parts, e)
	}
	return strings.TrimSpace(strings.Join(dedupeNonEmpty(parts), " "))
}

// webFindingsToSources converts the Tavily findings into the dossier's stored
// sources so the inline [n] markers resolve to real URLs.
func webFindingsToSources(web *webResearch) []storage.ResearchSource {
	if web == nil {
		return nil
	}
	sources := make([]storage.ResearchSource, 0, len(web.Findings))
	for _, f := range web.Findings {
		sources = append(sources, storage.ResearchSource{N: f.N, Title: f.Title, URL: f.URL})
	}
	return sources
}

// personResearchSections is the shared output spec — the Town-style section
// set. Headings are written in the user's language (the model is told to), so
// the dossier is a self-contained localized document; the frontend renders it
// as-is.
const personResearchSections = `## Output — a Markdown profile with these sections, in this order.
Write every section HEADING and all prose in the user's language (per the
instruction at the top). Keep a section brief or write "Unknown" rather than
padding it. Do not invent content to fill a section.

1. Contact — emails, phone, company, role, location, per-platform handles.
2. Who They Are — background, what they're known for, current focus.
3. Your Relationship — the nature of the relationship to the user.
4. Recent / Open Threads — the active topics/threads between the user and this
   person, inferred from the recent message snippets (and web if relevant), with
   rough dates when known.
5. Communication Style — BOTH directions: how the user communicates with them,
   and how they communicate with the user (tone, channel, length) — from the
   snippets.
6. Interaction Cadence — frequency/rhythm of contact, derived from the message
   stats (totals, first/last seen, per-app).
7. Uncertainties — a short list of what is NOT verified or is low-confidence.
8. Claims / Citations — a numbered list of the key factual claims; for each give
   Confidence (high/medium/low), Source (web / message history / general
   knowledge), and a ref (a [n] web source, or "message history" / "local").`

// personResearchTail is the parser contract appended to every prompt.
const personResearchTail = `Then append exactly these two delimited blocks (tags stay in English; only the
prose inside follows the language instruction):

<summary>one sentence describing this person (<= 30 words)</summary>
<dossier>the full Markdown profile from the sections above</dossier>`

func personResearchPrompt(seed researchSeed, langInstruction string, web *webResearch) (prompt string) {
	defer func() { prompt, _ = pii.RedactText(prompt) }()
	seed = redactResearchSeed(seed)
	notKnown := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "(none known)"
		}
		return s
	}

	localSignals := fmt.Sprintf(`## Local signals (what the app already knows)
- Names / aliases: %s
- Per-app accounts: %s
- Message stats:
%s
- Recent message snippets between the user and this person (redacted):
%s
- User-supplied context: %s`,
		notKnown(seed.Names), notKnown(seed.Handles), notKnown(seed.Stats), notKnown(seed.Recent), notKnown(seed.Extra))

	// Web-grounded variant: real sources + [n] citations (Town-quality).
	if web != nil && len(web.Findings) > 0 {
		var fb strings.Builder
		if web.Answer != "" {
			fb.WriteString("Search summary: " + web.Answer + "\n\n")
		}
		for _, f := range web.Findings {
			fb.WriteString(fmt.Sprintf("[%d] %s — %s\n%s\n\n", f.N, f.Title, f.URL, f.Content))
		}

		return fmt.Sprintf(`%s

You are a people-research assistant. Write a thorough, sourced profile of the
person below by combining the WEB FINDINGS with the local signals. Use the web
findings for external facts (role, company, background, public handles); use the
local signals for relationship, cadence, communication style, and open threads.

%s

## Web findings (your web sources — cite with [n])
%s

## Rules
- Cite every web-sourced fact with the matching [n] marker (the numbers above).
  Use ONLY those numbers; never invent sources or URLs.
- For facts drawn from the user's own message history, attribute them to
  "message history" in the Claims / Citations section (no [n], no URL).
- If the findings clearly describe a DIFFERENT person than the local signals
  suggest (common-name mismatch), say so and rely on local signals.
- NEVER fabricate emails, phone numbers, or precise facts. Write "Unknown".

%s

%s`,
			langInstruction, localSignals, fb.String(), personResearchSections, personResearchTail)
	}

	// Local-only variant: no web sources available, synthesize from knowledge.
	return fmt.Sprintf(`%s

You are a people-research assistant. Write a thorough, structured profile of the
person below using (a) the local signals and (b) your own training knowledge.
Do NOT call any tools or browse the web — synthesize directly.

%s

## Rules
- If you recognize this person from your training knowledge (public figure,
  founder, notable engineer matching name + handles + context), include what you
  know: background, role, company, public handles.
- Mark anything uncertain "(from general knowledge)" / "(uncertain)" and list it
  under Uncertainties. NEVER fabricate emails, phone numbers, or precise facts.
- For a private individual you have no knowledge of, that's fine — build from the
  local signals and write "Unknown" for unfillable fields. Do not invent details.
- No [n] web citations in this mode; in Claims / Citations attribute each claim
  to "message history", "local signal", or "general knowledge".

%s

%s`,
		langInstruction, localSignals, personResearchSections, personResearchTail)
}

// ── Output parsing ──────────────────────────────────────────────────

var (
	dossierSummaryRe = regexp.MustCompile(`(?s)<summary>\s*(.*?)\s*</summary>`)
	dossierBodyRe    = regexp.MustCompile(`(?s)<dossier>\s*(.*?)\s*</dossier>`)
	dossierSourcesRe = regexp.MustCompile(`(?s)<sources>\s*(.*?)\s*</sources>`)
)

// parseDossier extracts the markdown body, one-line summary, and sources from
// the agent's delimited output. It is tolerant: a missing <dossier> block
// falls back to the raw text with the control blocks stripped, and malformed
// <sources> JSON yields no sources rather than failing the whole run.
func parseDossier(raw string) (markdown, summary string, sources []storage.ResearchSource) {
	raw = strings.TrimSpace(raw)

	if m := dossierBodyRe.FindStringSubmatch(raw); m != nil {
		markdown = strings.TrimSpace(m[1])
	} else {
		// Fallback: strip the control blocks and keep whatever prose remains.
		stripped := dossierSummaryRe.ReplaceAllString(raw, "")
		stripped = dossierSourcesRe.ReplaceAllString(stripped, "")
		markdown = strings.TrimSpace(stripped)
	}

	if m := dossierSummaryRe.FindStringSubmatch(raw); m != nil {
		summary = strings.TrimSpace(m[1])
	}

	if m := dossierSourcesRe.FindStringSubmatch(raw); m != nil {
		var parsed []storage.ResearchSource
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &parsed); err == nil {
			sources = parsed
		}
	}
	return markdown, summary, sources
}

func dedupeNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}
