package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/sop"
)

const (
	mcpReadSnippetLimit         = 320
	mcpReadMaxResults           = 20
	mcpReadScanLimit            = 1000
	mcpReadSearchCandidateLimit = 100
)

// Only Source is a code-owned enum. Every stored string, even an identifier
// or status, crosses the same redacted, explicitly untrusted boundary.
type mcpReadItem struct {
	Source     string            `json:"source"`
	ID         mcpUntrustedText  `json:"id"`
	At         int64             `json:"at"`
	EndAt      int64             `json:"end_at,omitempty"`
	AtLocal    string            `json:"at_local,omitempty"`
	EndAtLocal string            `json:"end_at_local,omitempty"`
	Title      *mcpUntrustedText `json:"title,omitempty"`
	Snippet    *mcpUntrustedText `json:"snippet,omitempty"`
	Status     *mcpUntrustedText `json:"status,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	URL        *mcpUntrustedText `json:"url,omitempty"`
}

type mcpReadResult struct {
	uses  []contextUseEntry
	Items []mcpReadItem `json:"items"`
	// Bounded is false when a result or scan cap prevents a complete answer.
	Bounded    bool   `json:"bounded"`
	Truncated  bool   `json:"truncated"`
	Withheld   int    `json:"withheld"`
	Omitted    int    `json:"omitted,omitempty"`
	Total      int    `json:"total,omitempty"`
	NextOffset *int   `json:"next_offset,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func mcpReadTextPtr(value string) *mcpUntrustedText {
	text := mcpReadText(value)
	return &text
}

type mcpReadArguments struct {
	Query  string `json:"query"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	From   string `json:"from"`
	To     string `json:"to"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

func (a *App) callMCPReadTool(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	// The owner portrait tools (mcp_portrait.go) have their own arguments.
	switch name {
	case "portrait_draft":
		if err := decodeStrictJSON(raw, &struct{}{}); err != nil {
			return nil, mcpReadInvalid("invalid tool arguments")
		}
		return a.mcpPortraitDraft(ctx)
	case "portrait_keep":
		client, _ := ctx.Value(mcpReadClientContextKey{}).(string)
		return a.mcpPortraitKeep(ctx, client, raw)
	}
	p, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var args mcpReadArguments
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil {
		return nil, mcpReadInvalid("invalid tool arguments")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, mcpReadInvalid("invalid tool arguments")
	}
	if len(args.Query) > 512 || len(args.Name) > 512 || len(args.Kind) > 64 {
		return nil, mcpReadInvalid("tool argument is too long")
	}
	if args.Offset < 0 || (args.Offset != 0 && name != "calendar_range" && name != "calendar_today") {
		return nil, mcpReadInvalid("offset must be nonnegative and is only supported by calendar tools")
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	args.Limit = min(args.Limit, mcpReadMaxResults)
	result := mcpReadResult{Items: []mcpReadItem{}, Bounded: true}
	switch name {
	case "activity_search":
		if strings.TrimSpace(args.Query) == "" {
			return nil, mcpReadInvalid("query is required")
		}
		err = p.activitySearch(args, &result)
	case "memory_search", "memory_list":
		if name == "memory_search" && strings.TrimSpace(args.Query) == "" {
			return nil, mcpReadInvalid("query is required")
		}
		if name == "memory_list" && args.Kind != "" {
			if _, ok := validMemoryDimensions[args.Kind]; !ok {
				return nil, mcpReadInvalid("unknown memory kind")
			}
		}
		err = p.memoryRead(args, &result)
	case "people_lookup":
		if strings.TrimSpace(args.Name) == "" {
			return nil, mcpReadInvalid("name is required")
		}
		err = p.peopleLookup(args, &result)
	case "calendar_today", "calendar_range":
		err = p.calendarRead(name, args, &result)
	case "brief_today":
		err = p.briefToday(&result)
	case "playbook_status":
		err = p.playbookStatus(&result)
	case "run_history":
		err = p.runHistory(args.Limit, &result)
	default:
		return nil, mcpReadInvalid("unknown read-only Isoform tool")
	}
	if err != nil {
		var invalid *mcpReadValidationError
		if errors.As(err, &invalid) {
			return nil, err
		}
		return nil, errors.New("Isoform read data is unavailable")
	}
	// A pause during a disk read must suppress the in-flight response too.
	current, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return nil, err
	}
	if current.calendar != p.calendar || !reflect.DeepEqual(current.hidden, p.hidden) || !reflect.DeepEqual(current.ignored, p.ignored) || !reflect.DeepEqual(current.pauses, p.pauses) {
		return nil, errors.New("Isoform privacy settings changed; retry the read")
	}
	// Memory readers account for their own limits, including a filter-independent
	// bound for kind lists. A matching private record must not change this flag.
	if name != "activity_search" && name != "people_lookup" && name != "memory_search" && name != "memory_list" && p.privacyBudgetExhausted {
		result.Truncated = true
	}
	result.Bounded = !result.Truncated
	if result.Reason == "" {
		switch true {
		case result.Truncated:
			result.Reason = "This may be a partial answer: source scans and privacy checks are bounded. Narrow the query or use next_offset when provided."
		case len(result.Items) == 0 && result.Withheld > 0:
			result.Reason = "No readable match was found; some source records were withheld by privacy or provenance checks independently of the query."
		case len(result.Items) == 0:
			result.Reason = "No matching records were found in the readable local sources. Privacy restrictions may withhold other sources."
		}
	}
	for i := range result.Items {
		item := &result.Items[i]
		if item.At > 0 {
			item.AtLocal = time.UnixMilli(item.At).In(time.Local).Format(time.RFC3339Nano)
		}
		if item.EndAt > 0 {
			item.EndAtLocal = time.UnixMilli(item.EndAt).In(time.Local).Format(time.RFC3339Nano)
		}
	}
	a.recordContextEntries(result.uses, "assistant_tool", "")
	return result, nil
}

func mcpReadRange(from, to string, defaults bool) (time.Time, time.Time, error) {
	now := time.Now()
	start, end := now.AddDate(0, 0, -7), now
	parse := func(value string) (time.Time, error) {
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return t, nil
		}
		return time.ParseInLocation("2006-01-02", value, time.Local)
	}
	var err error
	if from != "" {
		start, err = parse(from)
		if err != nil {
			return start, end, mcpReadInvalid("invalid from date; use RFC3339 or YYYY-MM-DD")
		}
	} else if !defaults {
		return start, end, mcpReadInvalid("from is required")
	}
	if to != "" {
		end, err = parse(to)
		if err != nil {
			return start, end, mcpReadInvalid("invalid to date; use RFC3339 or YYYY-MM-DD")
		}
		// A calendar date includes that entire local day, including DST days.
		if len(to) == len("2006-01-02") {
			end = end.AddDate(0, 0, 1)
		}
	} else if !defaults {
		return start, end, mcpReadInvalid("to is required")
	}
	if !start.Before(end) || end.After(start.AddDate(0, 0, 31)) {
		return start, end, mcpReadInvalid("date range must be ordered and at most 31 days")
	}
	return start, end, nil
}

func (p *mcpReadPrivacy) activitySearch(args mcpReadArguments, result *mcpReadResult) error {
	start, end, err := mcpReadRange(args.From, args.To, true)
	if err != nil {
		return err
	}
	if p.app.activity == nil || p.app.activity.store == nil {
		result.Reason = "Activity storage is not available."
		return nil
	}
	s := p.app.activity.store
	// Keyword search stays local and read-only: no query embedding, model
	// request, index sync, screenshot read or transcript is needed.
	// Plain JSON without escapes must contain a matching decoded title as
	// literal text. Skip JSON decoding for those definite nonmatches; escaped
	// JSON always takes the full path so Unicode escapes retain their meaning.
	const observations = `WITH observations AS (
		SELECT s.id, COALESCE(x.timestamp, v.start_time + CAST(f.frame_time * 1000 AS INTEGER), s.start_time) AS observed_at,
			s.end_time, f.text, COALESCE(x.app,'') AS snapshot_app,
			COALESCE(x.browser_url,'') AS snapshot_url, COALESCE(x.parsed_json,'') AS snapshot_parsed
			FROM activity_ocr_frames f JOIN activity_sessions s ON s.id=f.session_id
			LEFT JOIN activity_snapshots x ON x.id=f.segment_id AND x.session_id=f.session_id
			LEFT JOIN activity_segments v ON v.id=f.segment_id AND v.session_id=f.session_id
			WHERE COALESCE(s.triage_status,'') != 'protected'
		UNION ALL
		SELECT s.id, e.timestamp, s.end_time, json_extract(e.data,'$.windowTitle'), '', '', ''
			FROM activity_events e JOIN activity_sessions s ON s.id=e.session_id
			WHERE COALESCE(s.triage_status,'') != 'protected' AND
			CASE WHEN instr(lower(e.data),lower(?))=0 AND instr(e.data,char(92))=0
				THEN 0 ELSE json_valid(e.data) END
		UNION ALL
		SELECT s.id, x.timestamp, s.end_time, x.ax_text, COALESCE(x.app,''), COALESCE(x.browser_url,''), COALESCE(x.parsed_json,'')
			FROM activity_snapshots x JOIN activity_sessions s ON s.id=x.session_id
			WHERE COALESCE(s.triage_status,'') != 'protected'
		) `
	// Match before decoding capture provenance. Group first so one busy
	// session cannot spend the candidate budget on repeated observations.
	// Bare columns beside MAX come from that maximum row in SQLite.
	if err := p.searchPrivacyBound(result, mcpReadSearchCandidateLimit); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(p.ctx, observations+`SELECT id,MAX(observed_at),end_time,text,snapshot_app,snapshot_url,snapshot_parsed FROM observations
		WHERE observed_at>=? AND observed_at<? AND instr(lower(text),lower(?))>0
		GROUP BY id ORDER BY MAX(observed_at) DESC,id LIMIT ?`,
		args.Query, start.UnixMilli(), end.UnixMilli(), args.Query, mcpReadSearchCandidateLimit)
	if err != nil {
		return err
	}
	type candidate struct {
		id, text                                 string
		at, endAt                                int64
		snapshotApp, snapshotURL, snapshotParsed string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.at, &item.endAt, &item.text, &item.snapshotApp, &item.snapshotURL, &item.snapshotParsed); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range candidates {
		if !p.snapshotAllowed(item.snapshotApp, item.snapshotURL, item.snapshotParsed) {
			continue
		}
		snippet := mcpReadSnippet(item.text, args.Query)
		if !strings.Contains(strings.ToLower(snippet.Text), strings.ToLower(args.Query)) || !p.sessionAllowed(item.id) {
			continue
		}
		if len(result.Items) == args.Limit {
			result.Truncated = true
			result.Omitted++
			continue
		}
		result.Items = append(result.Items, mcpReadItem{Source: "activity_session", ID: mcpReadText(item.id), At: item.at, EndAt: item.endAt, Snippet: &snippet})
	}
	return nil
}

// Search metadata must not reveal whether a hidden source matched a guessed
// keyword. Withheld remains zero; the partial flag is conservative and based
// on cheap, query-independent source counts. Privacy checks still run only on
// matching candidates. No captured text is decoded by this budget check.
func (p *mcpReadPrivacy) searchPrivacyBound(result *mcpReadResult, sessionLimit int) error {
	if p.app.activity == nil || p.app.activity.store == nil {
		return nil
	}
	var limited bool
	err := p.app.activity.store.db.QueryRowContext(p.ctx, `SELECT
		EXISTS(SELECT 1 FROM activity_sessions LIMIT 1 OFFSET ?) OR
		EXISTS(SELECT 1 FROM activity_events LIMIT 1 OFFSET 10000) OR
		EXISTS(SELECT 1 FROM activity_snapshots LIMIT 1 OFFSET 10000)`, sessionLimit).Scan(&limited)
	if err != nil {
		return err
	}
	// Each session has separate 10k-event and 10k-snapshot privacy caps.
	// Larger global sets may hit either cap before the shared 100k-read budget.
	result.Truncated = result.Truncated || limited
	return nil
}

func (p *mcpReadPrivacy) peopleLookup(args mcpReadArguments, result *mcpReadResult) error {
	people, limited, err := p.app.store.SearchMCPPrincipals(p.ctx, args.Name, mcpReadSearchCandidateLimit)
	if err != nil {
		return err
	}
	result.Truncated = limited
	if err := p.searchPrivacyBound(result, mcpReadSessionCheckLimit); err != nil {
		return err
	}
	for _, person := range people {
		if !strings.Contains(strings.ToLower(mcpReadText(person.DisplayName).Text), strings.ToLower(args.Name)) || !p.personAllowed(person) {
			continue
		}
		if len(result.Items) == args.Limit {
			result.Truncated = true
			result.Omitted++
			continue
		}
		// Notes and recent messages can combine unrelated captures, so never
		// expose them through a lookup of a person's identity.
		result.Items = append(result.Items, mcpReadItem{Source: "person", ID: mcpReadText(person.ID), At: person.UpdatedAt, Title: mcpReadTextPtr(person.DisplayName)})
	}
	return nil
}

func (p *mcpReadPrivacy) calendarRead(name string, args mcpReadArguments, result *mcpReadResult) error {
	var start, end time.Time
	var err error
	if name == "calendar_today" {
		now := time.Now()
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		end = start.AddDate(0, 0, 1)
	} else {
		start, end, err = mcpReadRange(args.From, args.To, false)
		if err != nil {
			return err
		}
	}
	if !p.calendar {
		result.Reason = "Calendar permission is disabled in Isoform Settings."
		return nil
	}
	events, err := p.app.store.ListCalendarEvents(p.ctx, start.UnixMilli(), end.UnixMilli(), false)
	if err != nil {
		return err
	}
	// IDs give equal-start events a stable order across offset pages.
	sort.Slice(events, func(i, j int) bool {
		if events[i].StartAt != events[j].StartAt {
			return events[i].StartAt < events[j].StartAt
		}
		return events[i].ID < events[j].ID
	})
	for _, event := range events {
		if !p.calendarAllowed(event) {
			if p.sessionLimited(event.SessionID) {
				result.Omitted++
				result.Truncated = true
			} else {
				result.Withheld++
			}
			continue
		}
		result.Total++
		if result.Total <= args.Offset {
			continue
		}
		if len(result.Items) == args.Limit {
			result.Truncated = true
			result.Omitted++
			continue
		}
		result.Items = append(result.Items, mcpReadItem{Source: "calendar_event", ID: mcpReadText(event.ID), At: event.StartAt, EndAt: event.EndAt, Title: mcpReadTextPtr(event.Title)})
	}
	if result.Total > args.Offset+len(result.Items) {
		next := args.Offset + len(result.Items)
		result.NextOffset = &next
	}
	return nil
}

func (p *mcpReadPrivacy) playbookStatus(result *mcpReadResult) error {
	if p.app.playbookPlans == nil || p.app.sopStore == nil {
		result.Reason = "Playbook storage is not available."
		return nil
	}
	plans, err := p.app.playbookPlans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if plan.Removed {
			continue
		}
		entry, err := p.app.sopStore.Get(plan.ID)
		if err != nil {
			continue
		}
		allowed := entry.Source == sop.SourceManual || entry.Source == sop.SourceImported
		if len(entry.SourceIDs) > 0 {
			allowed = true
			for _, id := range entry.SourceIDs {
				if !p.memorySessionAllowed(id) {
					allowed = false
					break
				}
			}
		}
		proof, valid := parseSOPReadProvenance(entry.ReadProvenance)
		allowed = allowed && valid
		for _, app := range proof.Apps {
			allowed = allowed && p.appAllowed(app, app)
		}
		// Reuse the model-context SOP gate after bounded source checks. Source
		// receipts and future provenance restrictions have one authority.
		allowed = allowed && p.app.sopEntryAllowsModelRead(entry)
		if !allowed {
			limited := false
			for _, id := range entry.SourceIDs {
				limited = limited || p.sessionLimited(id)
			}
			if limited {
				result.Omitted++
				result.Truncated = true
			} else {
				result.Withheld++
			}
			continue
		}
		if len(result.Items) == mcpReadMaxResults {
			result.Truncated = true
			result.Omitted++
			continue
		}
		enabled := plan.Schedule != nil && plan.Schedule.Enabled && plan.BodyHash != ""
		result.Items = append(result.Items, mcpReadItem{Source: "playbook", ID: mcpReadText(plan.ID), At: plan.UpdatedAt, Title: mcpReadTextPtr(entry.Title), Status: mcpReadTextPtr(string(entry.Status)), Enabled: &enabled})
	}
	return nil
}

func (p *mcpReadPrivacy) runHistory(limit int, result *mcpReadResult) error {
	runs, err := p.app.store.ListMCPRunMetadata(p.ctx, 0, time.Now().Add(time.Second).UnixMilli(), mcpReadScanLimit+1)
	if err != nil {
		return err
	}
	if len(runs) > mcpReadScanLimit {
		result.Truncated = true
		runs = runs[:mcpReadScanLimit]
	}
	for _, run := range runs {
		if !p.timeAllowed(run.StartedAt, run.FinishedAt) {
			result.Withheld++
			continue
		}
		if len(result.Items) == limit {
			result.Truncated = true
			result.Omitted++
			continue
		}
		// Only execution metadata: summaries, errors, prompts and logs lack
		// exhaustive observation provenance and are deliberately withheld.
		result.Items = append(result.Items, mcpReadItem{Source: "scheduled_run", ID: mcpReadText(run.ID), At: run.StartedAt, EndAt: run.FinishedAt, Status: mcpReadTextPtr(run.Status)})
	}
	return nil
}
